// Unit tests for dashboard helpers - run with: node --test go/dashboard/*.test.js
const { test } = require('node:test');
const assert = require('node:assert');

// Load i18n map first so t() polyfill in helpers.js can resolve keys
const i18n = require('./i18n.js');
global.I18N_MAP = i18n.I18N_MAP;
global.fmt = new Intl.NumberFormat('zh-CN');
const helpers = require('./helpers.js');

test('esc escapes HTML', () => {
  assert.strictEqual(helpers.esc('<script>alert(1)</script>'), '&lt;script&gt;alert(1)&lt;/script&gt;');
  assert.strictEqual(helpers.esc('foo & bar'), 'foo &amp; bar');
  assert.strictEqual(helpers.esc(null), '');
  assert.strictEqual(helpers.esc(undefined), '');
});

test('num coerces values safely', () => {
  assert.strictEqual(helpers.num(42), 42);
  assert.strictEqual(helpers.num('42'), 42);
  assert.strictEqual(helpers.num('abc'), 0);
  assert.strictEqual(helpers.num(null), 0);
  assert.strictEqual(helpers.num(undefined), 0);
});

test('pct formats percentage', () => {
  assert.strictEqual(helpers.pct(95.3), '95.3%');
  assert.strictEqual(helpers.pct(100), '100.0%');
  assert.strictEqual(helpers.pct(0), '0.0%');
  assert.strictEqual(helpers.pct(NaN), '-');
});

test('formatMs formats milliseconds', () => {
  assert.strictEqual(helpers.formatMs(500), '500ms');
  assert.strictEqual(helpers.formatMs(113.25), '113.25ms');
  assert.strictEqual(helpers.formatMs(1000), '1.00s');
  assert.strictEqual(helpers.formatMs(1500), '1.50s');
  assert.strictEqual(helpers.formatMs(1500.5), '1.50s');
  assert.strictEqual(helpers.formatMs(0), '-');
  assert.strictEqual(helpers.formatMs(-1), '-');
});

test('formatDurationAndTTFT shows duration and first-token time in one value', () => {
  assert.strictEqual(helpers.formatDurationAndTTFT(181470, 12350), '181.47s / 12.35s');
  assert.strictEqual(helpers.formatDurationAndTTFT(181470, 0), '181.47s / -');
  assert.strictEqual(helpers.formatDurationAndTTFT(120, undefined), '120ms / -');
});

test('duration and first-token labels are localized in every supported language', () => {
  const expected = {
    'zh-CN': ['平均用时', '平均用时', '用时 / 首字'],
    'zh-TW': ['平均用時', '平均用時', '用時 / 首字'],
    en: ['Avg Duration', 'Avg Duration', 'Duration / First Token'],
    ru: ['Среднее время', 'Ср. время', 'Время / первый токен'],
  };
  Object.entries(expected).forEach(([language, labels]) => {
    assert.strictEqual(i18n.I18N_MAP[language].avg_latency, labels[0]);
    assert.strictEqual(i18n.I18N_MAP[language].col_avg_latency, labels[1]);
    assert.strictEqual(i18n.I18N_MAP[language].col_latency, labels[2]);
  });
});

test('formatUsd preserves small non-zero costs', () => {
  assert.strictEqual(helpers.formatUsd(1.2345), 'US$1.23');
  assert.strictEqual(helpers.formatUsd(0.000445), 'US$0.000445');
  assert.strictEqual(helpers.formatUsd(0.0000004), '<US$0.000001');
  assert.strictEqual(helpers.formatUsd(0), 'US$0.00');
});

test('totalTokens computes token sum', () => {
  const detail = { tokens: { total_tokens: 100, input_tokens: 50, output_tokens: 50 } };
  assert.strictEqual(helpers.totalTokens(detail), 100);
  const detail2 = { tokens: { input_tokens: 30, output_tokens: 20 } };
  assert.strictEqual(helpers.totalTokens(detail2), 50);
  const detail3 = { tokens: { input_tokens: 10, output_tokens: 5, cached_tokens: 8 } };
  // Cached tokens are a discount classification of input tokens, not extra total tokens.
  assert.strictEqual(helpers.totalTokens(detail3), 15);
  const detail4 = { provider: 'anthropic', tokens: { input_tokens: 10, output_tokens: 5, cached_tokens: 8, cache_write_tokens: 2 } };
  assert.strictEqual(helpers.totalTokens(detail4), 25);
  const detail5 = { provider: 'claude', tokens: { input_tokens: 10, output_tokens: 5, cached_tokens: 8, cache_write_tokens: 2, total_tokens: 1 } };
  assert.strictEqual(helpers.totalTokens(detail5), 25);
});

