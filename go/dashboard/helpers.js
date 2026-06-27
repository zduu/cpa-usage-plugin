// Pure helper functions - usable in tests without DOM.
const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));
const num = (value) => Number.isFinite(Number(value)) ? Number(value) : 0;
function compact(value) { const n = num(value), abs = Math.abs(n); const trim = (v) => v.toFixed(1).replace(/\.0$/, ''); if (abs >= 1e6) return trim(n / 1e6) + 'M'; if (abs >= 1e3) return trim(n / 1e3) + 'k'; return fmt.format(n) }
const pct = (value) => Number.isFinite(value) ? value.toFixed(1) + '%' : '-';
const formatMs = (value) => Number.isFinite(value) && value > 0 ? (value >= 1000 ? (value / 1000).toFixed(2) + '秒' : Math.round(value) + '毫秒') : '-';
function totalTokens(detail) { const t = detail.tokens || {}; return num(t.total_tokens) || num(t.input_tokens) + num(t.output_tokens) + num(t.reasoning_tokens) }
function tokenCost(model, inputTokens, outputTokens, cachedTokens, reasoningTokens, prices) { const p = prices && prices[model]; if (!p) return 0; const cached = Math.max(num(cachedTokens), 0); const input = Math.max(num(inputTokens) - cached, 0); const output = Math.max(num(outputTokens), 0) + Math.max(num(reasoningTokens), 0); return input / 1e6 * num(p.prompt) + output / 1e6 * num(p.completion) + cached / 1e6 * num(p.cache) }
function detailCost(detail, prices) { const t = detail.tokens || {}; return tokenCost(detail.model, t.input_tokens, t.output_tokens, Math.max(num(t.cached_tokens), num(t.cache_tokens)), t.reasoning_tokens, prices) }
function aggregateCost(row, prices) { return tokenCost(row.model, row.input_tokens, row.output_tokens, row.cached_tokens, row.reasoning_tokens, prices) }
function looksLikeKey(v) { return typeof v === 'string' && (v.startsWith('sk-') || v.startsWith('AIza') || v.startsWith('hf_') || v.startsWith('pk_') || v.startsWith('rk_') || v.length >= 80) }
function looksLikeCredentialId(v) { const s = String(v || '').trim(); return /^[a-f0-9]{8,}$/i.test(s) || (s.length >= 32 && !/[ ./_-]/.test(s)) }
function isCredentialMarker(v) { return /^(api[-_ ]?key|apikey|key|credential|auth)$/i.test(String(v || '').trim()) }
function trimCredentialSuffix(value) {
  let s = String(value ?? '').trim(); if (!s) return '';
  const dot = s.split(' · ').map((p) => p.trim()).filter(Boolean);
  const marker = dot.findIndex(isCredentialMarker);
  if (marker > 0) return dot.slice(0, marker).join(' · ');
  if (dot.length > 1 && looksLikeCredentialId(dot[dot.length - 1])) return dot.slice(0, -1).join(' · ');
  const colon = s.split(':').map((p) => p.trim()).filter(Boolean);
  if (colon.length >= 3 && looksLikeCredentialId(colon[colon.length - 1])) return colon.slice(0, -1).join(':');
  return s;
}
function sourceLabel(detail) { const s = trimCredentialSuffix(detail.source); if (s && !looksLikeKey(s)) return s; const p = trimCredentialSuffix(detail.provider); if (p && !looksLikeKey(p)) return p; return '未知来源' }
function sourceKey(detail) { return sourceLabel(detail) }
function friendlyApiName(apiName) { const clean = trimCredentialSuffix(apiName); if (!clean) return '未知接口'; const parts = clean.split(' · ').filter(function (p) { return !looksLikeKey(p) && !isCredentialMarker(p) && !looksLikeCredentialId(p) }); return parts.length ? parts.join(' · ') : clean }
function clientApiLabel(detail) { const label = String((detail && detail.api_key) || '').trim(); return label || '未知 API' }
function clientApiGroupKey(detail) {
  const label = String((detail && detail.api_key) || '').trim();
  if (label) return 'api_key:' + label;
  const hash = String((detail && detail.api_key_hash) || '').trim();
  if (hash) return 'api_key_hash:' + hash;
  return '(unknown)';
}
function avg(values) { const xs = values.map(num).filter((v) => v > 0); return xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : 0 }
function bucketSeries(rows, metric, minutes, count) {
  const now = Date.now(); const step = minutes * 60e3; const start = now - step * count; const arr = new Array(count).fill(0);
  rows.forEach((d) => { const idx = Math.floor((d.timestamp_ms - start) / step); if (idx >= 0 && idx < count) arr[idx] += metric === 'tokens' ? d.total_tokens : metric === 'cost' ? d.cost : 1 });
  return arr;
}
function healthColor(rate) { if (rate < 0) return ''; const stops = [[239, 68, 68], [250, 204, 21], [34, 197, 94]]; const seg = rate < .5 ? 0 : 1; const t = seg === 0 ? rate * 2 : (rate - .5) * 2; const a = stops[seg], b = stops[seg + 1]; return 'rgb(' + a.map((v, i) => Math.round(v + (b[i] - v) * t)).join(',') + ')' }
function healthCellStyle(i, count, total, rate) { const rows = 7, cols = Math.ceil(count / rows), age = count - 1 - i, col = cols - Math.floor(age / rows), row = rows - (age % rows); return 'grid-column:' + col + ';grid-row:' + row + ';' + (total ? 'background:' + healthColor(rate) : '') }
function timestampMs(value) { const ms = Date.parse(value); return Number.isFinite(ms) ? ms : 0 }
function pluginEndpoint(path, pathname) {
  const clean = String(path || '').replace(/^\/+/, '');
  const current = String(pathname || (typeof location !== 'undefined' ? location.pathname : ''));
  const resourceMarker = '/resource/plugins/usage-statistics/';
  const resourceIdx = current.indexOf(resourceMarker);
  if (resourceIdx >= 0) return current.slice(0, resourceIdx + resourceMarker.length) + clean;
  const managementMarker = '/management/plugins/usage-statistics/';
  const managementIdx = current.indexOf(managementMarker);
  if (managementIdx >= 0) return current.slice(0, managementIdx + managementMarker.length) + clean;
  return './' + clean;
}
function managementEndpoint(path, pathname) {
  const clean = String(path || '').replace(/^\/+/, '');
  const current = String(pathname || (typeof location !== 'undefined' ? location.pathname : ''));
  const resourceMarker = '/resource/plugins/usage-statistics/';
  const resourceIdx = current.indexOf(resourceMarker);
  if (resourceIdx >= 0) return current.slice(0, resourceIdx) + '/management/plugins/usage-statistics/' + clean;
  const managementMarker = '/management/plugins/usage-statistics/';
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
function unwrapPluginPayload(payload) {
  const unwrapResponse = (value) => {
    if (value && typeof value === 'object' && typeof value.status_code === 'number' && Object.prototype.hasOwnProperty.call(value, 'body')) {
      const bodyText = decodeManagementBody(value.body);
      if (value.status_code >= 400) throw new Error(bodyText || ('请求失败：' + value.status_code));
      try { return JSON.parse(bodyText) } catch { return bodyText }
    }
    return value;
  };
  if (!payload || typeof payload !== 'object' || !Object.prototype.hasOwnProperty.call(payload, 'ok')) return unwrapResponse(payload);
  if (!payload.ok) {
    const message = payload.error && payload.error.message ? payload.error.message : '请求失败';
    throw new Error(message);
  }
  let result = payload.result;
  if (typeof result === 'string') {
    try { result = JSON.parse(result) } catch {}
  }
  return unwrapResponse(result);
}
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

// Export for Node.js test environment
if (typeof module !== 'undefined' && module.exports) {
  module.exports = { esc, num, compact, pct, formatMs, totalTokens, tokenCost, detailCost, aggregateCost, looksLikeKey, looksLikeCredentialId, isCredentialMarker, trimCredentialSuffix, sourceLabel, sourceKey, friendlyApiName, clientApiLabel, clientApiGroupKey, avg, bucketSeries, healthColor, healthCellStyle, timestampMs, pluginEndpoint, managementEndpoint, decodeManagementStorage, parseManagementStorage, currentManagementKey, groupedRows, decodeManagementBody, unwrapPluginPayload, fetchAllEventPages };
}
