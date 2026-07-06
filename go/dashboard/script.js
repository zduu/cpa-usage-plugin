// cpausage dashboard — main logic. Uses helpers from helpers.js.
const rangeKey = 'cpa-usage-range-v1';
var fmt = new Intl.NumberFormat(typeof getFormatLocale === 'function' ? getFormatLocale() : 'zh-CN');
var _lastFmtLocale = 'zh-CN';
let summaryData = null;         // DashboardSummary from /dashboard-summary
let eventsData = null;          // EventsResult from /dashboard-events
let modelPrices = {};
let manualModelPrices = {};
let selectedApi = '';
let clientApiSort = 'requests';
let trendMetric = 'cost'; // 'cost' | 'requests' | 'tokens' | 'rpm'
let pollTimer = null, pollFailures = 0;
let currentRange = '';
const eventsLimit = 500;
const apiDetailRecentLimit = 120;
const visiblePollDelayMs = 30000;
const hiddenPollDelayMs = 300000;
let apiDetailSeq = 0;
const apiDetailCache = new Map();
const conditionalPayloadCache = new Map();
let apiDetailLastRender = null;
let updatedState = { type: 'loading', generatedAt: null, message: '' };

function manualPricesFromResponse(data) {
  return data && Object.prototype.hasOwnProperty.call(data, 'manual_prices') ? (data.manual_prices || {}) : modelPrices;
}

// Dom helpers
const $ = (id) => document.getElementById(id);
const setText = (id, value) => { $(id).textContent = value };
function currentLocale() { return typeof getFormatLocale === 'function' ? getFormatLocale() : 'zh-CN'; }
function localizedColon() { return String(typeof I18N_LANG === 'string' ? I18N_LANG : '').startsWith('zh') ? '：' : ': '; }
function withLabel(key, value) { return t(key) + localizedColon() + value; }
function formatInteger(value) { return fmt.format(num(value)); }
function formatDateTime(value) { return new Date(value).toLocaleString(currentLocale()); }
function formatTime(value) { return new Date(value).toLocaleTimeString(currentLocale()); }
function statusText(failed) { return failed ? t('failure_label') : t('success_label'); }
function renderUpdated() {
  const el = $('updated');
  if (!el) return;
  if (updatedState.type === 'success') {
    setText('updated', withLabel('updated_at', formatTime(updatedState.generatedAt || Date.now())));
    return;
  }
  if (updatedState.type === 'compat') {
    setText('updated', withLabel('updated_at', formatTime(updatedState.generatedAt || Date.now())) + ' (' + t('compat_mode') + ')');
    return;
  }
  if (updatedState.type === 'error') {
    setText('updated', updatedState.message || t('load_usage_failed'));
    return;
  }
  setText('updated', t('loading'));
}

// ---- 主题检测：跟随 CPA 日间/夜间模式 ----
// CPA 管理面板在 iframe 中加载此看板，父窗口通过 data-theme 属性控制主题：
//   data-theme="dark"  → 暗色模式
//   data-theme="white" → 浅色模式（CPA 使用 "white"，不是 "light"）
//   无属性             → 自动（跟随 OS 偏好）
// 同时也通过 localStorage key cli-proxy-theme 持久化。
(function() {
  try {
    var CPA_THEME_STORAGE_KEY = 'cli-proxy-theme';

    function getParentDocument() {
      try {
        if (window.parent && window.parent !== window && window.parent.document) {
          return window.parent.document;
        }
      } catch (e) { /* 跨域不可访问 */ }
      return null;
    }

    // 将 CPA 主题值映射为 dark/light
    // CPA 面板使用 "white" 表示浅色模式
    function cpaThemeToMode(value) {
      if (value === 'dark') return 'dark';
      if (value === 'white') return 'light';
      return null; // auto 或其他 → 回退到 OS 偏好
    }

    // 从父窗口 html data-theme 属性检测
    function detectFromParentDocument() {
      var parentDoc = getParentDocument();
      if (!parentDoc || !parentDoc.documentElement) return null;
      var theme = parentDoc.documentElement.getAttribute('data-theme');
      return cpaThemeToMode(theme);
    }

    // 从共享 localStorage 检测（与父窗口同源时可用）
    function detectFromLocalStorage() {
      try {
        var stored = localStorage.getItem(CPA_THEME_STORAGE_KEY);
        return cpaThemeToMode(stored);
      } catch (e) { return null; }
    }

    // 回退：OS 偏好
    function detectFromOS() {
      if (typeof window.matchMedia === 'function') {
        return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
      }
      return 'light';
    }

    function detectCPATheme() {
      // 策略 1（最优先）：父窗口 data-theme 属性
      var mode = detectFromParentDocument();
      if (mode) return mode;
      // 策略 2：共享 localStorage
      mode = detectFromLocalStorage();
      if (mode) return mode;
      // 策略 3：回退 OS 偏好
      return detectFromOS();
    }

    function applyTheme(theme) {
      if (document.documentElement && document.documentElement.setAttribute) {
        document.documentElement.setAttribute('data-cpa-theme', theme);
      }
    }

    function syncTheme() {
      applyTheme(detectCPATheme());
    }

    // 监听父窗口 data-theme 属性变化（MutationObserver on parent）
    if (typeof MutationObserver !== 'undefined') {
      var parentDoc = getParentDocument();
      if (parentDoc && parentDoc.documentElement) {
        var parentObserver = new MutationObserver(function() {
          var mode = detectFromParentDocument();
          if (mode) applyTheme(mode);
        });
        parentObserver.observe(parentDoc.documentElement, {
          attributes: true,
          attributeFilter: ['data-theme']
        });
      }
    }

    // 监听同源 localStorage 变化（父窗口切换主题时触发 storage 事件）
    if (typeof window.addEventListener === 'function') {
      window.addEventListener('storage', function(e) {
        if (e.key === CPA_THEME_STORAGE_KEY) {
          var mode = cpaThemeToMode(e.newValue);
          // auto → 回退到 OS 或父窗口
          if (!mode) {
            mode = detectFromParentDocument() || detectFromOS();
          }
          applyTheme(mode);
        }
      });
    }

    // 监听 OS 偏好变化（仅在 CPA 为 auto 模式时生效）
    if (typeof window.matchMedia === 'function') {
      var osDarkQuery = window.matchMedia('(prefers-color-scheme: dark)');
      var onOSChange = function(e) {
        // 仅在 CPA 主题为 auto 时跟随 OS
        var stored = detectFromLocalStorage();
        var parentMode = detectFromParentDocument();
        if (stored === null && parentMode === null) {
          applyTheme(e.matches ? 'dark' : 'light');
        }
      };
      if (osDarkQuery && osDarkQuery.addEventListener) {
        osDarkQuery.addEventListener('change', onOSChange);
      }
    }

    // 首次同步
    syncTheme();
  } catch (e) { /* 主题检测失败不影响页面功能 */ }
})();

function cloneHeaders(headers) {
  if (!headers) return {};
  if (Array.isArray(headers)) return Object.fromEntries(headers);
  if (typeof headers.forEach === 'function') {
    const cloned = {};
    headers.forEach((value, key) => { cloned[key] = value });
    return cloned;
  }
  return Object.assign({}, headers);
}

function headerValue(headers, name) {
  if (!headers) return '';
  if (typeof headers.get === 'function') return headers.get(name) || headers.get(String(name).toLowerCase()) || '';
  const target = String(name).toLowerCase();
  for (const [key, value] of Object.entries(headers)) {
    if (String(key).toLowerCase() !== target) continue;
    return Array.isArray(value) ? String(value[0] || '') : String(value || '');
  }
  return '';
}

async function fetchJsonPayloadWithMeta(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  const responseHeaders = {};
  const responseEtag = headerValue(response.headers, 'ETag');
  if (responseEtag) responseHeaders.ETag = [responseEtag];
  if (response.status === 304) {
    return { data: '', statusCode: 304, headers: responseHeaders };
  }
  let payload = null;
  if (text) {
    try { payload = JSON.parse(text) } catch {
      if (!response.ok) throw new Error(text);
      throw new Error(t('response_not_json'));
    }
  }
  if (!response.ok) {
    const message = payload && payload.error && payload.error.message ? payload.error.message : (text || (t('request_failed_colon') + response.status));
    throw new Error(message);
  }
  const meta = unwrapPluginPayloadWithMeta(payload);
  meta.headers = Object.assign({}, meta.headers || {});
  if (responseEtag && !headerValue(meta.headers, 'ETag')) meta.headers.ETag = responseHeaders.ETag;
  if (!meta.statusCode) meta.statusCode = response.status || 200;
  return meta;
}

async function fetchJsonPayload(url, options) {
  const meta = await fetchJsonPayloadWithMeta(url, options);
  return meta.data;
}

async function fetchTextPayload(url, options) {
  const meta = await fetchTextPayloadWithMeta(url, options);
  return meta.data;
}

async function fetchTextPayloadWithMeta(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) throw new Error(text || (t('request_failed_colon') + response.status));
  if (!text) return { data: '', statusCode: response.status || 200, headers: {} };
  let payload = null;
  try { payload = JSON.parse(text) } catch { return { data: text, statusCode: response.status || 200, headers: {} } }
  const meta = unwrapPluginPayloadWithMeta(payload);
  if (meta.data == null) meta.data = '';
  meta.data = typeof meta.data === 'string' ? meta.data : JSON.stringify(meta.data);
  return meta;
}

async function fetchConditionalJsonPayload(cacheKey, url, options) {
  const cached = conditionalPayloadCache.get(cacheKey);
  const merged = Object.assign({}, options || {});
  const headers = cloneHeaders(merged.headers);
  if (cached && cached.etag && !headerValue(headers, 'If-None-Match')) headers['If-None-Match'] = cached.etag;
  merged.headers = headers;
  let meta = await fetchJsonPayloadWithMeta(url, merged);
  if (meta.statusCode === 304) {
    if (cached && Object.prototype.hasOwnProperty.call(cached, 'data')) return cached.data;
    const retryOptions = Object.assign({}, options || {});
    const retryHeaders = cloneHeaders(retryOptions.headers);
    delete retryHeaders['If-None-Match'];
    delete retryHeaders['if-none-match'];
    retryOptions.headers = retryHeaders;
    meta = await fetchJsonPayloadWithMeta(String(url) + (String(url).includes('?') ? '&' : '?') + '_ts=' + Date.now(), retryOptions);
    if (meta.statusCode === 304) throw new Error(t('no_304_cache'));
  }
  const etag = headerValue(meta.headers, 'ETag');
  if (etag) conditionalPayloadCache.set(cacheKey, { etag, data: meta.data });
  else conditionalPayloadCache.delete(cacheKey);
  return meta.data;
}