test('uncachedInputTokens excludes cache only for cache-inclusive providers', () => {
  assert.strictEqual(helpers.uncachedInputTokens({ provider: 'openai', tokens: { input_tokens: 100, output_tokens: 20, cached_tokens: 30, cache_write_tokens: 10, total_tokens: 120 } }), 60);
  assert.strictEqual(helpers.uncachedInputTokens({ provider: 'anthropic', tokens: { input_tokens: 100, output_tokens: 20, cached_tokens: 30, cache_write_tokens: 10, total_tokens: 160 } }), 100);
  assert.strictEqual(helpers.uncachedInputTokens({ tokens: { input_tokens: 60, output_tokens: 20, cached_tokens: 30, cache_write_tokens: 10, total_tokens: 120 } }), 60);
  assert.strictEqual(helpers.uncachedInputTokens({ provider: 'openai', tokens: { input_tokens: 10, cached_tokens: 20 } }), 0);
});

test('usesExclusiveCacheInput only infers exclusive accounting from a positive total', () => {
  assert.strictEqual(helpers.usesExclusiveCacheInput('anthropic', 100, 20, 40, 160), true);
  assert.strictEqual(helpers.usesExclusiveCacheInput('', 60, 20, 40, 120), true);
  assert.strictEqual(helpers.usesExclusiveCacheInput('', 0, 0, 0, 0), false);
});

test('detailCost computes cost', () => {
  const prices = { 'gpt-4': { prompt: 30, completion: 60, cache: 15 } };
  const detail = {
    model: 'gpt-4',
    tokens: { input_tokens: 1000000, output_tokens: 500000, reasoning_tokens: 100000, cached_tokens: 200000, cache_tokens: 0, total_tokens: 1600000 }
  };
  // input: (1000000 - 200000) = 800000 / 1e6 * 30 = 24
  // output: 500000 / 1e6 * 60 = 30
  // cached: 200000 / 1e6 * 15 = 3
  // total: 57
  const cost = helpers.detailCost(detail, prices);
  assert.ok(Math.abs(cost - 57) < 0.01, 'cost should be ~57, got ' + cost);
});

test('detailCost separates cache reads and writes from an inclusive input total', () => {
  const prices = { model: { prompt: 1, completion: 0, cache: 0.1, cache_write: 1.25 } };
  const detail = {
    model: 'model',
    tokens: {
      input_tokens: 1000000,
      output_tokens: 0,
      total_tokens: 1000000,
      cached_tokens: 200000,
      cache_write_tokens: 300000,
    },
  };
  const cost = helpers.detailCost(detail, prices);
  assert.strictEqual(helpers.cacheTokenTotal(detail.tokens), 500000);
  assert.strictEqual(helpers.cacheReadTokens(detail.tokens), 200000);
  assert.strictEqual(helpers.cacheReadTokens({ cache_tokens: 500000, cached_tokens: 999999, cache_write_tokens: 300000 }), 200000);
  assert.ok(Math.abs(cost - 0.895) < 1e-9, 'cost should be 0.895, got ' + cost);
  detail.provider = 'openai';
  detail.tokens.total_tokens = 1500000;
  const inflatedTotalCost = helpers.detailCost(detail, prices);
  assert.ok(Math.abs(inflatedTotalCost - 0.895) < 1e-9, 'non-Claude inflated total cost should be 0.895, got ' + inflatedTotalCost);
});

test('detailCost keeps Claude-style exclusive input tokens billable', () => {
  const prices = { model: { prompt: 1, completion: 0, cache: 0.1, cache_write: 1.25 } };
  const detail = {
    model: 'model',
    provider: 'anthropic',
    tokens: {
      input_tokens: 500000,
      output_tokens: 0,
      total_tokens: 1000000,
      cached_tokens: 200000,
      cache_tokens: 500000,
      cache_write_tokens: 300000,
    },
  };
  const cost = helpers.detailCost(detail, prices);
  assert.ok(Math.abs(cost - 0.895) < 1e-9, 'cost should be 0.895, got ' + cost);
  delete detail.tokens.total_tokens;
  const missingTotalCost = helpers.detailCost(detail, prices);
  assert.ok(Math.abs(missingTotalCost - 0.895) < 1e-9, 'missing-total cost should be 0.895, got ' + missingTotalCost);
});

