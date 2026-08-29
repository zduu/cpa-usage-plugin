// Pure helper functions - usable in tests without DOM.
// i18n polyfill: when i18n.js is loaded first, global t() already exists and this is skipped.
// When not (test environment), t() is created with zh-CN fallback from I18N_MAP.
// NOTE: no 'var' — must not shadow the global t() from i18n.js.
if (typeof t !== 'function') {
  t = function(key) {
    var args = arguments;
    if (typeof I18N_MAP !== 'undefined' && I18N_MAP['zh-CN'] && I18N_MAP['zh-CN'][key]) {
      return I18N_MAP['zh-CN'][key].replace(/\{(\d+)\}/g, function(m, i) {
        var v = args[+i+1]; return v != null ? String(v) : m;
      });
    }
    return key;
  };
}
const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));
const num = (value) => Number.isFinite(Number(value)) ? Number(value) : 0;
function compact(value) { const n = num(value), abs = Math.abs(n); const trim = (v) => v.toFixed(1).replace(/\.0$/, ''); if (abs >= 1e6) return trim(n / 1e6) + 'M'; if (abs >= 1e3) return trim(n / 1e3) + 'k'; return fmt.format(n) }
const pct = (value) => Number.isFinite(value) ? value.toFixed(1) + '%' : '-';
const trimFixed = (value, digits) => Number(value).toFixed(digits).replace(/\.0+$|(\.\d*?[1-9])0+$/, '$1');
const formatMs = (value) => {
  if (!(Number.isFinite(value) && value > 0)) return '-';
  if (value < 1000) return trimFixed(value, 2) + 'ms';
  return (value / 1000).toFixed(2) + 's';
};
const formatDurationAndTTFT = (durationMs, ttftMs) => formatMs(num(durationMs)) + ' / ' + formatMs(num(ttftMs));
// Intl.NumberFormat 的构造相当贵,而事件表一次渲染就有上百个金额单元格,所以按
// (currency, 小数位) 缓存实例。切换语言时 script.js 会调用 refreshMoneyFormatters()
// 丢弃整个缓存。
var moneyFormatterCache = new Map();
function refreshMoneyFormatters() {
  moneyFormatterCache = new Map();
}
function moneyFormatter(currency, minDigits, maxDigits) {
  var key = currency + ':' + minDigits + ':' + maxDigits;
  var cached = moneyFormatterCache.get(key);
  if (!cached) {
    cached = new Intl.NumberFormat(typeof getFormatLocale === 'function' ? getFormatLocale() : 'zh-CN', { style: 'currency', currency: currency, minimumFractionDigits: minDigits, maximumFractionDigits: maxDigits });
    moneyFormatterCache.set(key, cached);
  }
  return cached;
}
function formatUsd(value) {
  return formatMoney(value, typeof dashboardCurrencyState !== 'undefined' ? dashboardCurrencyState : null);
}
function formatMoney(usdAmount, currencyState) {
  var value = Number(usdAmount);
  if (!Number.isFinite(value)) return '-';
  var state = currencyState || {};
  var currency = state.currency === 'CNY' && Number(state.rate) > 0 ? 'CNY' : 'USD';
  if (currency === 'CNY') value *= Number(state.rate);
  var abs = Math.abs(value);
  // 用 Intl 的 currency 样式而不是手拼前缀,货币符号位置和分隔符才会跟随 locale
  // (ru-RU 是 "1,50 $" 而不是 "$1.50")。负数保持原样显示,不夹到 0。
  if (abs === 0) return moneyFormatter(currency, 2, 2).format(0);
  // 极小值只显示阈值,不显示无意义的一长串 0。负号由 Intl 自己按 locale 排版,这里
  // 只加「小于/大于」前缀 —— 手工再拼一个 '-' 会出现 '>--US$0.000001'。
  if (abs < 0.000001) return (value < 0 ? '>' : '<') + moneyFormatter(currency, 6, 6).format(value < 0 ? -0.000001 : 0.000001);
  if (abs < 0.01) return moneyFormatter(currency, 2, 6).format(value);
  return moneyFormatter(currency, 2, 2).format(value);
}
function providerUsesExclusiveCache(provider) { return /^(anthropic|claude)(?:-|$)/i.test(String(provider || '').trim()) }
function usesExclusiveCacheInput(provider, inputTokens, outputTokens, cacheTokens, totalTokensValue) { const providerKey = String(provider || '').trim(); const input = Math.max(num(inputTokens), 0); const output = Math.max(num(outputTokens), 0); const cache = Math.max(num(cacheTokens), 0); const total = Math.max(num(totalTokensValue), 0); return providerUsesExclusiveCache(providerKey) || (!providerKey && total > 0 && total >= input + output + cache) }
function totalTokens(detail) { const t = detail.tokens || {}; const input = Math.max(num(t.input_tokens), 0); const output = Math.max(num(t.output_tokens), 0); const cache = cacheTokenTotal(t); const computed = input + output + (usesExclusiveCacheInput(detail && detail.provider, input, output, cache, t.total_tokens) ? cache : 0); return Math.max(num(t.total_tokens), computed, 0) }
function uncachedInputTokens(detail) { const t = detail && detail.tokens || {}; const input = Math.max(num(t.input_tokens), 0); const cache = cacheTokenTotal(t); return usesExclusiveCacheInput(detail && detail.provider, input, t.output_tokens, cache, t.total_tokens) ? input : Math.max(input - cache, 0) }
const normalizedPriceIndexCache = typeof WeakMap !== 'undefined' ? new WeakMap() : null;
function normalizedPriceIndex(prices) { if (!prices || typeof prices !== 'object') return new Map(); const keys = Object.keys(prices); if (normalizedPriceIndexCache) { const cached = normalizedPriceIndexCache.get(prices); if (cached && cached.size === keys.length) return cached.index; } const index = new Map(); keys.forEach((model) => { const key = String(model || '').trim().toLowerCase(); if (key) index.set(key, prices[model]); }); if (normalizedPriceIndexCache) normalizedPriceIndexCache.set(prices, { size: keys.length, index }); return index }
function directPriceForModel(model, prices) { if (!prices) return null; if (prices[model]) return prices[model]; const key = String(model || '').trim().toLowerCase(); return key ? (normalizedPriceIndex(prices).get(key) || null) : null }
function priceLookupKeys(model, provider) { const providerKey = String(provider || '').trim(); const modelKey = String(model || '').trim(); const keys = []; const seen = new Set(); const add = (key) => { key = String(key || '').trim(); const norm = key.toLowerCase(); if (!norm || seen.has(norm)) return; keys.push(key); seen.add(norm); }; if (providerKey && modelKey) add(providerKey + '/' + modelKey); add(modelKey); const slash = modelKey.indexOf('/'); if (slash > 0 && slash < modelKey.length - 1) add(modelKey.slice(slash + 1)); return keys }
function priceForModel(model, prices, provider, manualPrices) { const keys = priceLookupKeys(model, provider); for (const key of keys) { const price = directPriceForModel(key, manualPrices); if (price) return price; } for (const key of keys) { const price = directPriceForModel(key, prices); if (price) return price; } return null }
function cacheTokenTotal(tokens) { const t = tokens || {}; const cacheWrite = Math.max(num(t.cache_write_tokens), 0); const explicitCacheTotal = Math.max(num(t.cache_tokens), 0); return explicitCacheTotal > 0 ? Math.max(explicitCacheTotal, num(t.cached_tokens), cacheWrite) : Math.max(num(t.cached_tokens), 0) + cacheWrite }
function cacheReadTokens(tokens) { const t = tokens || {}; const explicitCacheTotal = Math.max(num(t.cache_tokens), 0); return explicitCacheTotal > 0 ? Math.max(explicitCacheTotal - Math.max(num(t.cache_write_tokens), 0), 0) : Math.max(num(t.cached_tokens), 0) }
function tokenCost(model, inputTokens, outputTokens, totalTokensValue, cachedTokens, cacheWriteTokens, reasoningTokens, prices, provider, manualPrices) { const p = priceForModel(model, prices, provider, manualPrices); if (!p) return 0; const output = Math.max(num(outputTokens), 0); const cacheRead = Math.max(num(cachedTokens), 0); const cacheWrite = Math.max(num(cacheWriteTokens), 0); const cacheTotal = cacheRead + cacheWrite; const inputValue = Math.max(num(inputTokens), 0); const input = usesExclusiveCacheInput(provider, inputValue, output, cacheTotal, totalTokensValue) ? inputValue : Math.max(inputValue - cacheTotal, 0); return input / 1e6 * num(p.prompt) + output / 1e6 * num(p.completion) + cacheRead / 1e6 * num(p.cache) + cacheWrite / 1e6 * num(p.cache_write) }
function detailCost(detail, prices, manualPrices) { if (detail && Object.prototype.hasOwnProperty.call(detail, 'cost_usd')) return Math.max(num(detail.cost_usd), 0); const t = detail.tokens || {}; const cacheWrite = Math.max(num(t.cache_write_tokens), 0); return tokenCost(detail.model, t.input_tokens, t.output_tokens, totalTokens(detail), cacheReadTokens(t), cacheWrite, t.reasoning_tokens, prices, detail.provider, manualPrices) }
function aggregateCost(row, prices, manualPrices) { if (row && Object.prototype.hasOwnProperty.call(row, 'estimated_cost')) return Math.max(num(row.estimated_cost), 0); const providers = Array.isArray(row && row.providers) ? row.providers : []; if (providers.length) return providers.reduce((sum, p) => sum + tokenCost(row.model, p.input_tokens, p.output_tokens, p.total_tokens, p.cached_tokens, p.cache_write_tokens, p.reasoning_tokens, prices, p.provider, manualPrices), 0); return tokenCost(row && row.model, row && row.input_tokens, row && row.output_tokens, row && row.total_tokens, row && row.cached_tokens, row && row.cache_write_tokens, row && row.reasoning_tokens, prices, row && row.provider, manualPrices) }
function looksLikeKey(v) { return typeof v === 'string' && (v.startsWith('sk-') || v.startsWith('AIza') || v.startsWith('hf_') || v.startsWith('pk_') || v.startsWith('rk_') || v.length >= 80) }
function looksLikeCredentialId(v) { const s = String(v || '').trim(); return /^[a-f0-9]{8,}$/i.test(s) || (s.length >= 32 && !/[ ./_-]/.test(s)) }
function isCredentialMarker(v) { return /^(api[-_ ]?key|apikey|key|credential|auth)$/i.test(String(v || '').trim()) }
function isCredentialLabel(v) { return /^(凭证|credential|auth)\s+\S+$/i.test(String(v || '').trim()) }
function trimCredentialSuffix(value) {
  let s = String(value ?? '').trim(); if (!s) return '';
  const dot = s.split(' · ').map((p) => p.trim()).filter(Boolean);
  const marker = dot.findIndex(isCredentialMarker);
  if (marker > 0) return dot.slice(0, marker).join(' · ');
  if (dot.length > 1 && isCredentialLabel(dot[dot.length - 1])) return dot.slice(0, -1).join(' · ');
  if (dot.length > 1 && looksLikeCredentialId(dot[dot.length - 1])) return dot.slice(0, -1).join(' · ');
  const colon = s.split(':').map((p) => p.trim()).filter(Boolean);
  if (colon.length >= 3 && looksLikeCredentialId(colon[colon.length - 1])) return colon.slice(0, -1).join(':');
  return s;
}
function sourceLabel(detail) { const a = String(detail.api || '').trim(); if (a) return friendlyApiName(a); const s = trimCredentialSuffix(detail.source); if (s && !looksLikeKey(s)) return s; const p = trimCredentialSuffix(detail.provider); if (p && !looksLikeKey(p)) return p; return t('unknown_source') }
function sourceKey(detail) { return sourceLabel(detail) }
function friendlyApiName(apiName) { const clean = trimCredentialSuffix(apiName); if (!clean) return t('unknown_interface'); const parts = clean.split(' · ').filter(function (p) { return !looksLikeKey(p) && !isCredentialMarker(p) && !isCredentialLabel(p) && !looksLikeCredentialId(p) }); return parts.length ? parts.join(' · ') : clean }
function clientApiLabel(detail) { const label = String((detail && detail.api_key) || '').trim(); return label || t('unknown_api') }
function clientApiGroupKey(detail) {
  const hash = String((detail && detail.api_key_hash) || '').trim();
  if (hash) return 'api_key_hash:' + hash;
  const label = String((detail && detail.api_key) || '').trim();
  if (label) return 'api_key:' + label;
  return '(unknown)';
}
function avg(values) { const xs = values.map(num).filter((v) => v > 0); return xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : 0 }
function bucketSeries(rows, metric, minutes, count) {
  const now = Date.now(); const step = minutes * 60e3; const start = now - step * count; const arr = new Array(count).fill(0);
  rows.forEach((d) => { const idx = Math.floor((d.timestamp_ms - start) / step); if (idx >= 0 && idx < count) arr[idx] += metric === 'tokens' ? d.total_tokens : metric === 'cost' ? d.cost : 1 });
  return arr;
}
function hourFromTimestamp(value, fallbackDate) {
  if (typeof value === 'string') {
    const hasZone = /(?:Z|[+-]\d{2}:?\d{2})$/i.test(value);
    if (!hasZone) {
      const match = value.match(/[T\s](\d{1,2})(?::\d{2})?/);
      if (match) {
        const hour = Number(match[1]);
        if (Number.isFinite(hour) && hour >= 0 && hour < 24) return hour;
      }
    }
  }
  const date = value ? new Date(value) : (fallbackDate || new Date());
  const hour = date && typeof date.getHours === 'function' ? date.getHours() : NaN;
  return Number.isFinite(hour) && hour >= 0 && hour < 24 ? hour : new Date().getHours();
}
function dashboardCurrentHour(summary) {
  const metaHour = Number(summary && summary._meta && summary._meta.current_hour);
  if (Number.isFinite(metaHour) && metaHour >= 0 && metaHour < 24) return Math.floor(metaHour);
  return hourFromTimestamp(summary && summary.generated_at);
}
function orderedRecentHours(hours, currentHour) {
  const ordered = Array.from(new Set((hours || []).map(Number).filter((v) => Number.isFinite(v) && v >= 0 && v < 24))).sort((a, b) => a - b);
  const hour = Number(currentHour);
  if (!ordered.length || !Number.isFinite(hour)) return ordered;
  const start = (Math.floor(hour) + 1) % 24;
  const distance = (v) => (v - start + 24) % 24;
  return ordered.sort((a, b) => distance(a) - distance(b));
}
function healthColor(rate) { if (rate < 0) return ''; const stops = [[239, 68, 68], [250, 204, 21], [34, 197, 94]]; const seg = rate < .5 ? 0 : 1; const t = seg === 0 ? rate * 2 : (rate - .5) * 2; const a = stops[seg], b = stops[seg + 1]; return 'rgb(' + a.map((v, i) => Math.round(v + (b[i] - v) * t)).join(',') + ')' }
function healthCellStyle(i, count, total, rate) { const rows = 7, cols = Math.ceil(count / rows), age = count - 1 - i, col = cols - Math.floor(age / rows), row = rows - (age % rows); return 'grid-column:' + col + ';grid-row:' + row + ';' + (total ? 'background:' + healthColor(rate) : '') }
function timestampMs(value) { const ms = Date.parse(value); return Number.isFinite(ms) ? ms : 0 }
function pluginEndpoint(path, pathname) {
  const clean = String(path || '').replace(/^\/+/, '');
  const current = String(pathname || (typeof location !== 'undefined' ? location.pathname : ''));
  if (/\/management\.html\/?$/.test(current)) return '/v0/management/plugins/usage-dashboard-zduu/' + clean;
  const resourceMarker = '/resource/plugins/usage-dashboard-zduu/';
  const resourceIdx = current.indexOf(resourceMarker);
  if (resourceIdx >= 0) return current.slice(0, resourceIdx + resourceMarker.length) + clean;
  const managementMarker = '/management/plugins/usage-dashboard-zduu/';
  const managementIdx = current.indexOf(managementMarker);
  if (managementIdx >= 0) return current.slice(0, managementIdx + managementMarker.length) + clean;
  return './' + clean;
}
function managementEndpoint(path, pathname) {
  const clean = String(path || '').replace(/^\/+/, '');
  const current = String(pathname || (typeof location !== 'undefined' ? location.pathname : ''));
  if (/\/management\.html\/?$/.test(current)) return '/v0/management/plugins/usage-dashboard-zduu/' + clean;
  const resourceMarker = '/resource/plugins/usage-dashboard-zduu/';
  const resourceIdx = current.indexOf(resourceMarker);
  if (resourceIdx >= 0) return current.slice(0, resourceIdx) + '/management/plugins/usage-dashboard-zduu/' + clean;
  const managementMarker = '/management/plugins/usage-dashboard-zduu/';
  const managementIdx = current.indexOf(managementMarker);
  if (managementIdx >= 0) return current.slice(0, managementIdx + managementMarker.length) + clean;
  return './' + clean;
}
function decodeManagementStorage(value, host, userAgent) {
  const raw = String(value || '');
  const prefix = 'enc::v1::';
  if (!raw.startsWith(prefix)) return raw;
  const keyText = 'cli-proxy-api-webui::secure-storage|' + String(host || '') + '|' + String(userAgent || '');
  const key = new TextEncoder().encode(keyText);
  const binary = atob(raw.slice(prefix.length));
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i) ^ key[i % key.length];
  return new TextDecoder().decode(bytes);
}
function parseManagementStorage(value, host, userAgent) {
  if (!value) return null;
  const decoded = decodeManagementStorage(value, host, userAgent);
  try { return JSON.parse(decoded) } catch { return decoded }
}
function currentManagementKey(storage, host, userAgent) {
  const store = storage || (typeof localStorage !== 'undefined' ? localStorage : null);
  if (!store || typeof store.getItem !== 'function') return '';
  const currentHost = host || (typeof location !== 'undefined' ? location.host : '');
  const currentUA = userAgent || (typeof navigator !== 'undefined' ? navigator.userAgent : '');
  const auth = parseManagementStorage(store.getItem('cli-proxy-auth'), currentHost, currentUA);
  const key = auth && typeof auth === 'object' ? ((auth.state && auth.state.managementKey) || auth.managementKey || '') : '';
  if (typeof key === 'string' && key.trim()) return key.trim();
  const legacy = parseManagementStorage(store.getItem('managementKey'), currentHost, currentUA);
  if (typeof legacy === 'string') return legacy.trim();
  if (legacy && typeof legacy === 'object') return String((legacy.state && legacy.state.managementKey) || legacy.managementKey || '').trim();
  return '';
}
function groupedRows(rows, keyFn, nameFn) {
  const map = new Map();
  rows.forEach((d) => { const key = keyFn(d); const r = map.get(key) || { name: nameFn(d), requests: 0, success: 0, failure: 0, tokens: 0, cached: 0, reasoning: 0, cost: 0, latency: [], ttft: [] }; r.requests++; d.failed ? r.failure++ : r.success++; r.tokens += d.total_tokens; r.cached += d.cached_tokens; r.reasoning += d.reasoning_tokens; r.cost += d.cost; if (num(d.latency_ms) > 0) r.latency.push(num(d.latency_ms)); if (num(d.ttft_ms) > 0) r.ttft.push(num(d.ttft_ms)); map.set(key, r) });
  return [...map.values()].sort((a, b) => b.requests - a.requests);
}
function decodeManagementBody(body) {
  if (body == null) return '';
  if (Array.isArray(body)) return new TextDecoder().decode(Uint8Array.from(body));
  if (typeof body !== 'string') return JSON.stringify(body);
  try {
    const binary = atob(body);
    const bytes = Uint8Array.from(binary, (ch) => ch.charCodeAt(0));
    return new TextDecoder().decode(bytes);
  } catch {
    return body;
  }
}
function unwrapPluginPayloadWithMeta(payload) {
  const unwrapResponse = (value) => {
    if (value && typeof value === 'object' && typeof value.status_code === 'number' && Object.prototype.hasOwnProperty.call(value, 'body')) {
      const bodyText = decodeManagementBody(value.body);
      if (value.status_code >= 400) throw new Error(bodyText || (t('request_failed_colon') + value.status_code));
      let body = bodyText;
      if (bodyText) {
        try { body = JSON.parse(bodyText) } catch {}
      }
      return { data: body, statusCode: value.status_code, headers: value.headers || {} };
    }
    return { data: value, statusCode: 200, headers: {} };
  };
  if (!payload || typeof payload !== 'object' || !Object.prototype.hasOwnProperty.call(payload, 'ok')) return unwrapResponse(payload);
  if (!payload.ok) {
    const message = payload.error && payload.error.message ? payload.error.message : t('request_failed');
    throw new Error(message);
  }
  let result = payload.result;
  if (typeof result === 'string') {
    try { result = JSON.parse(result) } catch {}
  }
  return unwrapResponse(result);
}
function unwrapPluginPayload(payload) { return unwrapPluginPayloadWithMeta(payload).data }
async function fetchAllEventPages(fetchPage, baseParams, pageLimit) {
  const limit = Math.max(1, num(pageLimit) || 500);
  const params = new URLSearchParams(baseParams || '');
  const events = [];
  let offset = num(params.get('offset'));
  let total = null;
  for (;;) {
    params.set('limit', String(limit));
    params.set('offset', String(offset));
    const page = await fetchPage(new URLSearchParams(params));
    const rows = page && Array.isArray(page.events) ? page.events : [];
    events.push(...rows);
    const pageTotal = num(page && page.total);
    if (pageTotal > 0 || rows.length === 0) total = pageTotal;
    if (rows.length === 0 || (total !== null && events.length >= total) || rows.length < limit) break;
    offset += limit;
  }
  return { events, total: total === null ? events.length : total };
}