function requireObjectPayload(data, label) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) {
    throw new Error(label + ' ' + t('empty_response'));
  }
  return data;
}

function managementFetchOptions(options) {
  const merged = Object.assign({}, options || {});
  const headers = Object.assign({}, merged.headers || {});
  const key = currentManagementKey();
  if (key) {
    headers.Authorization = headers.Authorization || ('Bearer ' + key);
    headers['x-management-key'] = headers['x-management-key'] || key;
  }
  merged.headers = headers;
  return merged;
}

function fetchManagementJsonPayload(path, options) {
  return fetchJsonPayload(managementEndpoint(path), managementFetchOptions(options));
}

function pluginFetchOptions(options) {
  return managementFetchOptions(options);
}

async function loadModelPrices() {
  const data = await fetchJsonPayload(pluginEndpoint('model-prices'), pluginFetchOptions({ cache: 'no-store' }));
  modelPrices = (data && data.prices) || {};
  manualModelPrices = manualPricesFromResponse(data);
  return modelPrices;
}

async function saveModelPrice(model, price) {
  const data = await fetchManagementJsonPayload('model-prices', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, price })
  });
  modelPrices = (data && data.prices) || {};
  manualModelPrices = manualPricesFromResponse(data);
  return modelPrices;
}

async function deleteModelPrice(model) {
  const params = new URLSearchParams();
  params.set('model', model);
  const data = await fetchManagementJsonPayload('model-prices?' + params.toString(), { method: 'DELETE' });
  modelPrices = (data && data.prices) || {};
  manualModelPrices = manualPricesFromResponse(data);
  return modelPrices;
}