test('detailCost matches model prices case-insensitively', () => {
  const prices = { 'gpt-5.5': { prompt: 1, completion: 2, cache: 0.5 } };
  const detail = {
    model: 'GPT-5.5',
    tokens: { input_tokens: 1000000, output_tokens: 1000000, cached_tokens: 100000 }
  };
  const cost = helpers.detailCost(detail, prices);
  assert.ok(Math.abs(cost - 2.95) < 0.01, 'cost should be ~2.95, got ' + cost);
});

test('detailCost uses provider-specific prices with manual override first', () => {
  const prices = {
    'gpt-5.5': { prompt: 1, completion: 2, cache: 0.5 },
    'openai/gpt-5.5': { prompt: 1, completion: 2, cache: 0.5 },
    'openrouter/openai/gpt-5.5': { prompt: 9, completion: 99, cache: 0.9 },
  };
  const detail = {
    provider: 'OpenRouter',
    model: 'openai/gpt-5.5',
    tokens: { input_tokens: 1000000, output_tokens: 1000000, cached_tokens: 100000 }
  };
  const providerCost = helpers.detailCost(detail, prices);
  assert.ok(Math.abs(providerCost - 107.19) < 0.01, 'cost should use openrouter pricing, got ' + providerCost);

  const manual = { 'OPENAI/GPT-5.5': { prompt: 3, completion: 4, cache: 1 } };
  const manualCost = helpers.detailCost(detail, prices, manual);
  assert.ok(Math.abs(manualCost - 6.8) < 0.01, 'cost should use manual pricing, got ' + manualCost);
});

test('bare manual price applies to every provider unless that provider has a manual override', () => {
  const prices = {
    'openai/same-model': { prompt: 1, completion: 0, cache: 0 },
    'openrouter/same-model': { prompt: 9, completion: 0, cache: 0 },
  };
  const bareManual = { 'same-model': { prompt: 3, completion: 0, cache: 0 } };
  const openAI = { provider: 'openai', model: 'same-model', tokens: { input_tokens: 1000000 } };
  const openRouter = { provider: 'openrouter', model: 'same-model', tokens: { input_tokens: 1000000 } };

  assert.strictEqual(helpers.detailCost(openAI, prices, bareManual), 3);
  assert.strictEqual(helpers.detailCost(openRouter, prices, bareManual), 3);

  const scopedManual = Object.assign({}, bareManual, {
    'openrouter/same-model': { prompt: 7, completion: 0, cache: 0 },
  });
  assert.strictEqual(helpers.detailCost(openAI, prices, scopedManual), 3);
  assert.strictEqual(helpers.detailCost(openRouter, prices, scopedManual), 7);
});

test('detailCost falls back from provider-prefixed model to bare model price', () => {
  const prices = { 'gpt-5.5': { prompt: 1.25, completion: 10, cache: 0.125 } };
  const detail = {
    provider: 'openai-compatible',
    model: 'openai/gpt-5.5',
    tokens: { input_tokens: 1000000, output_tokens: 1000000, cached_tokens: 100000 }
  };
  const cost = helpers.detailCost(detail, prices);
  assert.ok(Math.abs(cost - 11.1375) < 0.01, 'cost should fall back to bare model pricing, got ' + cost);
});

test('aggregateCost splits mixed-provider model totals by provider', () => {
  const prices = {
    'openai/gpt-5.5': { prompt: 1, completion: 2, cache: 0.5 },
    'openrouter/openai/gpt-5.5': { prompt: 9, completion: 99, cache: 0.9 },
  };
  const row = {
    model: 'openai/gpt-5.5',
    providers: [
      { provider: 'openai', input_tokens: 1000000, output_tokens: 1000000, cached_tokens: 100000 },
      { provider: 'openrouter', input_tokens: 1000000, output_tokens: 1000000, cached_tokens: 100000 },
    ]
  };
  const cost = helpers.aggregateCost(row, prices);
  assert.ok(Math.abs(cost - 110.14) < 0.01, 'cost should split provider pricing, got ' + cost);
});

