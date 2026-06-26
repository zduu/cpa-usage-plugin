// cpausage dashboard — main logic. Uses helpers from helpers.js.
const rangeKey = 'cpa-usage-range-v1';
const fmt = new Intl.NumberFormat('zh-CN');
const money = new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'USD', maximumFractionDigits: 2 });
let summaryData = null;         // DashboardSummary from /dashboard-summary
let eventsData = null;          // EventsResult from /dashboard-events
let modelPrices = {};
let selectedApi = '';
let clientApiSort = 'requests';
let pollTimer = null, pollFailures = 0;
const eventsLimit = 500;

// Dom helpers
const $ = (id) => document.getElementById(id);
const setText = (id, value) => { $(id).textContent = value };

async function fetchJsonPayload(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  let payload = null;
  if (text) {
    try { payload = JSON.parse(text) } catch {
      if (!response.ok) throw new Error(text);
      throw new Error('响应不是有效 JSON');
    }
  }
  if (!response.ok) {
    const message = payload && payload.error && payload.error.message ? payload.error.message : (text || ('请求失败：' + response.status));
    throw new Error(message);
  }
  return unwrapPluginPayload(payload);
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

async function loadModelPrices() {
  const data = await fetchJsonPayload(pluginEndpoint('model-prices'), { cache: 'no-store' });
  modelPrices = (data && data.prices) || {};
  return modelPrices;
}

async function saveModelPrice(model, price) {
  const data = await fetchManagementJsonPayload('model-prices', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, price })
  });
  modelPrices = (data && data.prices) || {};
  return modelPrices;
}