function drawSpark(id, values, color) {
  const svg = $(id); const w = svg.clientWidth || 320, h = 54; const max = Math.max(...values, 1); const points = values.map((v, i) => [i * (w / (Math.max(values.length - 1, 1))), h - 8 - (v / max) * (h - 16)]);
  const d = points.map((p, i) => (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' ');
  svg.setAttribute('viewBox', '0 0 ' + w + ' ' + h);
  svg.innerHTML = '<path d="' + d + '" fill="none" stroke="' + color + '" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/>';
}

function renderStats() {
  if (!summaryData) return;
  const u = summaryData.usage;
  setText('totalRequests', fmt.format(u.total_requests));
  setText('successText', withLabel('success_requests', formatInteger(u.success_count)));
  setText('failureText', withLabel('failure_requests', formatInteger(u.failure_count)));
  setText('avgLatency', withLabel('avg_latency', formatMs(u.avg_latency_ms)));
  setText('totalTokens', compact(u.total_tokens));
  setText('cachedText', withLabel('cached_tokens', compact(u.cached_tokens)));
  setText('reasoningText', withLabel('reasoning_tokens', compact(u.reasoning_tokens)));
  // RPM: compute from hourly time series
  const hourValues = Object.values(u.requests_by_hour || {}).map(num);
  const recentHours = hourValues.slice(-1);
  const recentReq = recentHours.length ? recentHours[0] : 0;
  setText('rpm', (recentReq / 60).toFixed(2));
  setText('rpmMeta', withLabel('recent_requests_label', formatInteger(recentReq)));
  const cost = (summaryData.model_stats || []).reduce((s, m) => s + aggregateCost(m, modelPrices, manualModelPrices), 0);
  setText('totalCost', formatUsd(cost));
  setText('costMeta', withLabel('total_tokens_label', compact(u.total_tokens)));
  // Sparklines from hourly data
  const reqByHour = Array.from({ length: 24 }, (_, i) => {
    const k = String(i).padStart(2, '0');
    return num(u.requests_by_hour && u.requests_by_hour[k]) || 0;
  });
  const tokByHour = Array.from({ length: 24 }, (_, i) => {
    const k = String(i).padStart(2, '0');
    return num(u.tokens_by_hour && u.tokens_by_hour[k]) || 0;
  });
  drawSpark('requestSpark', reqByHour, '#8b8680');
  drawSpark('tokenSpark', tokByHour, '#8b5cf6');
  drawSpark('rpmSpark', reqByHour.length ? reqByHour.map(v => v / 60) : [0], '#22c55e');
  drawSpark('costSpark', reqByHour.length ? reqByHour.map(v => (cost > 0 ? v / Math.max(u.total_requests || 1, 1) * cost : 0)) : [0], '#f59e0b');
}

function storageBatchTitle(storage) {
  const records = num(storage && storage.last_write_batch_records);
  if (records <= 0) return '';
  const parts = [t('storage_batch_title', formatInteger(records))];
  const duration = num(storage.last_write_batch_duration_ms);
  if (duration > 0) parts.push(t('storage_batch_duration') + ' ' + formatMs(duration));
  const avgDuration = num(storage.write_batch_avg_duration_ms);
  if (avgDuration > 0) parts.push(t('storage_batch_avg') + ' ' + formatMs(avgDuration));
  const p95Duration = num(storage.write_batch_p95_duration_ms);
  const p99Duration = num(storage.write_batch_p99_duration_ms);
  if (p95Duration > 0) parts.push(t('storage_batch_p95') + ' ' + formatMs(p95Duration) + (p99Duration > 0 ? ' / p99 ' + formatMs(p99Duration) : ''));
  const wait = num(storage.last_write_queue_wait_ms);
  if (wait > 0) parts.push(t('storage_batch_wait') + ' ' + formatMs(wait));
  const avgWait = num(storage.write_queue_wait_avg_ms);
  if (avgWait > 0) parts.push(t('storage_batch_avg_wait') + ' ' + formatMs(avgWait));
  const p95Wait = num(storage.write_queue_wait_p95_ms);
  const p99Wait = num(storage.write_queue_wait_p99_ms);
  if (p95Wait > 0) parts.push(t('storage_batch_wait_p95') + ' ' + formatMs(p95Wait) + (p99Wait > 0 ? ' / p99 ' + formatMs(p99Wait) : ''));
  return parts.join(typeof I18N_LANG === 'string' && I18N_LANG.startsWith('zh') ? '，' : ', ');
}

function storagePressureLabel(value) {
  switch (String(value || '')) {
    case 'full': return t('write_queue_full');
    case 'backlog': return t('write_queue_backlog');
    case 'queued': return t('write_queued');
    case 'slow': return t('write_slow');
    case 'normal': return t('write_normal');
    default: return '';
  }
}

function storagePressureTitle(storage) {
  const label = storagePressureLabel(storage && storage.write_pressure);
  return label ? withLabel('write_pressure', label) : '';
}

function storageTitle() {
  return Array.from(arguments).filter(Boolean).join(' | ');
}

function renderStorageStatus() {
  const el = $('storageStatus');
  if (!el) return;
  const storage = summaryData && summaryData._meta && summaryData._meta.storage;
  el.className = 'storageStatus';
  el.title = '';
  if (!storage) {
    el.textContent = '';
    return;
  }
  if (!storage.enabled) {
    el.textContent = t('storage_disabled');
    el.classList.add('warn');
    el.title = storage.path || '';
    return;
  }
  if (storage.last_error) {
    el.textContent = t('storage_error');
    el.classList.add('bad');
    el.title = storage.last_error;
    return;
  }
  const titleParts = [storage.last_snapshot_at || storage.loaded_path || storage.path || ''];
  const queued = num(storage.write_queue_length);
  if (queued > 0) {
    const capacity = num(storage.write_queue_capacity);
    titleParts.push(formatInteger(queued) + (capacity > 0 ? t('storage_pending_queue', formatInteger(capacity)) : t('storage_pending_queue_no_capacity')));
  }
  const pending = num(storage.pending_buffered_records);
  if (pending > 0) {
    titleParts.push(formatInteger(pending) + t('storage_pending_flush'));
  }
  const pendingSync = num(storage.pending_unsynced_records);
  if (pendingSync > 0) {
    titleParts.push(formatInteger(pendingSync) + t('storage_pending_sync'));
  }
  const pendingSnapshot = num(storage.pending_snapshot_records);
  if (pendingSnapshot > 0) {
    titleParts.push(formatInteger(pendingSnapshot) + t('storage_pending_snapshot'));
  }
  el.textContent = t('storage_enabled');
  el.classList.add('ok');
  el.title = storageTitle(...titleParts, storagePressureTitle(storage), storageBatchTitle(storage));
}

function renderHealth() {
  if (!summaryData) return;
  const grid = normalizeHealthGrid(summaryData.health_grid, summaryData.generated_at);
  const count = 672, rows = 7, cols = Math.ceil(count / rows);
  let totalS = 0, totalF = 0;
  const cells = [], tooltips = [];
  grid.forEach((slot, i) => {
    totalS += slot.success; totalF += slot.failure;
    const total = slot.total;
    const rate = total ? slot.success / total : -1;
    const timeRange = formatDateTime(slot.start) + ' - ' + formatDateTime(slot.end);
    const tip = '<span>' + timeRange + '</span><br>' + (total ? '<span class="ok">' + t('success_label') + ' ' + formatInteger(slot.success) + '</span> <span class="bad">' + t('failure_label') + ' ' + formatInteger(slot.failure) + '</span> <span>' + t('success_rate') + ' ' + pct(rate * 100) + '</span>' : '<span>' + t('no_requests') + '</span>');
    tooltips.push(tip);
    cells.push('<div class="healthCell ' + (total ? 'active' : '') + '" data-health-idx="' + i + '" style="' + healthCellStyle(i, count, total, rate) + '"></div>');
  });
  $('healthGrid').innerHTML = cells.join('');
  const tip = $('tooltip');
  const showTip = function (cell) {
    if (!cell) return;
    const idx = parseInt(cell.dataset.healthIdx); if (isNaN(idx) || idx < 0 || idx >= count) { tip.classList.add('hidden'); return }
    tip.innerHTML = tooltips[idx]; tip.classList.remove('hidden');
    const r = cell.getBoundingClientRect(); let left = r.right + 8, top = r.top - 6;
    if (left + 260 > window.innerWidth) left = r.left - 268; if (top + 64 > window.innerHeight) top = window.innerHeight - 74; if (top < 6) top = 6;
    tip.style.left = left + 'px'; tip.style.top = top + 'px';
  };
  $('healthGrid').onmouseover = function (e) {
    const cell = e.target.closest('.healthCell');
    if (!cell) { tip.classList.add('hidden'); return }
    showTip(cell);
  };
  $('healthGrid').onmouseleave = function (e) {
    if (!e.relatedTarget || !e.relatedTarget.closest('.healthCell')) tip.classList.add('hidden');
  };
  $('healthGrid').onmouseout = function (e) { const t = e.relatedTarget; if (!t || !t.closest('.healthCell')) tip.classList.add('hidden') };
  const total = totalS + totalF; setText('healthRate', total ? pct(totalS / total * 100) : '-'); setText('healthSuccess', t('success_label') + ' ' + formatInteger(totalS)); setText('healthFailure', t('failure_label') + ' ' + formatInteger(totalF));
}

const healthGridCount = 672;
const healthGridStepMs = 15 * 60 * 1000;

function healthGridWindowEnd(value) {
  const ms = timestampMs(value) || Date.now();
  return Math.floor(ms / healthGridStepMs) * healthGridStepMs + healthGridStepMs;
}

function emptyHealthGrid(value) {
  const end = healthGridWindowEnd(value);
  const start = end - healthGridCount * healthGridStepMs;
  return Array.from({ length: healthGridCount }, (_, i) => {
    const slotStart = start + i * healthGridStepMs;
    return { slot: i, total: 0, success: 0, failure: 0, start: new Date(slotStart).toISOString(), end: new Date(slotStart + healthGridStepMs).toISOString() };
  });
}

function normalizeHealthGrid(grid, generatedAt) {
  const normalized = emptyHealthGrid(generatedAt);
  if (!Array.isArray(grid)) return normalized;
  grid.slice(0, healthGridCount).forEach((slot, i) => {
    if (!slot || typeof slot !== 'object') return;
    const success = num(slot.success);
    const failure = num(slot.failure);
    normalized[i] = Object.assign({}, normalized[i], slot, {
      slot: i,
      success,
      failure,
      total: num(slot.total) || success + failure,
    });
  });
  return normalized;
}

function modelNames() {
  if (summaryData && summaryData.model_stats) return summaryData.model_stats.map(m => m.model).filter(Boolean).sort((a, b) => a.localeCompare(b));
  return [];
}

function priceModelOptions() {
  return [...new Set([...modelNames(), ...Object.keys(modelPrices || {}), ...Object.keys(manualModelPrices || {})])].filter(Boolean).sort((a, b) => a.localeCompare(b));
}

function fillPriceForm(model) {
  $('priceModel').value = model || '';
  const p = priceForModel($('priceModel').value, modelPrices, '', manualModelPrices) || {};
  $('pricePrompt').value = p.prompt ?? '';
  $('priceCompletion').value = p.completion ?? '';
  $('priceCache').value = p.cache ?? '';
}

function syncPriceFormForModel(model) {
  if (!model) {
    fillPriceForm('');
    return;
  }
  if (priceForModel(model, modelPrices, '', manualModelPrices)) fillPriceForm(model);
}

function renderPrices() {
  const selected = $('priceModel').value;
  $('priceModelOptions').innerHTML = priceModelOptions().map((m) => '<option value="' + esc(m) + '"></option>').join('');
  $('priceModel').value = selected;
  const entries = Object.entries(manualModelPrices).sort(([a], [b]) => a.localeCompare(b));
  $('priceList').innerHTML = entries.length ? entries.map(([m, p]) => '<div class="priceItem"><div><strong>' + esc(m) + '</strong><div class="priceMeta"><span>' + t('input_price') + ' ' + num(p.prompt).toFixed(4) + '</span><span>' + t('output_price') + ' ' + num(p.completion).toFixed(4) + '</span><span>' + t('cache_price') + ' ' + num(p.cache).toFixed(4) + '</span></div></div><div class="priceActions"><button class="btn" data-edit-price="' + esc(m) + '">' + t('edit') + '</button><button class="btn danger" data-del-price="' + esc(m) + '">' + t('delete') + '</button></div></div>').join('') : '<div class="empty">' + t('no_prices') + '</div>';
  document.querySelectorAll('[data-edit-price]').forEach((btn) => btn.onclick = () => fillPriceForm(btn.dataset.editPrice));
  document.querySelectorAll('[data-del-price]').forEach((btn) => btn.onclick = async () => {
    try {
      await deleteModelPrice(btn.dataset.delPrice);
      if ($('priceModel').value === btn.dataset.delPrice) fillPriceForm('');
      await rerender({ refreshEvents: false, refreshApiDetail: true });
    } catch (e) {
      alert(t('price_delete_failed') + (e && e.message ? e.message : t('unknown_error')));
    }
  });
}

function renderClientApiStats() {
  const stats = summaryData && summaryData.client_api_stats;
  if (!stats || !stats.length) { $('clientApiStats').innerHTML = '<div class="empty">' + t('no_api_data') + '</div>'; return }
  let rows = stats.map((r) => ({
    name: r.api_key || t('unknown_api'),
    requests: r.total_requests,
    success: r.success_count,
    failure: r.failure_count,
    tokens: r.total_tokens,
    cost: (r.models || []).reduce((s, m) => s + aggregateCost(m, modelPrices, manualModelPrices), 0)
  }));
  if (clientApiSort === 'tokens') rows.sort((a, b) => b.tokens - a.tokens);
  else if (clientApiSort === 'cost') rows.sort((a, b) => b.cost - a.cost);
  else rows.sort((a, b) => b.requests - a.requests);
  document.querySelectorAll('[data-api-sort]').forEach((btn) => btn.classList.toggle('active', btn.dataset.apiSort === clientApiSort));
  $('clientApiStats').innerHTML = rows.length ? '<div class="apiCardGrid">' + rows.map((r) => '<div class="apiCard"><div><div class="apiName">' + esc(r.name) + '</div><div class="apiChips"><span class="chip">' + withLabel('sort_requests', formatInteger(r.requests)) + ' (<span class="ok">' + formatInteger(r.success) + '</span>&nbsp;<span class="bad">' + formatInteger(r.failure) + '</span>)</span><span class="chip">' + withLabel('sort_tokens', compact(r.tokens)) + '</span><span class="chip">' + withLabel('sort_cost', formatUsd(r.cost)) + '</span></div></div></div>').join('') + '</div>' : '<div class="empty">' + t('no_api_data') + '</div>';
}

function renderApiStats() {
  const usage = summaryData && summaryData.usage;
  if (!usage || !usage.apis) { $('apiStats').innerHTML = '<div class="empty">' + t('no_upstream_data') + '</div>'; $('apiSelect').innerHTML = '<option value="">' + t('upstream_select_none') + '</option>'; return }
  const rows = Object.entries(usage.apis).map(([api, a]) => ({
    api,
    requests: a.total_requests,
    success: a.success_count,
    failure: a.failure_count,
    tokens: a.total_tokens,
    avgLatency: a.avg_latency_ms,
    successRate: a.total_requests ? a.success_count / a.total_requests * 100 : 100,
    modelCount: Object.keys(a.models || {}).length
  })).sort((a, b) => b.requests - a.requests);
  if (rows.length && (!selectedApi || !rows.some((r) => r.api === selectedApi))) selectedApi = rows[0].api;
  if (!rows.length) selectedApi = '';
  $('apiSelect').innerHTML = rows.length ? rows.map((r) => '<option value="' + esc(r.api) + '">' + esc(friendlyApiName(r.api)) + '</option>').join('') : '<option value="">' + t('upstream_select_none') + '</option>';
  $('apiSelect').value = selectedApi;
  $('apiSelect').disabled = !rows.length;
  $('apiSelect').onchange = () => { selectedApi = $('apiSelect').value; renderApiStats(); renderApiDetail() };
  $('apiStats').innerHTML = rows.length ? '<table><thead><tr><th>' + t('col_api') + '</th><th>' + t('col_requests') + '</th><th>' + t('col_success_rate') + '</th><th>' + t('col_tokens') + '</th><th>' + t('col_avg_latency') + '</th><th>' + t('col_models') + '</th></tr></thead><tbody>' + rows.map((r) => '<tr class="clickableRow ' + (r.api === selectedApi ? 'selectedRow' : '') + '" data-api="' + esc(r.api) + '"><td class="nameCell">' + esc(friendlyApiName(r.api)) + '</td><td>' + formatInteger(r.requests) + ' <span class="ok">(' + formatInteger(r.success) + '</span> <span class="bad">' + formatInteger(r.failure) + ')</span></td><td class="' + (r.successRate >= 95 ? 'ok' : r.successRate >= 80 ? 'neutral' : 'bad') + '">' + pct(r.successRate) + '</td><td>' + compact(r.tokens) + '</td><td>' + formatMs(r.avgLatency) + '</td><td>' + formatInteger(r.modelCount) + ' ' + t('model_count') + '</td></tr>').join('') + '</tbody></table>' : '<div class="empty">' + t('no_upstream_data') + '</div>';
  document.querySelectorAll('[data-api]').forEach((row) => row.onclick = () => { selectedApi = row.getAttribute('data-api') || ''; renderApiStats(); renderApiDetail() });
}

function metricHtml(label, value, extra) {
  return '<div class="metric"><div class="metricLabel">' + esc(label) + '</div><div class="metricValue">' + value + '</div>' + (extra ? '<div class="subtle metricMeta">' + extra + '</div>' : '') + '</div>';
}

function barsHtml(title, rows, total, emptyText) {
  if (!rows.length) return '<div><div class="subtle" style="margin-bottom:8px">' + esc(title) + '</div><div class="empty">' + esc(emptyText) + '</div></div>';
  return '<div><div class="subtle" style="margin-bottom:8px">' + esc(title) + '</div><div class="barList">' + rows.slice(0, 8).map((r) => {
    const width = total ? Math.max(4, Math.round(r.requests / total * 100)) : 0;
    return '<div class="barItem"><div class="nameCell">' + esc(r.name) + '</div><div class="barTrack"><div class="barFill" style="width:' + width + '%"></div></div><div>' + formatInteger(r.requests) + ' ' + t('col_requests') + '</div></div>';
  }).join('') + '</div></div>';
}

function normalizeApiDetailEvent(d) {
  const tokens = d.tokens || {};
  return Object.assign({}, d, {
    timestamp_ms: timestampMs(d.timestamp),
    total_tokens: totalTokens(d),
    cached_tokens: Math.max(num(tokens.cached_tokens), num(tokens.cache_tokens)),
    reasoning_tokens: num(tokens.reasoning_tokens),
    cost: detailCost(d, modelPrices, manualModelPrices)
  });
}

async function fetchApiDetailData(api) {
  const params = new URLSearchParams();
  params.set('range', $('range').value);
  params.set('api', api);
  params.set('recent_limit', String(apiDetailRecentLimit));
  const url = pluginEndpoint('dashboard-api-detail') + '?' + params.toString();
  const data = requireObjectPayload(await fetchConditionalJsonPayload('dashboard-api-detail:' + url, url, pluginFetchOptions({ cache: 'no-store' })), 'dashboard-api-detail');
  data.recent_events = (data.recent_events || []).map(normalizeApiDetailEvent);
  return data;
}

function apiDetailCacheKey(api) {
  return api + '|' + $('range').value;
}

function apiDetailErrorHtml(errorRows, loading, error, knownFailureCount) {
  if (loading && !errorRows.length) return '<div><div class="subtle" style="margin-bottom:8px">' + t('error_stats') + '</div><div class="empty">' + t('loading_api_detail') + '</div></div>';
  if (error && !errorRows.length && num(knownFailureCount) === 0) return '<div><div class="subtle" style="margin-bottom:8px">' + t('error_stats') + '</div><div class="empty">' + t('no_failures') + '</div></div>';
  if (error && !errorRows.length) return '<div><div class="subtle" style="margin-bottom:8px">' + t('error_stats') + '</div><div class="empty">' + t('detail_load_failed_msg') + esc(error.message || t('unknown_error')) + '</div></div>';
  return '<div><div class="subtle" style="margin-bottom:8px">' + t('error_stats') + '</div>' +
    (errorRows.length ? '<div class="tableWrap"><table><thead><tr><th>' + t('col_status') + '</th><th>' + t('col_requests') + '</th><th>' + t('error_stats') + '</th></tr></thead><tbody>' + errorRows.slice(0, 10).map((r) => '<tr><td class="bad">' + esc(r.status_code || '-') + '</td><td>' + formatInteger(r.count) + '</td><td><span class="errorText">' + esc(r.failure || t('no_body_returned')) + '</span></td></tr>').join('') + '</tbody></table></div>' : '<div class="empty">' + t('no_failures') + '</div>') +
    '</div>';
}

function apiDetailRecentHtml(rows, loading, error) {
  if (loading && !rows.length) return '<div><div class="subtle" style="margin-bottom:8px">' + t('recent_requests') + '</div><div class="empty">' + t('loading_api_detail') + '</div></div>';
  if (error && !rows.length) return '<div><div class="subtle" style="margin-bottom:8px">' + t('recent_requests') + '</div><div class="empty">' + t('detail_load_failed') + '</div></div>';
  return '<div><div class="subtle" style="margin-bottom:8px">' + t('recent_requests') + '</div>' +
    (rows.length ? '<div class="tableWrap"><table><thead><tr><th>' + t('col_time') + '</th><th>' + t('col_model') + '</th><th>' + t('col_result') + '</th><th>' + t('col_latency') + '</th><th>' + t('col_tokens') + '</th><th>' + t('col_source') + '</th></tr></thead><tbody>' + rows.slice(0, apiDetailRecentLimit).map((d) => '<tr><td>' + formatDateTime(d.timestamp_ms) + '</td><td class="nameCell">' + esc(d.model) + '</td><td class="' + (d.failed ? 'bad' : 'ok') + '">' + statusText(d.failed) + '</td><td>' + formatMs(num(d.latency_ms)) + '</td><td>' + formatInteger(d.total_tokens) + '</td><td class="nameCell">' + esc(sourceLabel(d)) + '</td></tr>').join('') + '</tbody></table></div>' : '<div class="empty">' + t('no_detail') + '</div>') +
    '</div>';
}

function renderApiDetailContent(apiData, detailState) {
  apiDetailLastRender = { api: selectedApi, apiData, detailState };
  const detail = detailState && detailState.detail;
  const rows = (detail && detail.recent_events) || [];
  const loading = detailState && detailState.loading;
  const error = detailState && detailState.error;
  const summary = (detail && detail.summary) || apiData;
  const requests = num(summary.total_requests), success = num(summary.success_count), failure = num(summary.failure_count);
  const knownFailureCount = num(apiData && apiData.failure_count);
  const rate = requests ? success / requests * 100 : 100;
  const models = detail ? (detail.model_stats || []).map((m) => ({ name: m.model || 'unknown', requests: num(m.total_requests), success: num(m.success_count), failure: num(m.failure_count), tokens: num(m.total_tokens), input_tokens: num(m.input_tokens), output_tokens: num(m.output_tokens), cached_tokens: num(m.cached_tokens), reasoning_tokens: num(m.reasoning_tokens), providers: m.providers || [], avgLatency: num(m.avg_latency_ms) })) : Object.entries(apiData.models || {}).map(([name, m]) => ({ name, requests: num(m.total_requests), success: num(m.success_count), failure: num(m.failure_count), tokens: num(m.total_tokens), input_tokens: num(m.input_tokens), output_tokens: num(m.output_tokens), cached_tokens: num(m.cached_tokens), reasoning_tokens: num(m.reasoning_tokens), providers: m.providers || [], avgLatency: num(m.avg_latency_ms) }));
  models.sort((a, b) => b.requests - a.requests);
  const sources = detail ? (detail.source_stats || []).map((s) => ({ name: s.source || t('unknown_source'), requests: num(s.total_requests), success: num(s.success_count), failure: num(s.failure_count), tokens: num(s.total_tokens) })) : [];
  const errorRows = (detail && detail.error_stats) || [];
  const totalCost = models.reduce((s, m) => s + aggregateCost({ model: m.name, input_tokens: m.input_tokens, output_tokens: m.output_tokens, cached_tokens: m.cached_tokens, reasoning_tokens: m.reasoning_tokens, providers: m.providers }, modelPrices, manualModelPrices), 0);
  $('apiDetail').innerHTML = '<div class="detailGrid">' +
    metricHtml(t('requests_label'), formatInteger(requests), '<span class="ok">' + t('success_label') + ' ' + formatInteger(success) + '</span>&nbsp;<span class="bad">' + t('failure_label') + ' ' + formatInteger(failure) + '</span>') +
    metricHtml(t('success_rate'), '<span class="' + (rate >= 95 ? 'ok' : rate >= 80 ? 'neutral' : 'bad') + '">' + pct(rate) + '</span>') +
    metricHtml(t('total_tokens_label'), compact(summary.total_tokens), '<span>' + withLabel('cached_tokens', compact(summary.cached_tokens)) + '</span><span>' + withLabel('reasoning_tokens', compact(summary.reasoning_tokens)) + '</span>') +
    metricHtml(t('avg_latency'), formatMs(summary.avg_latency_ms)) +
    metricHtml(t('model_count'), formatInteger(models.length), sources.length ? '<span>' + t('source_count') + ' ' + formatInteger(sources.length) + '</span>' : '') +
    metricHtml(t('total_cost'), formatUsd(totalCost), '<span>' + withLabel('total_tokens_label', compact(summary.total_tokens)) + '</span>') +
    '</div>' +
    '<div class="splitGrid">' +
    barsHtml(t('model_distribution'), models, requests, t('no_model_data')) +
    barsHtml(t('source_distribution'), sources, requests, loading ? t('loading_source_data') : t('no_source_data')) +
    '</div>' +
    '<div class="splitGrid">' + apiDetailErrorHtml(errorRows, loading, error, knownFailureCount) + apiDetailRecentHtml(rows, loading, error) + '</div>';
}

async function renderApiDetail() {
  const usage = summaryData && summaryData.usage;
  const apiData = usage && usage.apis && usage.apis[selectedApi];
  if (!apiData) { apiDetailSeq++; apiDetailLastRender = null; setText('apiDetailTitle', t('upstream_detail_select_hint')); $('apiDetail').innerHTML = '<div class="empty">' + t('no_detail_data') + '</div>'; return }
  const api = selectedApi;
  const seq = ++apiDetailSeq;
  const cacheKey = apiDetailCacheKey(api);
  const cached = apiDetailCache.get(cacheKey);
  setText('apiDetailTitle', friendlyApiName(api));
  renderApiDetailContent(apiData, cached ? { detail: cached, loading: true } : { loading: true });
  try {
    const result = await fetchApiDetailData(api);
    if (seq !== apiDetailSeq || api !== selectedApi) return;
    apiDetailCache.set(cacheKey, result);
    renderApiDetailContent(apiData, { detail: result });
  } catch (e) {
    if (seq !== apiDetailSeq || api !== selectedApi) return;
    renderApiDetailContent(apiData, cached ? { detail: cached, error: e } : { error: e });
  }
}

function renderApiDetailFromCache() {
  const usage = summaryData && summaryData.usage;
  const apiData = usage && usage.apis && usage.apis[selectedApi];
  if (!apiData) {
    apiDetailSeq++;
    apiDetailLastRender = null;
    setText('apiDetailTitle', t('upstream_detail_select_hint'));
    $('apiDetail').innerHTML = '<div class="empty">' + t('no_detail_data') + '</div>';
    return;
  }
  setText('apiDetailTitle', friendlyApiName(selectedApi));
  if (apiDetailLastRender && apiDetailLastRender.api === selectedApi) {
    renderApiDetailContent(apiData, apiDetailLastRender.detailState);
    return;
  }
  const cached = apiDetailCache.get(apiDetailCacheKey(selectedApi));
  renderApiDetailContent(apiData, cached ? { detail: cached } : { loading: true });
}

function renderModelStats() {
  if (!summaryData || !summaryData.model_stats) { $('modelStats').innerHTML = '<div class="empty">' + t('no_model_data') + '</div>'; return }
  const rows = summaryData.model_stats;
  $('modelStats').innerHTML = rows.length ? '<table><thead><tr><th>' + t('col_model') + '</th><th>' + t('col_requests') + '</th><th>' + t('col_tokens') + '</th><th>' + t('col_avg_latency') + '</th><th>' + t('col_success_rate') + '</th><th>' + t('col_cache_rate') + '</th><th>' + t('col_cost') + '</th><th>' + t('col_cost_per_m') + '</th></tr></thead><tbody>' + rows.map((r) => {
    const rate = r.total_requests ? r.success_count / r.total_requests * 100 : 100;
    const cost = aggregateCost(r, modelPrices, manualModelPrices);
    const cRate = cacheRate(r);
    const cpM = costPerMillion(r, modelPrices, manualModelPrices);
    return '<tr><td class="nameCell">' + esc(r.model) + '</td><td>' + formatInteger(r.total_requests) + ' <span class="ok">(' + formatInteger(r.success_count) + '</span> <span class="bad">' + formatInteger(r.failure_count) + ')</span></td><td>' + compact(r.total_tokens) + '</td><td>' + formatMs(r.avg_latency_ms) + '</td><td class="' + (rate >= 95 ? 'ok' : rate >= 80 ? 'neutral' : 'bad') + '">' + pct(rate) + '</td><td class="' + (cRate >= 50 ? 'ok' : cRate >= 20 ? 'neutral' : '') + '">' + pct(cRate) + '</td><td>' + formatUsd(cost) + '</td><td>' + (cpM ? formatUsd(cpM) + ' ' + t('cost_per_m_unit') : '-') + '</td></tr>';
  }).join('') + '</tbody></table>' : '<div class="empty">' + t('no_model_data') + '</div>';
}

function renderTrendChart() {
  var usage = summaryData && summaryData.usage;
  if (!usage) { $('trendChart').innerHTML = '<text x="50%" y="50%" text-anchor="middle" class="trendAxisText">' + t('no_trend_data') + '</text>'; clearAnomalyBar(); return }

  var range = $('range').value;
  var useHourly = (range === '7h' || range === '24h');
  var color, barColor;
  if (trendMetric === 'cost') { color = '#f59e0b'; barColor = 'rgba(245,158,11,0.18)'; }
  else if (trendMetric === 'tokens') { color = '#8b5cf6'; barColor = 'rgba(139,92,246,0.18)'; }
  else if (trendMetric === 'rpm') { color = '#22c55e'; barColor = 'rgba(34,197,94,0.18)'; }
  else { color = '#3b82f6'; barColor = 'rgba(59,130,246,0.18)'; }

  // ---- hourly mode (7h / 24h) ----
  if (useHourly) {
    var reqHour = usage.requests_by_hour || {};
    var tokHour = usage.tokens_by_hour || {};
    var costHour = usage.cost_by_hour || {};
    var hours = Object.keys(reqHour).concat(Object.keys(tokHour)).concat(Object.keys(costHour));
    var hourSet = new Set();
    hours.forEach(function(k) { hourSet.add(k); });
    var ordered = Array.from(hourSet).map(Number).filter(function(v) { return !isNaN(v); }).sort(function(a, b) { return a - b; });
    if (!ordered.length) { $('trendChart').innerHTML = '<text x="50%" y="50%" text-anchor="middle" class="trendAxisText">' + t('no_trend_data') + '</text>'; clearAnomalyBar(); return }

    var totalCost = 0, totalToks = 0;
    (summaryData.model_stats || []).forEach(function(r) {
      var t = num(r.total_tokens); if (t > 0) totalToks += t;
      var c = aggregateCost(r, modelPrices, manualModelPrices); if (Number.isFinite(c)) totalCost += c;
    });
    var blendedPrice = totalToks > 0 ? totalCost / totalToks * 1e6 : 0;

    var points = ordered.map(function(h) {
      var reqs = hourBucketValue(reqHour, h);
      var toks = hourBucketValue(tokHour, h);
      var cost = hourBucketValue(costHour, h);
      var hasCost = Object.prototype.hasOwnProperty.call(costHour, String(h).padStart(2, '0')) || Object.prototype.hasOwnProperty.call(costHour, String(h));
      var label = String(h).padStart(2, '0') + ':00';
      return { label: label, requests: reqs, tokens: toks, cost: hasCost ? cost : (blendedPrice && toks ? toks / 1e6 * blendedPrice : 0), rpm: reqs / 60 };
    });

    return renderTrendSvg(points, range, color, barColor, 'hour');
  }

  // ---- daily mode (7d / all) ----
  var tokensDay = usage.tokens_by_day || {};
  var requestsDay = usage.requests_by_day || {};
  var costDay = usage.cost_by_day || {};
  var allDays = new Set();
  Object.keys(tokensDay).forEach(function(k) { allDays.add(k); });
  Object.keys(requestsDay).forEach(function(k) { allDays.add(k); });
  Object.keys(costDay).forEach(function(k) { allDays.add(k); });
  var ordered = Array.from(allDays).sort();
  if (range === 'all' && ordered.length > 30) ordered = ordered.slice(-30);
  if (!ordered.length) { $('trendChart').innerHTML = '<text x="50%" y="50%" text-anchor="middle" class="trendAxisText">' + t('no_trend_data') + '</text>'; clearAnomalyBar(); return }

  var totalCost = 0, totalToks = 0;
  (summaryData.model_stats || []).forEach(function(r) {
    var t = num(r.total_tokens); if (t > 0) totalToks += t;
    var c = aggregateCost(r, modelPrices, manualModelPrices); if (Number.isFinite(c)) totalCost += c;
  });
  var blendedPrice = totalToks > 0 ? totalCost / totalToks * 1e6 : 0;

  var points = ordered.map(function(day) {
    var reqs = num(requestsDay[day]);
    var toks = num(tokensDay[day]);
    var hasCost = Object.prototype.hasOwnProperty.call(costDay, day);
    return { label: day.length > 7 ? day.slice(5) : day, requests: reqs, tokens: toks, cost: hasCost ? num(costDay[day]) : (blendedPrice && toks ? toks / 1e6 * blendedPrice : 0), rpm: reqs / 1440 };
  });

  renderTrendSvg(points, range, color, barColor, 'day');
}

function clearAnomalyBar() {
  var bar = $('anomalyBar');
  if (!bar) return;
  bar.className = 'anomalyBar';
  bar.innerHTML = '';
}

function renderTrendSvg(points, range, color, barColor, mode) {
  var valueFn, formatVal;
  if (trendMetric === 'requests') {
    valueFn = function(p) { return p.requests; }; formatVal = function(v) { return formatInteger(Math.round(v)); };
  } else if (trendMetric === 'tokens') {
    valueFn = function(p) { return p.tokens; }; formatVal = function(v) { return compact(v); };
  } else if (trendMetric === 'rpm') {
    valueFn = function(p) { return p.rpm; }; formatVal = function(v) { return Number(v).toFixed(1); };
  } else {
    valueFn = function(p) { return p.cost; }; formatVal = function(v) { return formatUsd(v); };
  }

  var values = points.map(valueFn);
  var maxVal = Math.max.apply(null, values.concat([1]));
  var minVal = Math.min.apply(null, values.concat([0]));
  var n = points.length;
  if (n < 2) n = 2;

  var W = 1200, H = 240, PL = 70, PR = 20, PT = 24, PB = 36;
  var chartW = W - PL - PR, chartH = H - PT - PB;
  var barW = Math.max(3, Math.min(60, chartW / n - (mode === 'hour' ? 4 : 6)));

  var html = '';
  // Y axis grid lines
  var ySteps = 4;
  for (var i = 0; i <= ySteps; i++) {
    var yVal = minVal + (maxVal - minVal) * i / ySteps;
    var y = PT + chartH - (i / ySteps) * chartH;
    html += '<line x1="' + PL + '" x2="' + (W - PR) + '" y1="' + y + '" y2="' + y + '" class="trendAxisLine"/>';
    html += '<text x="' + (PL - 8) + '" y="' + (y + 4) + '" text-anchor="end" class="trendAxisText">' + esc(formatVal(yVal)) + '</text>';
  }

  // Bars with color
  points.forEach(function(p, i) {
    var v = valueFn(p);
    var title = esc(p.label + ': ' + formatVal(v));
    var barH = maxVal > 0 ? Math.max(2, (v / maxVal) * chartH) : 2;
    var barX = PL + (i + 0.5) * (chartW / n) - barW / 2;
    var barY = PT + chartH - barH;
    html += '<rect x="' + barX + '" y="' + barY + '" width="' + barW + '" height="' + barH + '" fill="' + barColor + '" rx="2"><title>' + title + '</title></rect>';
  });

  // Line
  var lineD = '';
  points.forEach(function(p, i) {
    var v = valueFn(p);
    var lx = PL + (i + 0.5) * (chartW / n);
    var ly = PT + chartH - (maxVal > 0 ? (v / maxVal) * chartH : 0);
    lineD += (i ? 'L' : 'M') + lx + ' ' + ly;
  });
  html += '<path d="' + lineD + '" fill="none" stroke="' + color + '" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" vector-effect="non-scaling-stroke"/>';

  // Points
  points.forEach(function(p, i) {
    var v = valueFn(p);
    var title = esc(p.label + ': ' + formatVal(v));
    var lx = PL + (i + 0.5) * (chartW / n);
    var ly = PT + chartH - (maxVal > 0 ? (v / maxVal) * chartH : 0);
    html += '<circle cx="' + lx + '" cy="' + ly + '" r="4" fill="' + color + '"><title>' + title + '</title></circle>';
  });

  // X axis labels — show every Nth point
  var xStep = mode === 'hour' ? Math.max(1, Math.ceil(n / 12)) : Math.max(1, Math.ceil(n / 14));
  points.forEach(function(p, i) {
    if (i % xStep !== 0 && i !== n - 1) return;
    var lx = PL + (i + 0.5) * (chartW / n);
    html += '<text x="' + lx + '" y="' + (H - 6) + '" text-anchor="middle" class="trendAxisText">' + esc(p.label) + '</text>';
  });

  $('trendChart').setAttribute('viewBox', '0 0 ' + W + ' ' + H);
  $('trendChart').innerHTML = html;

  clearAnomalyBar();
}

function initTrendChart() {
  var select = $('trendMetric');
  var options = [
    { value: 'cost', label: t('trend_daily_cost') },
    { value: 'requests', label: t('trend_daily_requests') },
    { value: 'tokens', label: t('trend_daily_tokens') },
    { value: 'rpm', label: t('trend_daily_rpm') }
  ];
  select.innerHTML = options.map(function(o) { return '<option value="' + o.value + '"' + (trendMetric === o.value ? ' selected' : '') + '>' + o.label + '</option>'; }).join('');
  select.value = trendMetric;
  select.onchange = function() { trendMetric = select.value; renderTrendChart(); };
}

function renderFilters() {
  if (!summaryData) return;
  const models = modelNames();
  const sources = (summaryData.source_stats || []).map(s => s.source);
  const authIndexes = eventsData && eventsData.events ? [...new Set(eventsData.events.map((d) => d.auth_index || '-'))].sort() : [];
  const fill = (id, emptyLabel, values) => { const old = $(id).value; $(id).innerHTML = '<option value="">' + emptyLabel + '</option>' + values.map((v) => '<option value="' + esc(v) + '">' + esc(v) + '</option>').join(''); $(id).value = [...values, ''].includes(old) ? old : '' };
  fill('filterModel', t('filter_all_models'), models);
  fill('filterSource', t('filter_all_sources'), sources);
  fill('filterAuth', t('filter_all_credentials'), authIndexes);
}

function normalizeEventsPayload(data) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) {
    return { events: [], total: 0, limit: eventsLimit, offset: 0 };
  }
  return {
    events: Array.isArray(data.events) ? data.events : [],
    total: data.total || 0,
    limit: data.limit || eventsLimit,
    offset: data.offset || 0,
  };
}