test('detailCost returns 0 for unknown model', () => {
  const detail = { model: 'unknown', tokens: { total_tokens: 1000 } };
  assert.strictEqual(helpers.detailCost(detail, {}), 0);
});

test('aggregateCost uses the original model key, not an alias', () => {
  const prices = {
    'gpt-4': { prompt: 10, completion: 20, cache: 1 },
    'claude-alias': { prompt: 1000, completion: 1000, cache: 1000 },
  };
  const row = {
    model: 'gpt-4',
    alias: 'claude-alias',
    input_tokens: 1000000,
    output_tokens: 1000000,
    reasoning_tokens: 1000000,
    cached_tokens: 100000,
  };
  const cost = helpers.aggregateCost(row, prices);
  assert.ok(Math.abs(cost - 29.1) < 0.01, 'cost should use gpt-4 pricing, got ' + cost);
});

test('cacheRate handles inclusive and Claude-style exclusive input totals', () => {
  assert.strictEqual(helpers.cacheRate({ input_tokens: 100, output_tokens: 20, total_tokens: 120, cached_tokens: 40 }), 40);
  assert.ok(Math.abs(helpers.cacheRate({ input_tokens: 100, output_tokens: 20, total_tokens: 160, cached_tokens: 40 }) - 28.57142857142857) < 1e-9);
  assert.strictEqual(helpers.cacheRate({ input_tokens: 0, output_tokens: 20, total_tokens: 60, cached_tokens: 40 }), 100);
  assert.strictEqual(helpers.cacheRate({ input_tokens: 100, output_tokens: 0, total_tokens: 100, cached_tokens: 50, cache_write_tokens: 30 }), 20);
  assert.strictEqual(helpers.cacheRate({ input_tokens: 50, output_tokens: 0, total_tokens: 100, cached_tokens: 50, cache_write_tokens: 30 }), 20);
  assert.strictEqual(helpers.cacheRate({ providers: [
    { provider: 'openai', input_tokens: 100, total_tokens: 100, cached_tokens: 20 },
    { provider: 'anthropic', input_tokens: 80, total_tokens: 100, cached_tokens: 20 },
  ] }), 20);
});

test('hourBucketValue reads padded and plain hour keys', () => {
  assert.strictEqual(helpers.hourBucketValue({ '09': 12 }, 9), 12);
  assert.strictEqual(helpers.hourBucketValue({ '9': 13 }, 9), 13);
  assert.strictEqual(helpers.hourBucketValue({ '00': 5 }, 0), 5);
  assert.strictEqual(helpers.hourBucketValue({ '0': 6 }, 0), 6);
  assert.strictEqual(helpers.hourBucketValue({ '10': '7' }, '10'), 7);
  assert.strictEqual(helpers.hourBucketValue({}, 10), 0);
});

test('orderedRecentHours rotates hours so the current hour is last', () => {
  assert.deepStrictEqual(helpers.orderedRecentHours(['00', '17', '18', '23'], 0), [17, 18, 23, 0]);
  assert.deepStrictEqual(helpers.orderedRecentHours(['00', '17', '18', '23'], 17), [18, 23, 0, 17]);
});

test('hourFromTimestamp parses zoned timestamps in local time', () => {
  assert.strictEqual(helpers.hourFromTimestamp('2026-01-02T16:30:00Z'), new Date('2026-01-02T16:30:00Z').getHours());
  assert.strictEqual(helpers.hourFromTimestamp('2026-01-03T00:30:00+08:00'), new Date('2026-01-03T00:30:00+08:00').getHours());
});

test('dashboardCurrentHour prefers backend metadata over generated_at', () => {
  assert.strictEqual(helpers.dashboardCurrentHour({ generated_at: '2026-01-02T16:30:00Z', _meta: { current_hour: 0 } }), 0);
  assert.strictEqual(helpers.dashboardCurrentHour({ generated_at: '2026-01-02T16:30:00Z', _meta: { current_hour: 24 } }), new Date('2026-01-02T16:30:00Z').getHours());
});

test('looksLikeKey detects API key patterns', () => {
  assert.strictEqual(helpers.looksLikeKey('sk-abc123def456'), true);
  assert.strictEqual(helpers.looksLikeKey('AIzaSyABC123XYZ'), true);
  assert.strictEqual(helpers.looksLikeKey('hf_abcdefghijklmnop'), true);
  assert.strictEqual(helpers.looksLikeKey('pk_test_abc123'), true);
  assert.strictEqual(helpers.looksLikeKey('not-a-key'), false);
  assert.strictEqual(helpers.looksLikeKey('short'), false);
});