// ---- cache rate & cost-per-million helpers ----
function cacheRate(row) {
  var parts = Array.isArray(row.providers) && row.providers.length ? row.providers : [row];
  var cacheReads = 0;
  var inputWithCache = 0;
  parts.forEach(function(part) {
    // Aggregate cached_tokens follows snapshot v2 semantics: cache reads only.
    // Cache creation is reported separately and still belongs in the prompt
    // denominator for providers whose input_tokens excludes cache tokens.
    var cacheRead = Math.max(0, num(part.cached_tokens));
    var cacheWrite = Math.max(0, num(part.cache_write_tokens));
    var cacheTotal = cacheRead + cacheWrite;
    var input = Math.max(0, num(part.input_tokens));
    var output = Math.max(0, num(part.output_tokens));
    var total = Math.max(0, num(part.total_tokens));
    var provider = String(part.provider || '').trim();
    var exclusive = usesExclusiveCacheInput(provider, input, output, cacheTotal, total);
    cacheReads += cacheRead;
    inputWithCache += exclusive ? input + cacheTotal : input;
  });
  if (inputWithCache <= 0) return 0;
  return Math.min(100, cacheReads / inputWithCache * 100);
}
function costPerMillion(row, prices, manualPrices) {
  var cost = aggregateCost(row, prices, manualPrices);
  var tokens = num(row.total_tokens);
  if (!tokens || !Number.isFinite(cost)) return 0;
  return cost / tokens * 1e6;
}
function hourBucketValue(values, hour) {
  if (!values || typeof values !== 'object') return 0;
  var n = Number(hour);
  if (!Number.isFinite(n)) return 0;
  var padded = String(n).padStart(2, '0');
  var plain = String(n);
  if (Object.prototype.hasOwnProperty.call(values, padded)) return num(values[padded]);
  if (Object.prototype.hasOwnProperty.call(values, plain)) return num(values[plain]);
  return 0;
}

// Export for Node.js test environment
if (typeof module !== 'undefined' && module.exports) {
  module.exports = { esc, num, compact, pct, formatMs, formatDurationAndTTFT, formatUsd, formatMoney, providerUsesExclusiveCache, usesExclusiveCacheInput, totalTokens, uncachedInputTokens, priceForModel, cacheTokenTotal, cacheReadTokens, tokenCost, detailCost, aggregateCost, looksLikeKey, looksLikeCredentialId, isCredentialMarker, isCredentialLabel, trimCredentialSuffix, sourceLabel, sourceKey, friendlyApiName, clientApiLabel, clientApiGroupKey, avg, bucketSeries, hourFromTimestamp, dashboardCurrentHour, orderedRecentHours, healthColor, healthCellStyle, timestampMs, pluginEndpoint, managementEndpoint, decodeManagementStorage, parseManagementStorage, currentManagementKey, groupedRows, decodeManagementBody, unwrapPluginPayloadWithMeta, unwrapPluginPayload, fetchAllEventPages, cacheRate, costPerMillion, hourBucketValue };
}