function renderEventsContent() {
  const data = normalizeEventsPayload(eventsData);
  const rows = data.events;
  const total = data.total;
  setText('eventsCount', t('events_count', formatInteger(total), formatInteger(Math.min(rows.length, eventsLimit))));
  $('events').innerHTML = rows.length ? '<table><thead><tr><th>' + t('col_time') + '</th><th>' + t('col_model') + '</th><th>' + t('col_source') + '</th><th>' + t('col_credential') + '</th><th>' + t('col_result') + '</th><th>' + t('col_latency') + '</th><th>' + t('col_input') + '</th><th>' + t('col_output') + '</th><th>' + t('col_thinking') + '</th><th>' + t('col_cache') + '</th><th>' + t('col_total') + '</th></tr></thead><tbody>' + rows.map((d) => '<tr><td>' + formatDateTime(timestampMs(d.timestamp)) + '</td><td class="nameCell">' + esc(d.model) + '</td><td class="nameCell">' + esc(sourceLabel(d)) + '</td><td>' + esc(d.auth_index || '-') + '</td><td class="' + (d.failed ? 'bad' : 'ok') + '">' + statusText(d.failed) + '</td><td>' + formatMs(num(d.latency_ms)) + '</td><td>' + formatInteger(num(d.tokens && d.tokens.input_tokens)) + '</td><td>' + formatInteger(num(d.tokens && d.tokens.output_tokens)) + '</td><td>' + formatInteger(num(d.tokens && d.tokens.reasoning_tokens)) + '</td><td>' + formatInteger(num(d.tokens && Math.max(d.tokens.cached_tokens || 0, d.tokens.cache_tokens || 0))) + '</td><td>' + formatInteger(num(d.tokens && d.tokens.total_tokens)) + '</td></tr>').join('') + '</tbody></table>' : '<div class="empty">' + t('no_events') + '</div>';
  renderFilters();
}