test('looksLikeCredentialId detects hex IDs', () => {
  assert.strictEqual(helpers.looksLikeCredentialId('a4e4860e4fc0'), true);
  assert.strictEqual(helpers.looksLikeCredentialId('1111222233334444'), true);
  assert.strictEqual(helpers.looksLikeCredentialId('not-hex-id'), false);
  assert.strictEqual(helpers.looksLikeCredentialId('user-a-example-invalid-codex'), false);
  assert.strictEqual(helpers.looksLikeCredentialId('abc'), false);
});

test('isCredentialMarker detects credential keywords', () => {
  assert.strictEqual(helpers.isCredentialMarker('apikey'), true);
  assert.strictEqual(helpers.isCredentialMarker('api_key'), true);
  assert.strictEqual(helpers.isCredentialMarker('key'), true);
  assert.strictEqual(helpers.isCredentialMarker('credential'), true);
  assert.strictEqual(helpers.isCredentialMarker('auth'), true);
  assert.strictEqual(helpers.isCredentialMarker('provider'), false);
  assert.strictEqual(helpers.isCredentialMarker('source'), false);
});

test('isCredentialLabel detects rendered credential labels', () => {
  assert.strictEqual(helpers.isCredentialLabel('凭证 aaaabbbbccccdddd'), true);
  assert.strictEqual(helpers.isCredentialLabel('credential 2222333344445555'), true);
  assert.strictEqual(helpers.isCredentialLabel('public'), false);
});

test('trimCredentialSuffix removes credential suffixes', () => {
  assert.strictEqual(helpers.trimCredentialSuffix('openai · apikey · abc123'), 'openai');
  assert.strictEqual(helpers.trimCredentialSuffix('codex · user-a@example.invalid · 凭证 aaaabbbbccccdddd'), 'codex · user-a@example.invalid');
  assert.strictEqual(helpers.trimCredentialSuffix('openai-compatible-example-free · public · 凭证 2222333344445555'), 'openai-compatible-example-free · public');
  assert.strictEqual(helpers.trimCredentialSuffix('deepseek'), 'deepseek');
  assert.strictEqual(helpers.trimCredentialSuffix(''), '');
  assert.strictEqual(helpers.trimCredentialSuffix(null), '');
});

test('sourceLabel returns clean source name', () => {
  assert.strictEqual(helpers.sourceLabel({ api: 'codex · 上游 b374b8e7c98ca23c', source: 'codex', provider: 'codex' }), 'codex · 上游 b374b8e7c98ca23c');
  assert.strictEqual(helpers.sourceLabel({ api: 'claude · 上游 f85c45252fee', source: 'claude', provider: 'claude' }), 'claude · 上游 f85c45252fee');
  assert.strictEqual(helpers.sourceLabel({ source: 'openai · key · hash', provider: 'openai' }), 'openai');
  assert.strictEqual(helpers.sourceLabel({ source: 'sk-secret-key', provider: 'my-provider' }), 'my-provider');
  assert.strictEqual(helpers.sourceLabel({}), '未知来源');
});

test('friendlyApiName cleans API names', () => {
  assert.strictEqual(helpers.friendlyApiName('openai · apikey · abc123'), 'openai');
  assert.strictEqual(
    helpers.friendlyApiName('codex · user-a@example.invalid · 凭证 aaaabbbbccccdddd'),
    'codex · user-a@example.invalid'
  );
  assert.strictEqual(
    helpers.friendlyApiName('openai-compatible-example-free · public · 凭证 2222333344445555'),
    'openai-compatible-example-free · public'
  );
  assert.strictEqual(
    helpers.friendlyApiName('codex · 上游 b374b8e7c98ca23c'),
    'codex · 上游 b374b8e7c98ca23c'
  );
  assert.strictEqual(helpers.friendlyApiName(''), '未知接口');
});

test('clientApiLabel extracts API key label', () => {
  assert.strictEqual(helpers.clientApiLabel({ api_key: 'my-key' }), 'my-key');
  assert.strictEqual(helpers.clientApiLabel({ api_key: '  my-key  ' }), 'my-key');
  assert.strictEqual(helpers.clientApiLabel({}), '未知 API');
});