async function deleteModelPrice(model) {
  const params = new URLSearchParams();
  params.set('model', model);
  const data = await fetchManagementJsonPayload('model-prices?' + params.toString(), { method: 'DELETE' });
  modelPrices = (data && data.prices) || {};
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
  setText('successText', '成功请求：' + fmt.format(u.success_count));
  setText('failureText', '失败请求：' + fmt.format(u.failure_count));
  setText('avgLatency', '平均延迟：' + formatMs(u.avg_latency_ms));
  setText('totalTokens', compact(u.total_tokens));
  setText('cachedText', '缓存 token：' + compact(u.cached_tokens));
  setText('reasoningText', '思考 token：' + compact(u.reasoning_tokens));
  // RPM: compute from hourly time series
  const hourValues = Object.values(u.requests_by_hour || {}).map(num);
  const recentHours = hourValues.slice(-1);
  const recentReq = recentHours.length ? recentHours[0] : 0;
  setText('rpm', (recentReq / 60).toFixed(2));
  setText('rpmMeta', '最近1小时请求：' + fmt.format(recentReq));
  const cost = (summaryData.model_stats || []).reduce((s, m) => s + aggregateCost(m, modelPrices), 0);
  setText('totalCost', money.format(cost));
  setText('costMeta', '总 token 数：' + compact(u.total_tokens));
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

function renderHealth() {
  if (!summaryData || !summaryData.health_grid) return;
  const grid = summaryData.health_grid;
  const count = 672, rows = 7, cols = Math.ceil(count / rows);
  let totalS = 0, totalF = 0;
  const cells = [], tooltips = [];
  grid.forEach((slot, i) => {
    totalS += slot.success; totalF += slot.failure;
    const total = slot.total;
    const rate = total ? slot.success / total : -1;
    const timeRange = new Date(slot.start).toLocaleString() + ' - ' + new Date(slot.end).toLocaleString();
    const tip = '<span>' + timeRange + '</span><br>' + (total ? '<span class="ok">成功 ' + slot.success + '</span> <span class="bad">失败 ' + slot.failure + '</span> <span>成功率 ' + pct(rate * 100) + '</span>' : '<span>无请求</span>');
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
  const total = totalS + totalF; setText('healthRate', total ? pct(totalS / total * 100) : '-'); setText('healthSuccess', '成功 ' + fmt.format(totalS)); setText('healthFailure', '失败 ' + fmt.format(totalF));
}

function modelNames() {
  if (summaryData && summaryData.model_stats) return summaryData.model_stats.map(m => m.model).filter(Boolean).sort((a, b) => a.localeCompare(b));
  return [];
}

function priceModelOptions() {
  return [...new Set([...modelNames(), ...Object.keys(modelPrices || {})])].filter(Boolean).sort((a, b) => a.localeCompare(b));
}

function fillPriceForm(model) {
  $('priceModel').value = model || '';
  const p = modelPrices[$('priceModel').value] || {};
  $('pricePrompt').value = p.prompt ?? '';
  $('priceCompletion').value = p.completion ?? '';
  $('priceCache').value = p.cache ?? '';
}

function syncPriceFormForModel(model) {
  if (!model) {
    fillPriceForm('');
    return;
  }
  if (modelPrices[model]) fillPriceForm(model);
}

function renderPrices() {
  const selected = $('priceModel').value;
  $('priceModelOptions').innerHTML = priceModelOptions().map((m) => '<option value="' + esc(m) + '"></option>').join('');
  $('priceModel').value = selected;
  const entries = Object.entries(modelPrices).sort(([a], [b]) => a.localeCompare(b));
  $('priceList').innerHTML = entries.length ? entries.map(([m, p]) => '<div class="priceItem"><div><strong>' + esc(m) + '</strong><div class="priceMeta"><span>输入 ' + num(p.prompt).toFixed(4) + '</span><span>输出 ' + num(p.completion).toFixed(4) + '</span><span>缓存 ' + num(p.cache).toFixed(4) + '</span></div></div><div class="priceActions"><button class="btn" data-edit-price="' + esc(m) + '">编辑</button><button class="btn danger" data-del-price="' + esc(m) + '">删除</button></div></div>').join('') : '<div class="empty">暂无价格设置，设置后会显示估算花费。</div>';
  document.querySelectorAll('[data-edit-price]').forEach((btn) => btn.onclick = () => fillPriceForm(btn.dataset.editPrice));
  document.querySelectorAll('[data-del-price]').forEach((btn) => btn.onclick = async () => {
    try {
      await deleteModelPrice(btn.dataset.delPrice);
      if ($('priceModel').value === btn.dataset.delPrice) fillPriceForm('');
      await rerender();
    } catch (e) {
      alert('删除价格失败：' + (e && e.message ? e.message : '未知错误'));
    }
  });
}

function renderCredentials() {
  if (!summaryData || !summaryData.source_stats) { $('credentialStats').innerHTML = '<div class="empty">暂无来源数据</div>'; return }
  const rows = summaryData.source_stats;
  $('credentialStats').innerHTML = rows.length ? '<table><thead><tr><th>来源</th><th>请求次数</th><th>成功率</th></tr></thead><tbody>' + rows.map((r) => {
    const rate = r.total_requests ? r.success_count / r.total_requests * 100 : 100;
    return '<tr><td class="nameCell">' + esc(r.source) + (r.provider && r.provider !== r.source ? '<span class="pill">' + esc(r.provider) + '</span>' : '') + '</td><td>' + fmt.format(r.total_requests) + ' <span class="ok">(' + fmt.format(r.success_count) + '</span> <span class="bad">' + fmt.format(r.failure_count) + ')</span></td><td class="' + (rate >= 95 ? 'ok' : rate >= 80 ? 'neutral' : 'bad') + '">' + pct(rate) + '</td></tr>'
  }).join('') + '</tbody></table>' : '<div class="empty">暂无来源数据</div>';
}

function renderClientApiStats() {
  const stats = summaryData && summaryData.client_api_stats;
  if (!stats || !stats.length) { $('clientApiStats').innerHTML = '<div class="empty">暂无 API key 请求数据</div>'; return }
  let rows = stats.map((r) => ({
    name: r.api_key || '未知 API',
    requests: r.total_requests,
    success: r.success_count,
    failure: r.failure_count,
    tokens: r.total_tokens,
    cost: (r.models || []).reduce((s, m) => s + aggregateCost(m, modelPrices), 0)
  }));
  if (clientApiSort === 'tokens') rows.sort((a, b) => b.tokens - a.tokens);
  else if (clientApiSort === 'cost') rows.sort((a, b) => b.cost - a.cost);
  else rows.sort((a, b) => b.requests - a.requests);
  document.querySelectorAll('[data-api-sort]').forEach((btn) => btn.classList.toggle('active', btn.dataset.apiSort === clientApiSort));
  $('clientApiStats').innerHTML = rows.length ? '<div class="apiCardGrid">' + rows.map((r) => '<div class="apiCard"><div><div class="apiName">' + esc(r.name) + '</div><div class="apiChips"><span class="chip">请求次数: ' + fmt.format(r.requests) + '（<span class="ok">' + fmt.format(r.success) + '</span> <span class="bad">' + fmt.format(r.failure) + '</span>）</span><span class="chip">Token数量: ' + compact(r.tokens) + '</span><span class="chip">总花费: ' + money.format(r.cost) + '</span></div></div><div class="apiArrow">▶</div></div>').join('') + '</div>' : '<div class="empty">暂无 API key 请求数据</div>';
}

function renderApiStats() {
  const usage = summaryData && summaryData.usage;
  if (!usage || !usage.apis) { $('apiStats').innerHTML = '<div class="empty">暂无接口数据</div>'; $('apiSelect').innerHTML = '<option value="">暂无上游接口</option>'; return }
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
  $('apiSelect').innerHTML = rows.length ? rows.map((r) => '<option value="' + esc(r.api) + '">' + esc(friendlyApiName(r.api)) + '</option>').join('') : '<option value="">暂无上游接口</option>';
  $('apiSelect').value = selectedApi;
  $('apiSelect').disabled = !rows.length;
  $('apiSelect').onchange = () => { selectedApi = $('apiSelect').value; renderApiStats(); renderApiDetail(); renderEvents() };
  $('apiStats').innerHTML = rows.length ? '<table><thead><tr><th>接口</th><th>请求</th><th>成功率</th><th>token</th><th>平均延迟</th><th>模型</th></tr></thead><tbody>' + rows.map((r) => '<tr class="clickableRow ' + (r.api === selectedApi ? 'selectedRow' : '') + '" data-api="' + esc(r.api) + '"><td class="nameCell">' + esc(friendlyApiName(r.api)) + '</td><td>' + fmt.format(r.requests) + ' <span class="ok">(' + fmt.format(r.success) + '</span> <span class="bad">' + fmt.format(r.failure) + ')</span></td><td class="' + (r.successRate >= 95 ? 'ok' : r.successRate >= 80 ? 'neutral' : 'bad') + '">' + pct(r.successRate) + '</td><td>' + compact(r.tokens) + '</td><td>' + formatMs(r.avgLatency) + '</td><td>' + r.modelCount + ' 个</td></tr>').join('') + '</tbody></table>' : '<div class="empty">暂无接口数据</div>';
  document.querySelectorAll('[data-api]').forEach((row) => row.onclick = () => { selectedApi = row.getAttribute('data-api') || ''; renderApiStats(); renderApiDetail(); renderEvents() });
}

function renderApiDetail() {
  const usage = summaryData && summaryData.usage;
  const apiData = usage && usage.apis && usage.apis[selectedApi];
  if (!apiData) { setText('apiDetailTitle', '选择一个上游接口查看模型、来源、错误和最近请求。'); $('apiDetail').innerHTML = '<div class="empty">暂无接口详情</div>'; return }
  setText('apiDetailTitle', friendlyApiName(selectedApi));
  const requests = apiData.total_requests, success = apiData.success_count, failure = apiData.failure_count;
  const rate = requests ? success / requests * 100 : 100;
  const models = Object.entries(apiData.models || {}).map(([name, m]) => ({ name, requests: m.total_requests, success: m.success_count, failure: m.failure_count, tokens: m.total_tokens, avgLatency: m.avg_latency_ms })).sort((a, b) => b.requests - a.requests);
  // Source stats for this API not available without details — show model distribution instead
  $('apiDetail').innerHTML = '<div class="detailGrid">' +
    '<div class="metric"><div class="metricLabel">请求数</div><div class="metricValue">' + fmt.format(requests) + '</div><div class="subtle" style="margin-top:6px"><span class="ok">成功 ' + fmt.format(success) + '</span> <span class="bad">失败 ' + fmt.format(failure) + '</span></div></div>' +
    '<div class="metric"><div class="metricLabel">成功率</div><div class="metricValue ' + (rate >= 95 ? 'ok' : rate >= 80 ? 'neutral' : 'bad') + '">' + pct(rate) + '</div></div>' +
    '<div class="metric"><div class="metricLabel">总 token</div><div class="metricValue">' + compact(apiData.total_tokens) + '</div></div>' +
    '<div class="metric"><div class="metricLabel">平均延迟</div><div class="metricValue">' + formatMs(apiData.avg_latency_ms) + '</div></div>' +
    '<div class="metric"><div class="metricLabel">模型数</div><div class="metricValue">' + fmt.format(models.length) + '</div></div>' +
    '<div class="metric"><div class="metricLabel">Token/请求</div><div class="metricValue">' + compact(requests ? Math.round(apiData.total_tokens / requests) : 0) + '</div></div>' +
    '</div>' +
    '<div><div class="subtle" style="margin-bottom:8px">模型分布</div>' +
    (models.length ? '<div class="barList">' + models.slice(0, 8).map((r) => { const width = requests ? Math.max(4, Math.round(r.requests / requests * 100)) : 0; return '<div class="barItem"><div class="nameCell">' + esc(r.name) + '</div><div class="barTrack"><div class="barFill" style="width:' + width + '%"></div></div><div>' + fmt.format(r.requests) + ' 次</div></div>' }).join('') + '</div>' : '<div class="empty">暂无模型数据</div>') +
    '</div>';
}

function renderModelStats() {
  if (!summaryData || !summaryData.model_stats) { $('modelStats').innerHTML = '<div class="empty">暂无模型数据</div>'; return }
  const rows = summaryData.model_stats;
  $('modelStats').innerHTML = rows.length ? '<table><thead><tr><th>模型</th><th>请求</th><th>token</th><th>平均延迟</th><th>成功率</th><th>花费</th></tr></thead><tbody>' + rows.map((r) => {
    const rate = r.total_requests ? r.success_count / r.total_requests * 100 : 100;
    const cost = aggregateCost(r, modelPrices);
    return '<tr><td class="nameCell">' + esc(r.model) + '</td><td>' + fmt.format(r.total_requests) + ' <span class="ok">(' + fmt.format(r.success_count) + '</span> <span class="bad">' + fmt.format(r.failure_count) + ')</span></td><td>' + compact(r.total_tokens) + '</td><td>' + formatMs(r.avg_latency_ms) + '</td><td class="' + (rate >= 95 ? 'ok' : rate >= 80 ? 'neutral' : 'bad') + '">' + pct(rate) + '</td><td>' + money.format(cost) + '</td></tr>'
  }).join('') + '</tbody></table>' : '<div class="empty">暂无模型数据</div>';
}

function renderFilters() {
  if (!summaryData) return;
  const models = modelNames();
  const sources = (summaryData.source_stats || []).map(s => s.source);
  const authIndexes = eventsData && eventsData.events ? [...new Set(eventsData.events.map((d) => d.auth_index || '-'))].sort() : [];
  const fill = (id, label, values) => { const old = $(id).value; $(id).innerHTML = '<option value="">全部' + label + '</option>' + values.map((v) => '<option value="' + esc(v) + '">' + esc(v) + '</option>').join(''); $(id).value = [...values, ''].includes(old) ? old : '' };
  fill('filterModel', '模型', models);
  fill('filterSource', '来源', sources);
  fill('filterAuth', '凭证', authIndexes);
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
  if (selectedApi) params.set('api', selectedApi);
  try {
    eventsData = await fetchJsonPayload(pluginEndpoint('dashboard-events') + '?' + params.toString(), { cache: 'no-store' });
  } catch (e) {
    eventsData = { events: [], total: 0, limit: eventsLimit, offset: 0 };
  }
  const rows = eventsData.events || [];
  const total = eventsData.total || 0;
  setText('eventsCount', '共 ' + fmt.format(total) + ' 条，显示 ' + fmt.format(Math.min(rows.length, eventsLimit)) + ' 条');
  $('events').innerHTML = rows.length ? '<table><thead><tr><th>时间</th><th>模型</th><th>来源</th><th>凭证</th><th>结果</th><th>延迟</th><th>输入</th><th>输出</th><th>思考</th><th>缓存</th><th>总计</th></tr></thead><tbody>' + rows.map((d) => '<tr><td>' + new Date(timestampMs(d.timestamp)).toLocaleString() + '</td><td class="nameCell">' + esc(d.model) + '</td><td>' + esc(sourceLabel(d)) + '</td><td>' + (esc(d.auth_index || '-')) + '</td><td class="' + (d.failed ? 'bad' : 'ok') + '">' + (d.failed ? '失败' : '成功') + '</td><td>' + formatMs(num(d.latency_ms)) + '</td><td>' + fmt.format(num(d.tokens && d.tokens.input_tokens)) + '</td><td>' + fmt.format(num(d.tokens && d.tokens.output_tokens)) + '</td><td>' + fmt.format(num(d.tokens && d.tokens.reasoning_tokens)) + '</td><td>' + fmt.format(num(d.tokens && Math.max(d.tokens.cached_tokens || 0, d.tokens.cache_tokens || 0))) + '</td><td>' + fmt.format(num(d.tokens && d.tokens.total_tokens)) + '</td></tr>').join('') + '</tbody></table>' : '<div class="empty">暂无请求事件</div>';
  renderFilters();
}

function download(name, text, type) { const a = document.createElement('a'); a.href = URL.createObjectURL(new Blob([text], { type })); a.download = name; a.click(); setTimeout(() => URL.revokeObjectURL(a.href), 1000) }

function rowsCsv(rows) {
  const head = ['时间', '模型', '来源', '凭证', '结果', '延迟毫秒', 'TTFT毫秒', '输入 token', '输出 token', '思考 token', '缓存 token', '总 token', '状态码', '错误'];
  return [head, ...rows.map((d) => [d.timestamp, d.model, sourceLabel(d), d.auth_index || '', d.failed ? '失败' : '成功', num(d.latency_ms), num(d.ttft_ms), num(d.tokens && d.tokens.input_tokens), num(d.tokens && d.tokens.output_tokens), num(d.tokens && d.tokens.reasoning_tokens), num(d.tokens && Math.max(d.tokens.cached_tokens || 0, d.tokens.cache_tokens || 0)), num(d.tokens && d.tokens.total_tokens), d.status_code || '', d.failure || ''])].map((row) => row.map((v) => '"' + String(v ?? '').replace(/"/g, '""') + '"').join(',')).join('\n');
}

function makeCounterRow(name) { return { model: name, total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, latency: [] } }
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
}
function finalizeCounterRow(row) {
  if (row.latency && row.latency.length) row.avg_latency_ms = row.latency.reduce((a, b) => a + b, 0) / row.latency.length;
  delete row.latency;
  return row;
}
function buildSummaryFromFullUsage(data) {
  const rawUsage = data.usage || {};
  const usage = {
    total_requests: rawUsage.total_requests || 0,
    success_count: rawUsage.success_count || 0,
    failure_count: rawUsage.failure_count || 0,
    total_tokens: rawUsage.total_tokens || 0,
    input_tokens: 0,
    output_tokens: 0,
    cached_tokens: 0,
    reasoning_tokens: 0,
    avg_latency_ms: 0,
    apis: {},
    requests_by_day: rawUsage.requests_by_day || {},
    requests_by_hour: rawUsage.requests_by_hour || {},
    tokens_by_day: rawUsage.tokens_by_day || {},
    tokens_by_hour: rawUsage.tokens_by_hour || {}
  };
  const modelAgg = new Map(), sourceAgg = new Map(), clientAgg = new Map();
  const latency = [];
  Object.entries(rawUsage.apis || {}).forEach(([api, a]) => {
    const apiRow = { total_requests: a.total_requests || 0, success_count: a.success_count || 0, failure_count: a.failure_count || 0, total_tokens: a.total_tokens || 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, avg_latency_ms: 0, models: {}, latency: [] };
    Object.entries(a.models || {}).forEach(([model, m]) => {
      const modelRow = makeCounterRow(model);
      (m.details || []).forEach((d) => {
        d.model = d.model || model;
        const tokens = d.tokens || {};
        const cached = Math.max(num(tokens.cached_tokens), num(tokens.cache_tokens));
        addDetailToCounter(modelRow, d);
        addDetailToCounter(apiRow, d);
        usage.input_tokens += num(tokens.input_tokens);
        usage.output_tokens += num(tokens.output_tokens);
        usage.cached_tokens += cached;
        usage.reasoning_tokens += num(tokens.reasoning_tokens);
        if (num(d.latency_ms) > 0) latency.push(num(d.latency_ms));

        const globalModel = modelAgg.get(d.model) || makeCounterRow(d.model);
        addDetailToCounter(globalModel, d);
        modelAgg.set(d.model, globalModel);

        const src = sourceLabel(d);
        const sourceRow = sourceAgg.get(src) || { source: src, provider: d.provider || '', total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0 };
        sourceRow.total_requests++; d.failed ? sourceRow.failure_count++ : sourceRow.success_count++; sourceRow.total_tokens += totalTokens(d);
        sourceAgg.set(src, sourceRow);

        const clientKey = d.api_key_hash || d.api_key || '(unknown)';
        const clientRow = clientAgg.get(clientKey) || { api_key: d.api_key || '未知 API', api_key_hash: d.api_key_hash || '', total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, modelMap: new Map() };
        clientRow.total_requests++; d.failed ? clientRow.failure_count++ : clientRow.success_count++; clientRow.total_tokens += totalTokens(d); clientRow.input_tokens += num(tokens.input_tokens); clientRow.output_tokens += num(tokens.output_tokens); clientRow.cached_tokens += cached; clientRow.reasoning_tokens += num(tokens.reasoning_tokens);
        const clientModel = clientRow.modelMap.get(d.model) || makeCounterRow(d.model);
        addDetailToCounter(clientModel, d);
        clientRow.modelMap.set(d.model, clientModel);
        clientAgg.set(clientKey, clientRow);
      });
      apiRow.models[model] = finalizeCounterRow(modelRow);
    });
    usage.apis[api] = finalizeCounterRow(apiRow);
  });
  usage.avg_latency_ms = latency.length ? latency.reduce((a, b) => a + b, 0) / latency.length : 0;
  return {
    usage,
    health_grid: [],
    source_stats: [...sourceAgg.values()].sort((a, b) => b.total_requests - a.total_requests),
    credential_stats: [],
    client_api_stats: [...clientAgg.values()].map((r) => { r.models = [...r.modelMap.values()].map(finalizeCounterRow).sort((a, b) => b.total_requests - a.total_requests); delete r.modelMap; return r }).sort((a, b) => b.total_requests - a.total_requests),
    model_stats: [...modelAgg.values()].map(finalizeCounterRow).sort((a, b) => b.total_requests - a.total_requests),
    generated_at: data.generated_at || new Date().toISOString(),
    _meta: {}
  };
}

async function exportRows(kind) {
  const params = new URLSearchParams();
  params.set('range', $('range').value);
  const fm = $('filterModel').value; if (fm) params.set('model', fm);
  const fs = $('filterSource').value; if (fs) params.set('source', fs);
  const fa = $('filterAuth').value; if (fa) params.set('auth', fa);
  if (selectedApi) params.set('api', selectedApi);
  try {
    const data = await fetchAllEventPages(
      (pageParams) => fetchJsonPayload(pluginEndpoint('dashboard-events') + '?' + pageParams.toString(), { cache: 'no-store' }),
      params,
      eventsLimit
    );
    const rows = data.events || [];
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    if (kind === 'json') { download('usage-events-' + stamp + '.json', JSON.stringify(rows, null, 2), 'application/json;charset=utf-8'); return }
    download('usage-events-' + stamp + '.csv', rowsCsv(rows), 'text/csv;charset=utf-8');
  } catch (e) { alert('导出失败'); }
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
    const data = await fetchAllEventPages(
      (pageParams) => fetchJsonPayload(pluginEndpoint('dashboard-events') + '?' + pageParams.toString(), { cache: 'no-store' }),
      params,
      eventsLimit
    );
    const rows = data.events || [];
    if (!rows.length) return;
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    const name = (friendlyApiName(selectedApi) || 'api').replace(/[\\/:*?"<>|\s]+/g, '-').slice(0, 80);
    if (kind === 'json') { download('usage-api-' + name + '-' + stamp + '.json', JSON.stringify(rows, null, 2), 'application/json;charset=utf-8'); return }
    download('usage-api-' + name + '-' + stamp + '.csv', rowsCsv(rows), 'text/csv;charset=utf-8');
  } catch (e) { alert('导出失败'); }
}

async function rerender() {
  renderStats();
  renderHealth();
  renderPrices();
  renderCredentials();
  renderClientApiStats();
  renderApiStats();
  renderApiDetail();
  renderModelStats();
  await renderEvents();
}

function schedulePoll(delayMs) { if (pollTimer) clearTimeout(pollTimer); pollTimer = setTimeout(load, delayMs) }
function nextFailureDelay() { return Math.min(300000, [5000, 15000, 45000, 90000, 180000][Math.min(pollFailures - 1, 4)] || 300000) }

async function load() {
  try {
    // Try new summary endpoint first
    const [data] = await Promise.all([
      fetchJsonPayload(pluginEndpoint('dashboard-summary'), { cache: 'no-store' }),
      loadModelPrices()
    ]);
    summaryData = data;
    setText('updated', '更新于 ' + new Date(data.generated_at || Date.now()).toLocaleTimeString());
    await rerender();
    pollFailures = 0; schedulePoll(30000);
  } catch (error) {
    // Fallback: try old dashboard-data endpoint
    try {
      const [data] = await Promise.all([
        fetchJsonPayload(pluginEndpoint('dashboard-data'), { cache: 'no-store' }),
        loadModelPrices()
      ]);
      summaryData = buildSummaryFromFullUsage(data);
      setText('updated', '更新于 ' + new Date(data.generated_at || Date.now()).toLocaleTimeString() + '（兼容模式）');
      await rerender();
      pollFailures = 0; schedulePoll(30000);
    } catch (fallbackError) {
      setText('updated', (error && error.message) || '加载用量统计失败');
      pollFailures++; schedulePoll(nextFailureDelay());
    }
  }
}

// Event bindings
$('range').value = localStorage.getItem(rangeKey) || '24h';
$('range').onchange = () => { localStorage.setItem(rangeKey, $('range').value); load() };
$('refreshBtn').onclick = load;
$('savePrice').onclick = async () => {
  const m = $('priceModel').value.trim(); if (!m) return;
  const prompt = num($('pricePrompt').value), completion = num($('priceCompletion').value), cache = $('priceCache').value === '' ? prompt : num($('priceCache').value);
  try {
    await saveModelPrice(m, { prompt, completion, cache });
    fillPriceForm('');
    await rerender();
  } catch (e) {
    alert('保存价格失败：' + (e && e.message ? e.message : '未知错误'));
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
    const data = await fetchJsonPayload(pluginEndpoint('usage/export'), { cache: 'no-store' });
    download('usage-export-' + new Date().toISOString().replace(/[:.]/g, '-') + '.json', JSON.stringify(data, null, 2), 'application/json;charset=utf-8');
  } catch (e) { alert('导出失败：' + (e && e.message ? e.message : '未知错误')) }
};
$('importBtn').onclick = () => $('importFile').click();
$('importFile').onchange = async (e) => {
  const file = e.target.files && e.target.files[0]; if (!file) return;
  try {
    const text = await file.text();
    if (!currentManagementKey()) throw new Error('未读取到管理登录状态，请回到管理中心重新登录并勾选记住登录。');
    const result = await fetchManagementJsonPayload('usage/import', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: text });
    alert('导入完成：新增 ' + (result.added || 0) + '，跳过 ' + (result.skipped || 0) + '，过期忽略 ' + (result.ignored_by_retention || 0));
    await load();
  } catch (err) {
    alert('导入失败：' + (err && err.message ? err.message : '未知错误'));
  } finally {
    e.target.value = '';
  }
};
load();