async function renderEvents() {
  // Fetch paginated events from server
  const params = new URLSearchParams();
  params.set('limit', String(eventsLimit));
  params.set('offset', '0');
  params.set('range', $('range').value);
  const fm = $('filterModel').value; if (fm) params.set('model', fm);
  const fs = $('filterSource').value; if (fs) params.set('source', fs);
  const fa = $('filterAuth').value; if (fa) params.set('auth', fa);
  try {
    const url = pluginEndpoint('dashboard-events') + '?' + params.toString();
    eventsData = normalizeEventsPayload(await fetchConditionalJsonPayload('dashboard-events:' + url, url, pluginFetchOptions({ cache: 'no-store' })));
  } catch (e) {
    eventsData = { events: [], total: 0, limit: eventsLimit, offset: 0 };
  }
  renderEventsContent();
}

function download(name, text, type) { const a = document.createElement('a'); a.href = URL.createObjectURL(new Blob([text], { type })); a.download = name; a.click(); setTimeout(() => URL.revokeObjectURL(a.href), 1000) }

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function createExportJob(params) {
  return fetchJsonPayload(managementEndpoint('dashboard-events-export-jobs') + '?' + params.toString(), pluginFetchOptions({ method: 'POST', cache: 'no-store' }));
}

async function getExportJob(id) {
  return fetchJsonPayload(managementEndpoint('dashboard-events-export-jobs?id=' + encodeURIComponent(id)), pluginFetchOptions({ cache: 'no-store' }));
}