test('clientApiGroupKey prefers hash and falls back to masked API key', () => {
  assert.strictEqual(helpers.clientApiGroupKey({ api_key: 'sk******xx', api_key_hash: 'hash-a' }), 'api_key_hash:hash-a');
  assert.strictEqual(helpers.clientApiGroupKey({ api_key: 'sk******xx', api_key_hash: 'hash-b' }), 'api_key_hash:hash-b');
  assert.strictEqual(helpers.clientApiGroupKey({ api_key: 'sk******xx' }), 'api_key:sk******xx');
  assert.strictEqual(helpers.clientApiGroupKey({ api_key_hash: 'hash-only' }), 'api_key_hash:hash-only');
  assert.strictEqual(helpers.clientApiGroupKey({}), '(unknown)');
});

test('avg computes average', () => {
  assert.strictEqual(helpers.avg([1, 2, 3, 4, 5]), 3);
  assert.strictEqual(helpers.avg([0]), 0);
  assert.strictEqual(helpers.avg([]), 0);
  assert.strictEqual(helpers.avg([100, 200, 300]), 200);
});

test('healthColor returns gradient colors', () => {
  // Success rate of 0 should be red-ish
  const red = helpers.healthColor(0);
  assert.ok(red.startsWith('rgb('));
  // Full success should be green-ish
  const green = helpers.healthColor(1);
  assert.ok(green.startsWith('rgb('));
  // No data returns empty
  assert.strictEqual(helpers.healthColor(-1), '');
});

test('timestampMs parses timestamps', () => {
  const ms = helpers.timestampMs('2026-06-25T10:00:00Z');
  assert.ok(ms > 1700000000000);
  assert.strictEqual(helpers.timestampMs('invalid'), 0);
});

test('pluginEndpoint builds management URLs from plugin resource paths', () => {
  assert.strictEqual(
    helpers.pluginEndpoint('usage/import', '/v0/management/plugins/usage-dashboard-zduu/dashboard'),
    '/v0/management/plugins/usage-dashboard-zduu/usage/import'
  );
  assert.strictEqual(
    helpers.pluginEndpoint('/dashboard-summary', '/v0/management/plugins/usage-dashboard-zduu/dashboard/'),
    '/v0/management/plugins/usage-dashboard-zduu/dashboard-summary'
  );
  assert.strictEqual(
    helpers.pluginEndpoint('usage/import', '/v0/resource/plugins/usage-dashboard-zduu/dashboard'),
    '/v0/resource/plugins/usage-dashboard-zduu/usage/import'
  );
  assert.strictEqual(
    helpers.managementEndpoint('usage/import', '/v0/resource/plugins/usage-dashboard-zduu/dashboard'),
    '/v0/management/plugins/usage-dashboard-zduu/usage/import'
  );
  assert.strictEqual(
    helpers.managementEndpoint('model-prices?model=gpt-4.1', '/v0/resource/plugins/usage-dashboard-zduu/dashboard'),
    '/v0/management/plugins/usage-dashboard-zduu/model-prices?model=gpt-4.1'
  );
  assert.strictEqual(
    helpers.pluginEndpoint('dashboard-events-export-jobs', '/management.html'),
    '/v0/management/plugins/usage-dashboard-zduu/dashboard-events-export-jobs'
  );
  assert.strictEqual(
    helpers.managementEndpoint('model-prices', '/management.html'),
    '/v0/management/plugins/usage-dashboard-zduu/model-prices'
  );
  assert.strictEqual(
    helpers.pluginEndpoint('usage/export', '/standalone/dashboard.html'),
    './usage/export'
  );
});

test('currentManagementKey reads management center storage', () => {
  const storage = {
    values: new Map([[
      'cli-proxy-auth',
      JSON.stringify({ state: { managementKey: 'sk-login-state' } })
    ]]),
    getItem(key) { return this.values.get(key) || null; }
  };
  assert.strictEqual(helpers.currentManagementKey(storage), 'sk-login-state');
});

