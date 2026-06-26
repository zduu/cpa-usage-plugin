// Unit tests for dashboard helpers — run with: node --test go/dashboard/helpers.test.js
const { test } = require('node:test');
const assert = require('node:assert');

// Load helpers in a way that simulates browser globals
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
  assert.strictEqual(helpers.formatMs(500), '500毫秒');
  assert.strictEqual(helpers.formatMs(1500), '1.50秒');
  assert.strictEqual(helpers.formatMs(0), '-');
  assert.strictEqual(helpers.formatMs(-1), '-');
});

test('totalTokens computes token sum', () => {
  const detail = { tokens: { total_tokens: 100, input_tokens: 50, output_tokens: 50 } };
  assert.strictEqual(helpers.totalTokens(detail), 100);
  const detail2 = { tokens: { input_tokens: 30, output_tokens: 20 } };
  assert.strictEqual(helpers.totalTokens(detail2), 50);
  const detail3 = { tokens: { input_tokens: 10, output_tokens: 5, cached_tokens: 8 } };
  // Cached tokens are a discount classification of input tokens, not extra total tokens.
  assert.strictEqual(helpers.totalTokens(detail3), 15);
});

test('detailCost computes cost', () => {
  const prices = { 'gpt-4': { prompt: 30, completion: 60, cache: 15 } };
  const detail = {
    model: 'gpt-4',
    tokens: { input_tokens: 1000000, output_tokens: 500000, reasoning_tokens: 100000, cached_tokens: 200000, cache_tokens: 0, total_tokens: 1600000 }
  };
  // input: (1000000 - 200000) = 800000 / 1e6 * 30 = 24
  // output + reasoning: 600000 / 1e6 * 60 = 36
  // cached: 200000 / 1e6 * 15 = 3
  // total: 63
  const cost = helpers.detailCost(detail, prices);
  assert.ok(Math.abs(cost - 63) < 0.01, 'cost should be ~63, got ' + cost);
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
  assert.ok(Math.abs(cost - 49.1) < 0.01, 'cost should use gpt-4 pricing, got ' + cost);
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
  assert.strictEqual(helpers.looksLikeCredentialId('5312415661d8a481'), true);
  assert.strictEqual(helpers.looksLikeCredentialId('not-hex-id'), false);
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

test('trimCredentialSuffix removes credential suffixes', () => {
  assert.strictEqual(helpers.trimCredentialSuffix('openai · apikey · abc123'), 'openai');
  assert.strictEqual(helpers.trimCredentialSuffix('deepseek'), 'deepseek');
  assert.strictEqual(helpers.trimCredentialSuffix(''), '');
  assert.strictEqual(helpers.trimCredentialSuffix(null), '');
});

test('sourceLabel returns clean source name', () => {
  assert.strictEqual(helpers.sourceLabel({ source: 'openai · key · hash', provider: 'openai' }), 'openai');
  assert.strictEqual(helpers.sourceLabel({ source: 'sk-secret-key', provider: 'my-provider' }), 'my-provider');
  assert.strictEqual(helpers.sourceLabel({}), '未知来源');
});

test('friendlyApiName cleans API names', () => {
  assert.strictEqual(helpers.friendlyApiName('openai · apikey · abc123'), 'openai');
  assert.strictEqual(helpers.friendlyApiName(''), '未知接口');
});

test('clientApiLabel extracts API key label', () => {
  assert.strictEqual(helpers.clientApiLabel({ api_key: 'my-key' }), 'my-key');
  assert.strictEqual(helpers.clientApiLabel({}), '未知 API');
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
    helpers.pluginEndpoint('usage/import', '/v0/management/plugins/usage-statistics/dashboard'),
    '/v0/management/plugins/usage-statistics/usage/import'
  );
  assert.strictEqual(
    helpers.pluginEndpoint('/dashboard-summary', '/v0/management/plugins/usage-statistics/dashboard/'),
    '/v0/management/plugins/usage-statistics/dashboard-summary'
  );
  assert.strictEqual(
    helpers.pluginEndpoint('usage/import', '/v0/resource/plugins/usage-statistics/dashboard'),
    '/v0/management/plugins/usage-statistics/usage/import'
  );
  assert.strictEqual(
    helpers.pluginEndpoint('usage/export', '/standalone/dashboard.html'),
    './usage/export'
  );
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