async function deleteExportJob(id) {
  try {
    await fetchJsonPayload(managementEndpoint('dashboard-events-export-jobs?id=' + encodeURIComponent(id)), pluginFetchOptions({ method: 'DELETE', cache: 'no-store' }));
  } catch {}
}

async function waitForExportJob(job) {
  let current = job;
  for (let i = 0; i < 120; i++) {
    if (current && current.status === 'succeeded') return current;
    if (current && current.status === 'failed') throw new Error(current.error || t('export_job_failed'));
    await delay(i < 10 ? 250 : 1000);
    current = await getExportJob(job.id);
  }
  throw new Error(t('export_job_timeout'));
}

async function fetchExportJobResult(params) {
  const job = await createExportJob(params);
  if (!job || !job.id) throw new Error(t('export_no_id'));
  try {
    const completed = await waitForExportJob(job);
    const downloadPath = completed.download_path || ('dashboard-events-export-download?id=' + encodeURIComponent(job.id));
    return await fetchTextPayloadWithMeta(managementEndpoint(downloadPath), pluginFetchOptions({ cache: 'no-store' }));
  } finally {
    await deleteExportJob(job.id);
  }
}

function rowsCsv(rows) {
  const head = [t('col_time'), t('col_model'), t('col_source'), t('col_credential'), t('col_result'), 'latency_ms', 'ttft_ms', t('col_input') + ' token', t('col_output') + ' token', t('col_thinking') + ' token', t('col_cache') + ' token', t('col_total') + ' token', t('col_status'), t('error_stats')];
  return [head, ...rows.map((d) => [d.timestamp, d.model, sourceLabel(d), d.auth_index || '', statusText(d.failed), num(d.latency_ms), num(d.ttft_ms), num(d.tokens && d.tokens.input_tokens), num(d.tokens && d.tokens.output_tokens), num(d.tokens && d.tokens.reasoning_tokens), num(d.tokens && Math.max(d.tokens.cached_tokens || 0, d.tokens.cache_tokens || 0)), num(d.tokens && d.tokens.total_tokens), d.status_code || '', d.failure || ''])].map((row) => row.map((v) => '"' + String(v ?? '').replace(/"/g, '""') + '"').join(',')).join('\n');
}

function makeCounterRow(name) { return { model: name, total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, latency: [], providerMap: new Map() } }
function mergeProviderStat(target, stat) {
  if (!target || !target.providerMap || !stat) return;
  const provider = String(stat.provider || '').trim();
  const key = provider.toLowerCase();
  const row = target.providerMap.get(key) || { provider, total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0 };
  row.total_requests += num(stat.total_requests);
  row.success_count += num(stat.success_count);
  row.failure_count += num(stat.failure_count);
  row.total_tokens += num(stat.total_tokens);
  row.input_tokens += num(stat.input_tokens);
  row.output_tokens += num(stat.output_tokens);
  row.cached_tokens += num(stat.cached_tokens);
  row.reasoning_tokens += num(stat.reasoning_tokens);
  target.providerMap.set(key, row);
}
function addDetailProviderToCounter(row, d) {
  if (!row || !row.providerMap) return;
  const tokens = d.tokens || {};
  mergeProviderStat(row, {
    provider: d.provider,
    total_requests: 1,
    success_count: d.failed ? 0 : 1,
    failure_count: d.failed ? 1 : 0,
    total_tokens: totalTokens(d),
    input_tokens: num(tokens.input_tokens),
    output_tokens: num(tokens.output_tokens),
    cached_tokens: Math.max(num(tokens.cached_tokens), num(tokens.cache_tokens)),
    reasoning_tokens: num(tokens.reasoning_tokens),
  });
}
function addDetailToCounter(row, d) {
  const tokens = d.tokens || {};
  row.total_requests++;
  d.failed ? row.failure_count++ : row.success_count++;
  row.total_tokens += totalTokens(d);
  row.input_tokens += num(tokens.input_tokens);
  row.output_tokens += num(tokens.output_tokens);
  row.cached_tokens += Math.max(num(tokens.cached_tokens), num(tokens.cache_tokens));
  row.reasoning_tokens += num(tokens.reasoning_tokens);
  if (num(d.latency_ms) > 0) row.latency.push(num(d.latency_ms));
  addDetailProviderToCounter(row, d);
}
function providerHasValues(provider) {
  return num(provider.total_requests) > 0 || num(provider.total_tokens) > 0 || num(provider.input_tokens) > 0 || num(provider.output_tokens) > 0 || num(provider.cached_tokens) > 0 || num(provider.reasoning_tokens) > 0;
}
function finalizeCounterRow(row) {
  if (row.latency && row.latency.length) row.avg_latency_ms = row.latency.reduce((a, b) => a + b, 0) / row.latency.length;
  if (row.providerMap) {
    const providers = [...row.providerMap.values()].filter(providerHasValues);
    const summed = providers.reduce((acc, p) => mergeCounterRow(acc, p), { total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0 });
    const remainder = {
      provider: '',
      total_requests: Math.max(num(row.total_requests) - num(summed.total_requests), 0),
      success_count: Math.max(num(row.success_count) - num(summed.success_count), 0),
      failure_count: Math.max(num(row.failure_count) - num(summed.failure_count), 0),
      total_tokens: Math.max(num(row.total_tokens) - num(summed.total_tokens), 0),
      input_tokens: Math.max(num(row.input_tokens) - num(summed.input_tokens), 0),
      output_tokens: Math.max(num(row.output_tokens) - num(summed.output_tokens), 0),
      cached_tokens: Math.max(num(row.cached_tokens) - num(summed.cached_tokens), 0),
      reasoning_tokens: Math.max(num(row.reasoning_tokens) - num(summed.reasoning_tokens), 0),
    };
    if (providerHasValues(remainder)) providers.push(remainder);
    providers.sort((a, b) => num(b.total_requests) - num(a.total_requests) || String(a.provider || '').localeCompare(String(b.provider || '')));
    if (providers.length) row.providers = providers;
  }
  delete row.latency;
  delete row.providerMap;
  return row;
}
function applySnapshotCounter(row, raw) {
  if (!raw || typeof raw !== 'object') return row;
  ['total_requests', 'success_count', 'failure_count', 'total_tokens', 'input_tokens', 'output_tokens', 'cached_tokens', 'reasoning_tokens', 'avg_latency_ms'].forEach((field) => {
    if (Object.prototype.hasOwnProperty.call(raw, field)) row[field] = num(raw[field]);
  });
  return row;
}
function mergeCounterRow(target, row) {
  target.total_requests += num(row.total_requests);
  target.success_count += num(row.success_count);
  target.failure_count += num(row.failure_count);
  target.total_tokens += num(row.total_tokens);
  target.input_tokens += num(row.input_tokens);
  target.output_tokens += num(row.output_tokens);
  target.cached_tokens += num(row.cached_tokens);
  target.reasoning_tokens += num(row.reasoning_tokens);
  if (target.providerMap && Array.isArray(row.providers)) row.providers.forEach((provider) => mergeProviderStat(target, provider));
  if (target.providerMap && row.providerMap) row.providerMap.forEach((provider) => mergeProviderStat(target, provider));
  if (Array.isArray(target.latency) && Array.isArray(row.latency)) row.latency.forEach((value) => { if (num(value) > 0) target.latency.push(num(value)); });
  return target;
}
function dashboardRangeCutoffMs(rangeKey, referenceMs) {
  const now = num(referenceMs) || Date.now();
  switch (rangeKey) {
    case '7h': return now - 7 * 60 * 60 * 1000;
    case '24h': return now - 24 * 60 * 60 * 1000;
    case '7d': return now - 7 * 24 * 60 * 60 * 1000;
    default: return 0;
  }
}
function detailMatchesRange(d, cutoffMs) {
  if (!cutoffMs) return true;
  const ms = timestampMs(d && d.timestamp);
  // Exclude details whose timestamp we cannot parse: 0 means "invalid date"
  // and blindly including them would inflate range-scoped aggregates.
  return ms > 0 && ms >= cutoffMs;
}
function incrementSeriesValue(values, key, amount) {
  values[key] = num(values[key]) + num(amount);
}
function detailModelName(fallback, d) {
  const model = String(d && d.model || '').trim();
  if (model) return model;
  const fallbackModel = String(fallback || '').trim();
  return fallbackModel || 'unknown';
}
function credentialStatKey(d) {
  const raw = d && d.auth_index;
  const value = raw == null ? '' : String(raw);
  return value || '(空)';
}
function addDetailToCredentialAgg(credentialAgg, d) {
  const key = credentialStatKey(d);
  const row = credentialAgg.get(key) || { auth_index: key, total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0 };
  row.total_requests++;
  d.failed ? row.failure_count++ : row.success_count++;
  row.total_tokens += totalTokens(d);
  credentialAgg.set(key, row);
}
function detailSeriesBucket(d) {
  const raw = String(d && d.timestamp || '');
  const ms = timestampMs(raw);
  if (!ms) return null;
  const match = raw.match(/^(\d{4}-\d{2}-\d{2})T([01]\d|2[0-3])/);
  if (match) return { day: match[1], hour: match[2] };
  const dt = new Date(ms);
  return { day: dt.toISOString().slice(0, 10), hour: String(dt.getUTCHours()).padStart(2, '0') };
}
function addDetailToUsageTotals(usage, d, latency) {
  const tokens = d.tokens || {};
  usage.total_requests++;
  d.failed ? usage.failure_count++ : usage.success_count++;
  usage.total_tokens += totalTokens(d);
  usage.input_tokens += num(tokens.input_tokens);
  usage.output_tokens += num(tokens.output_tokens);
  usage.cached_tokens += Math.max(num(tokens.cached_tokens), num(tokens.cache_tokens));
  usage.reasoning_tokens += num(tokens.reasoning_tokens);
  if (latency && num(d.latency_ms) > 0) latency.push(num(d.latency_ms));
}
function addDetailToUsageSeries(usage, d) {
  const bucket = detailSeriesBucket(d);
  if (!bucket) return;
  const tokens = totalTokens(d);
  const cost = detailCost(d, modelPrices, manualModelPrices);
  incrementSeriesValue(usage.requests_by_day, bucket.day, 1);
  incrementSeriesValue(usage.requests_by_hour, bucket.hour, 1);
  incrementSeriesValue(usage.tokens_by_day, bucket.day, tokens);
  incrementSeriesValue(usage.tokens_by_hour, bucket.hour, tokens);
  incrementSeriesValue(usage.cost_by_day, bucket.day, cost);
  incrementSeriesValue(usage.cost_by_hour, bucket.hour, cost);
}
function buildSummaryFromFullUsage(data, rangeKey) {
  data = requireObjectPayload(data, 'dashboard-data');
  const rawUsage = data.usage || {};
  const cutoffMs = dashboardRangeCutoffMs(rangeKey, timestampMs(data.generated_at) || Date.now());
  const rangeScoped = cutoffMs > 0;
  const usage = {
    total_requests: rangeScoped ? 0 : rawUsage.total_requests || 0,
    success_count: rangeScoped ? 0 : rawUsage.success_count || 0,
    failure_count: rangeScoped ? 0 : rawUsage.failure_count || 0,
    total_tokens: rangeScoped ? 0 : rawUsage.total_tokens || 0,
    input_tokens: 0,
    output_tokens: 0,
    cached_tokens: 0,
    reasoning_tokens: 0,
    avg_latency_ms: 0,
    apis: {},
    requests_by_day: rangeScoped ? {} : rawUsage.requests_by_day || {},
    requests_by_hour: rangeScoped ? {} : rawUsage.requests_by_hour || {},
    tokens_by_day: rangeScoped ? {} : rawUsage.tokens_by_day || {},
    tokens_by_hour: rangeScoped ? {} : rawUsage.tokens_by_hour || {},
    cost_by_day: rangeScoped ? {} : rawUsage.cost_by_day || {},
    cost_by_hour: rangeScoped ? {} : rawUsage.cost_by_hour || {}
  };
  const modelAgg = new Map(), sourceAgg = new Map(), credentialAgg = new Map(), clientAgg = new Map();
  const latency = [];
  const healthDetails = [];
  Object.entries(rawUsage.apis || {}).forEach(([api, a]) => {
    const apiRow = { total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, avg_latency_ms: 0, models: {}, latency: [] };
    const apiModelRows = new Map();
    Object.entries(a.models || {}).forEach(([model, m]) => {
      const modelRow = makeCounterRow(model);
      (m.details || []).forEach((d) => {
        d.model = rangeScoped ? detailModelName(model, d) : (d.model || model);
        healthDetails.push(d);
        if (!detailMatchesRange(d, cutoffMs)) return;
        const tokens = d.tokens || {};
        const cached = Math.max(num(tokens.cached_tokens), num(tokens.cache_tokens));
        const detailModelRow = rangeScoped ? (apiModelRows.get(d.model) || makeCounterRow(d.model)) : modelRow;
        addDetailToCounter(detailModelRow, d);
        if (rangeScoped) apiModelRows.set(d.model, detailModelRow);
        addDetailToCounter(apiRow, d);
        if (rangeScoped) {
          addDetailToUsageTotals(usage, d, latency);
          addDetailToUsageSeries(usage, d);
        } else {
          usage.input_tokens += num(tokens.input_tokens);
          usage.output_tokens += num(tokens.output_tokens);
          usage.cached_tokens += cached;
          usage.reasoning_tokens += num(tokens.reasoning_tokens);
          if (num(d.latency_ms) > 0) latency.push(num(d.latency_ms));
        }

        const src = sourceLabel(d);
        const sourceRow = sourceAgg.get(src) || { source: src, provider: d.provider || '', total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0 };
        sourceRow.total_requests++; d.failed ? sourceRow.failure_count++ : sourceRow.success_count++; sourceRow.total_tokens += totalTokens(d);
        sourceAgg.set(src, sourceRow);

        addDetailToCredentialAgg(credentialAgg, d);

        const clientKey = clientApiGroupKey(d);
        const clientRow = clientAgg.get(clientKey) || { api_key: clientApiLabel(d), api_key_hash: d.api_key_hash || '', total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, modelMap: new Map() };
        clientRow.total_requests++; d.failed ? clientRow.failure_count++ : clientRow.success_count++; clientRow.total_tokens += totalTokens(d); clientRow.input_tokens += num(tokens.input_tokens); clientRow.output_tokens += num(tokens.output_tokens); clientRow.cached_tokens += cached; clientRow.reasoning_tokens += num(tokens.reasoning_tokens);
        const clientModel = clientRow.modelMap.get(d.model) || makeCounterRow(d.model);
        addDetailToCounter(clientModel, d);
        clientRow.modelMap.set(d.model, clientModel);
        clientAgg.set(clientKey, clientRow);
      });
      if (rangeScoped) return;
      applySnapshotCounter(modelRow, m);
      const globalModel = modelAgg.get(model) || makeCounterRow(model);
      mergeCounterRow(globalModel, modelRow);
      modelAgg.set(model, globalModel);
      apiRow.models[model] = finalizeCounterRow(modelRow);
    });
    if (rangeScoped) {
      apiModelRows.forEach((modelRow, model) => {
        const globalModel = modelAgg.get(model) || makeCounterRow(model);
        mergeCounterRow(globalModel, modelRow);
        modelAgg.set(model, globalModel);
        const finalizedModel = finalizeCounterRow(modelRow);
        if (!finalizedModel.total_requests) return;
        apiRow.models[model] = finalizedModel;
      });
    }
    const finalizedAPI = finalizeCounterRow(rangeScoped ? apiRow : applySnapshotCounter(apiRow, a));
    if (!rangeScoped || finalizedAPI.total_requests) usage.apis[api] = finalizedAPI;
  });
  if (!rangeScoped) applySnapshotCounter(usage, rawUsage);
  usage.avg_latency_ms = latency.length ? latency.reduce((a, b) => a + b, 0) / latency.length : 0;
  if (!rangeScoped && Object.prototype.hasOwnProperty.call(rawUsage, 'avg_latency_ms')) usage.avg_latency_ms = num(rawUsage.avg_latency_ms);
  const credentialStats = [...credentialAgg.values()].sort((a, b) => b.total_requests - a.total_requests);
  return {
    usage,
    health_grid: buildHealthGridFromDetails(healthDetails, data.generated_at),
    source_stats: [...sourceAgg.values()].sort((a, b) => b.total_requests - a.total_requests),
    credential_stats: rangeScoped ? credentialStats : [],
    client_api_stats: [...clientAgg.values()].map((r) => { r.models = [...r.modelMap.values()].map(finalizeCounterRow).sort((a, b) => b.total_requests - a.total_requests); delete r.modelMap; return r }).sort((a, b) => b.total_requests - a.total_requests),
    model_stats: [...modelAgg.values()].map(finalizeCounterRow).sort((a, b) => b.total_requests - a.total_requests),
    generated_at: data.generated_at || new Date().toISOString(),
    _meta: {}
  };
}

function buildHealthGridFromDetails(details, generatedAt) {
  const grid = emptyHealthGrid(generatedAt);
  if (!Array.isArray(details) || !details.length) return grid;
  const windowStart = timestampMs(grid[0].start);
  const windowEnd = timestampMs(grid[grid.length - 1].end);
  details.forEach((detail) => {
    const ms = timestampMs(detail && detail.timestamp);
    if (!ms || ms < windowStart || ms >= windowEnd) return;
    const idx = Math.floor((ms - windowStart) / healthGridStepMs);
    const slot = grid[idx];
    if (!slot) return;
    if (detail.failed) slot.failure++;
    else slot.success++;
    slot.total = slot.success + slot.failure;
  });
  return grid;
}

async function exportRows(kind) {
  const params = new URLSearchParams();
  params.set('range', $('range').value);
  const fm = $('filterModel').value; if (fm) params.set('model', fm);
  const fs = $('filterSource').value; if (fs) params.set('source', fs);
  const fa = $('filterAuth').value; if (fa) params.set('auth', fa);
  try {
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    if (kind === 'csv') {
      params.set('format', 'csv');
      const meta = await fetchExportJobResult(params);
      notifyExportTruncated(exportTruncationFromHeaders(meta.headers));
      download('usage-events-' + stamp + '.csv', meta.data, 'text/csv;charset=utf-8');
      return;
    }
    params.set('format', 'json');
    const meta = await fetchExportJobResult(params);
    const data = typeof meta.data === 'string' ? JSON.parse(meta.data || '{}') : meta.data;
    const rows = data.events || [];
    notifyExportTruncated({ truncated: !!data.truncated, total: data.total, exported: rows.length });
    if (kind === 'json') { download('usage-events-' + stamp + '.json', JSON.stringify(rows, null, 2), 'application/json;charset=utf-8'); return }
    download('usage-events-' + stamp + '.csv', rowsCsv(rows), 'text/csv;charset=utf-8');
  } catch (e) { alert(t('export_failed')); }
}

async function exportApiRows(kind) {
  if (!selectedApi) return;
  const params = new URLSearchParams();
  params.set('range', $('range').value);
  const fm = $('filterModel').value; if (fm) params.set('model', fm);
  const fs = $('filterSource').value; if (fs) params.set('source', fs);
  const fa = $('filterAuth').value; if (fa) params.set('auth', fa);
  params.set('api', selectedApi);
  try {
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    const name = (friendlyApiName(selectedApi) || 'api').replace(/[\\/:*?"<>|\s]+/g, '-').slice(0, 80);
    if (kind === 'csv') {
      params.set('format', 'csv');
      const meta = await fetchExportJobResult(params);
      notifyExportTruncated(exportTruncationFromHeaders(meta.headers));
      download('usage-api-' + name + '-' + stamp + '.csv', meta.data, 'text/csv;charset=utf-8');
      return;
    }
    params.set('format', 'json');
    const meta = await fetchExportJobResult(params);
    const data = typeof meta.data === 'string' ? JSON.parse(meta.data || '{}') : meta.data;
    const rows = data.events || [];
    notifyExportTruncated({ truncated: !!data.truncated, total: data.total, exported: rows.length });
    if (kind === 'json') { download('usage-api-' + name + '-' + stamp + '.json', JSON.stringify(rows, null, 2), 'application/json;charset=utf-8'); return }
    download('usage-api-' + name + '-' + stamp + '.csv', rowsCsv(rows), 'text/csv;charset=utf-8');
  } catch (e) { alert(t('export_failed')); }
}

function exportTruncationFromHeaders(headers) {
  return {
    truncated: headerValue(headers, 'X-Export-Truncated') === 'true',
    total: num(headerValue(headers, 'X-Total-Count')),
    exported: num(headerValue(headers, 'X-Exported-Count')),
  };
}

function notifyExportTruncated(info) {
  if (!info || !info.truncated) return;
  alert(t('export_truncated', fmt.format(num(info.total)), fmt.format(num(info.exported))));
}

function summaryRecordKey(data) {
  if (!data) return '';
  const meta = data._meta || {};
  const usage = data.usage || {};
  return [
    meta.summary_version || '',
    meta.last_recorded_at || '',
    usage.total_requests || '',
    meta.current_detail_count || '',
    meta.evicted_total || '',
  ].join('|');
}

function shouldRefreshDetails(previousSummary, nextSummary, forceDetails) {
  if (forceDetails || !eventsData) return true;
  if (currentRange !== $('range').value) return true;
  const nextKey = summaryRecordKey(nextSummary);
  if (!nextKey) return true;
  return nextKey !== summaryRecordKey(previousSummary);
}

async function rerender(options) {
  const opts = Object.assign({ refreshEvents: true, refreshApiDetail: true }, options || {});
  const previousApi = selectedApi;

  // Refresh locale-aware formatters if language changed
  if (typeof getFormatLocale === 'function') {
    var newLocale = getFormatLocale();
    if (newLocale !== _lastFmtLocale) {
      fmt = new Intl.NumberFormat(newLocale);
      _lastFmtLocale = newLocale;
      if (typeof refreshMoneyFormatters === 'function') refreshMoneyFormatters();
    }
  }

  renderUpdated();
  renderStats();
  renderStorageStatus();
  renderHealth();
  renderPrices();
  renderClientApiStats();
  renderApiStats();
  renderModelStats();
  initTrendChart();
  renderTrendChart();
  if (opts.refreshEvents) await renderEvents();
  else renderEventsContent();
  if (opts.refreshApiDetail || previousApi !== selectedApi) await renderApiDetail();
  else renderApiDetailFromCache();
  if (typeof applyI18N === 'function') applyI18N();
}

function pollDelay() { return document.visibilityState === 'hidden' ? hiddenPollDelayMs : visiblePollDelayMs }
function schedulePoll(delayMs) { if (pollTimer) clearTimeout(pollTimer); pollTimer = setTimeout(load, delayMs) }
function nextFailureDelay() { return Math.min(300000, [5000, 15000, 45000, 90000, 180000][Math.min(pollFailures - 1, 4)] || 300000) }

async function load(options) {
  const forceDetails = options && options.forceDetails;
  try {
    const previousSummary = summaryData;
    const selectedRange = $('range').value;
    // Try new summary endpoint first with current range
    const summaryUrl = pluginEndpoint('dashboard-summary') + '?range=' + encodeURIComponent(selectedRange);
    // A transient model-prices error must not send the page through the
    // compatibility data path; stale prices are better than replacing
    // range-scoped stats with reconstructed fallback data.
    const [data] = await Promise.all([
      fetchConditionalJsonPayload('dashboard-summary:' + summaryUrl, summaryUrl, pluginFetchOptions({ cache: 'no-store' })),
      loadModelPrices().catch(function() { /* prices failure tolerated; stale prices beat wrong stats */ }),
    ]);
    summaryData = requireObjectPayload(data, 'dashboard-summary');
    updatedState = { type: 'success', generatedAt: data.generated_at || Date.now(), message: '' };
    renderUpdated();
    const refreshDetails = shouldRefreshDetails(previousSummary, summaryData, forceDetails);
    await rerender({ refreshEvents: refreshDetails, refreshApiDetail: refreshDetails });
    currentRange = selectedRange;
    pollFailures = 0; schedulePoll(pollDelay());
  } catch (error) {
    // Fallback: try old dashboard-data endpoint
    try {
      const previousSummary = summaryData;
      const selectedRange = $('range').value;
      const [data] = await Promise.all([
        fetchJsonPayload(pluginEndpoint('dashboard-data'), pluginFetchOptions({ cache: 'no-store' })),
        loadModelPrices().catch(function() { /* prices failure tolerated */ }),
      ]);
      summaryData = buildSummaryFromFullUsage(data, selectedRange);
      updatedState = { type: 'compat', generatedAt: data.generated_at || Date.now(), message: '' };
      renderUpdated();
      const refreshDetails = shouldRefreshDetails(previousSummary, summaryData, forceDetails);
      await rerender({ refreshEvents: refreshDetails, refreshApiDetail: refreshDetails });
      currentRange = selectedRange;
      pollFailures = 0; schedulePoll(pollDelay());
    } catch (fallbackError) {
      updatedState = { type: 'error', generatedAt: null, message: (fallbackError && fallbackError.message) || (error && error.message) || '' };
      renderUpdated();
      pollFailures++; schedulePoll(nextFailureDelay());
    }
  }
}

function handleVisibilityChange() {
  if (document.visibilityState === 'visible') {
    load();
    return;
  }
  schedulePoll(hiddenPollDelayMs);
}

// Event bindings
$('range').value = localStorage.getItem(rangeKey) || '24h';
$('range').onchange = () => { localStorage.setItem(rangeKey, $('range').value); load({ forceDetails: true }) };
$('refreshBtn').onclick = () => load({ forceDetails: true });
$('savePrice').onclick = async () => {
  const m = $('priceModel').value.trim(); if (!m) return;
  const prompt = num($('pricePrompt').value), completion = num($('priceCompletion').value), cache = $('priceCache').value === '' ? prompt : num($('priceCache').value);
  try {
    await saveModelPrice(m, { prompt, completion, cache });
    fillPriceForm('');
    await rerender({ refreshEvents: false, refreshApiDetail: true });
  } catch (e) {
    alert(t('price_save_failed') + (e && e.message ? e.message : t('unknown_error')));
  }
};
$('priceModel').onchange = () => syncPriceFormForModel($('priceModel').value);
document.querySelectorAll('[data-api-sort]').forEach((btn) => btn.onclick = () => { clientApiSort = btn.dataset.apiSort || 'requests'; renderClientApiStats() });
['filterModel', 'filterSource', 'filterAuth'].forEach((id) => $(id).onchange = renderEvents);
$('clearFilters').onclick = () => { ['filterModel', 'filterSource', 'filterAuth'].forEach((id) => $(id).value = ''); renderEvents() };
$('exportRowsCsv').onclick = () => exportRows('csv'); $('exportRowsJson').onclick = () => exportRows('json');
$('exportApiCsv').onclick = () => exportApiRows('csv'); $('exportApiJson').onclick = () => exportApiRows('json');
$('exportBtn').onclick = async () => {
  try {
    const data = await fetchJsonPayload(pluginEndpoint('usage/export'), pluginFetchOptions({ cache: 'no-store' }));
    download('usage-export-' + new Date().toISOString().replace(/[:.]/g, '-') + '.json', JSON.stringify(data, null, 2), 'application/json;charset=utf-8');
  } catch (e) { alert(t('export_failed_msg') + (e && e.message ? e.message : t('unknown_error'))) }
};
$('importBtn').onclick = () => $('importFile').click();
$('importFile').onchange = async (e) => {
  const file = e.target.files && e.target.files[0]; if (!file) return;
  try {
    const text = await file.text();
    if (!currentManagementKey()) throw new Error(t('import_no_key'));
    const result = await fetchManagementJsonPayload('usage/import', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: text });
    alert(t('import_complete', result.added || 0, result.skipped || 0, result.ignored_by_retention || 0));
    await load({ forceDetails: true });
  } catch (err) {
    alert(t('import_failed') + (err && err.message ? err.message : t('unknown_error')));
  } finally {
    e.target.value = '';
  }
};
if (document.addEventListener) document.addEventListener('visibilitychange', handleVisibilityChange);
renderUpdated();
initTrendChart();
load();