test('currentManagementKey decodes obfuscated management center storage', () => {
  const host = 'cpa.example.test';
  const ua = 'node-test-agent';
  const keyText = 'cli-proxy-api-webui::secure-storage|' + host + '|' + ua;
  const key = new TextEncoder().encode(keyText);
  const plain = JSON.stringify({ state: { managementKey: 'sk-obfuscated' } });
  const bytes = new TextEncoder().encode(plain);
  let binary = '';
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i] ^ key[i % key.length]);
  const storage = {
    getItem(key) { return key === 'cli-proxy-auth' ? 'enc::v1::' + btoa(binary) : null; }
  };
  assert.strictEqual(helpers.currentManagementKey(storage, host, ua), 'sk-obfuscated');
});

test('groupedRows groups by key', () => {
  const rows = [
    { model: 'gpt-4', total_tokens: 100, cached_tokens: 0, reasoning_tokens: 0, cost: 0.5, failed: false, latency_ms: 200, ttft_ms: 50 },
    { model: 'gpt-4', total_tokens: 200, cached_tokens: 10, reasoning_tokens: 0, cost: 1.0, failed: false, latency_ms: 300, ttft_ms: 60 },
    { model: 'gpt-3', total_tokens: 50, cached_tokens: 0, reasoning_tokens: 0, cost: 0.1, failed: true, latency_ms: 100, ttft_ms: 30 },
  ];
  const groups = helpers.groupedRows(rows, (d) => d.model, (d) => d.model);
  assert.strictEqual(groups.length, 2);
  assert.strictEqual(groups[0].name, 'gpt-4');
  assert.strictEqual(groups[0].requests, 2);
  assert.strictEqual(groups[0].tokens, 300);
  assert.strictEqual(groups[1].name, 'gpt-3');
  assert.strictEqual(groups[1].requests, 1);
  assert.strictEqual(groups[1].failure, 1);
});

test('unwrapPluginPayload returns direct payload unchanged', () => {
  const payload = { added: 2, skipped: 1 };
  assert.deepStrictEqual(helpers.unwrapPluginPayload(payload), payload);
});

test('unwrapPluginPayload throws plugin envelope errors', () => {
  assert.throws(
    () => helpers.unwrapPluginPayload({ ok: false, error: { code: 'invalid_json', message: 'failed to parse import payload' } }),
    /failed to parse import payload/
  );
});

test('unwrapPluginPayload decodes management response body', () => {
  const body = Buffer.from(JSON.stringify({ added: 430, skipped: 0 }), 'utf8').toString('base64');
  const payload = { ok: true, result: { status_code: 200, body } };
  assert.deepStrictEqual(helpers.unwrapPluginPayload(payload), { added: 430, skipped: 0 });
});

test('unwrapPluginPayload decodes top-level management response body', () => {
  const body = Buffer.from(JSON.stringify({ version: 1, usage: { total_requests: 430 } }), 'utf8').toString('base64');
  assert.deepStrictEqual(
    helpers.unwrapPluginPayload({ status_code: 200, body }),
    { version: 1, usage: { total_requests: 430 } }
  );
});

test('fetchAllEventPages fetches every page and preserves filters', async () => {
  const calls = [];
  const base = new URLSearchParams({ range: '24h', model: 'gpt-4.1', api: 'openai' });
  const result = await helpers.fetchAllEventPages(async (params) => {
    calls.push(Object.fromEntries(params.entries()));
    const offset = Number(params.get('offset'));
    const remaining = Math.max(1200 - offset, 0);
    const count = Math.min(Number(params.get('limit')), remaining);
    return {
      total: 1200,
      events: Array.from({ length: count }, (_, i) => ({ id: offset + i })),
    };
  }, base, 500);

  assert.strictEqual(result.events.length, 1200);
  assert.deepStrictEqual(result.events[0], { id: 0 });
  assert.deepStrictEqual(result.events[1199], { id: 1199 });
  assert.deepStrictEqual(calls.map((c) => c.offset), ['0', '500', '1000']);
  assert.ok(calls.every((c) => c.range === '24h' && c.model === 'gpt-4.1' && c.api === 'openai'));
});

test('fetchAllEventPages stops when a short page is returned without total', async () => {
  const calls = [];
  const result = await helpers.fetchAllEventPages(async (params) => {
    calls.push(Number(params.get('offset')));
    return { events: calls.length === 1 ? [{ id: 1 }, { id: 2 }] : [] };
  }, new URLSearchParams(), 500);

  assert.deepStrictEqual(calls, [0]);
  assert.deepStrictEqual(result, { events: [{ id: 1 }, { id: 2 }], total: 2 });
});
