const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class FakeElement {
  constructor(id) {
    this.id = id;
    this.value = '';
    this.textContent = '';
    this.innerHTML = '';
    this.disabled = false;
    this.clientWidth = 320;
    this.dataset = {};
    this.style = {};
    this.files = [];
    this.classList = {
      add() {},
      remove() {},
      toggle() {},
    };
  }
  setAttribute(name, value) {
    this[name] = value;
  }
  getAttribute(name) {
    return this[name] || '';
  }
  click() {
    if (typeof this.onclick === 'function') this.onclick({ target: this });
  }
  closest() {
    return null;
  }
  getBoundingClientRect() {
    return { left: 0, right: 12, top: 0 };
  }
}

function createDashboardHarness(options = {}) {
  const elements = new Map();
  const listeners = new Map();
  let visibilityState = options.visibilityState || 'visible';
  const sortButtons = ['requests', 'tokens', 'cost'].map((name) => {
    const el = new FakeElement('sort-' + name);
    el.dataset.apiSort = name;
    return el;
  });
  const downloads = [];
  const fetchCalls = [];
  const fetchRequests = [];
  const timeoutDelays = [];
  let summaryLastRecordedAt = options.lastRecordedAt || '2023-11-15T06:13:20Z';
  let prices = { 'gpt-4.1': { prompt: 2, completion: 8, cache: 0.5 } };
  const dashboardEtags = !!options.dashboardEtags;
  const wrapDashboardResponses = !!options.wrapDashboardResponses;

  const document = {
    get visibilityState() {
      return visibilityState;
    },
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    querySelectorAll(selector) {
      if (selector === '[data-api-sort]') return sortButtons;
      return [];
    },
    createElement(tag) {
      return new FakeElement(tag);
    },
    addEventListener(type, handler) {
      const handlers = listeners.get(type) || [];
      handlers.push(handler);
      listeners.set(type, handlers);
    },
  };

  const localStorage = {
    values: new Map(),
    getItem(key) {
      return this.values.has(key) ? this.values.get(key) : null;
    },
    setItem(key, value) {
      this.values.set(key, String(value));
    },
  };

  const summary = {
    generated_at: new Date().toISOString(),
    usage: {
      total_requests: 1200,
      success_count: 1190,
      failure_count: 10,
      total_tokens: 24000,
      cached_tokens: 100,
      reasoning_tokens: 50,
      avg_latency_ms: 120,
      apis: {
        openai: {
          total_requests: 1200,
          success_count: 1190,
          failure_count: 10,
          total_tokens: 24000,
          input_tokens: 4000,
          output_tokens: 5000,
          cached_tokens: 100,
          reasoning_tokens: 50,
          avg_latency_ms: 120,
          models: {
            'gpt-4.1': {
              total_requests: 1200,
              success_count: 1190,
              failure_count: 10,
              total_tokens: 24000,
              input_tokens: 4000,
              output_tokens: 5000,
              cached_tokens: 100,
              reasoning_tokens: 50,
              avg_latency_ms: 120,
            },
          },
        },
      },
      requests_by_hour: { '12': 1200 },
      tokens_by_hour: { '12': 24000 },
      requests_by_day: {},
      tokens_by_day: {},
    },
    health_grid: [],
    source_stats: [{ source: 'openai-prod', total_requests: 1200, success_count: 1190, failure_count: 10, total_tokens: 24000 }],
    credential_stats: [],
    client_api_stats: [],
    model_stats: [{ model: 'gpt-4.1', total_requests: 1200, success_count: 1190, failure_count: 10, total_tokens: 24000, input_tokens: 4000, output_tokens: 5000, cached_tokens: 0, reasoning_tokens: 0 }],
    _meta: {
      last_recorded_at: summaryLastRecordedAt,
      storage: { enabled: false, path: 'usage-statistics.jsonl' },
    },
  };
  if (options.storage) summary._meta.storage = options.storage;

  function eventsPage(url) {
    const parsed = new URL(url, 'http://test.local/v0/management/plugins/usage-statistics/dashboard');
    const offset = Number(parsed.searchParams.get('offset') || 0);
    const limit = Number(parsed.searchParams.get('limit') || 500);
    const count = Math.min(limit, Math.max(1200 - offset, 0));
    return {
      total: 1200,
      limit,
      offset,
      generated_at: new Date().toISOString(),
      events: Array.from({ length: count }, (_, i) => {
        const idx = offset + i;
        return {
          timestamp: new Date(1700000000000 + idx).toISOString(),
          model: 'gpt-4.1',
          source: 'openai-prod',
          provider: 'openai',
          auth_index: 'auth-1',
          failed: false,
          latency_ms: 120,
          tokens: { input_tokens: 10, output_tokens: 5, total_tokens: 15 },
        };
      }),
    };
  }

  function eventsExport(url) {
    const parsed = new URL(url, 'http://test.local/v0/management/plugins/usage-statistics/dashboard');
    const api = parsed.searchParams.get('api');
    const totalRows = api ? 8 : 1200;
    if (parsed.searchParams.get('format') === 'csv') {
      return '时间,模型,来源,凭证,结果,延迟毫秒,TTFT毫秒,输入 token,输出 token,思考 token,缓存 token,总 token,状态码,错误\n' +
        eventsPage('http://test.local/dashboard-events?limit=' + totalRows + '&offset=0').events.slice(0, totalRows)
          .map((event) => [event.timestamp, event.model, event.source, event.auth_index, event.failed ? '失败' : '成功', event.latency_ms, '', event.tokens.input_tokens, event.tokens.output_tokens, '', '', event.tokens.total_tokens, '', ''].join(','))
          .join('\n');
    }
    return {
      total: totalRows,
      limit: totalRows,
      offset: 0,
      generated_at: new Date().toISOString(),
      events: eventsPage('http://test.local/dashboard-events?limit=' + totalRows + '&offset=0').events.slice(0, totalRows),
    };
  }

  function apiDetailPayload() {
    return {
      api: 'openai',
      summary: {
        total_requests: 8,
        success_count: 7,
        failure_count: 1,
        total_tokens: 105,
        input_tokens: 70,
        output_tokens: 35,
        cached_tokens: 10,
        reasoning_tokens: 5,
        avg_latency_ms: 113,
      },
      model_stats: [
        { model: 'gpt-4.1', total_requests: 7, success_count: 7, failure_count: 0, total_tokens: 105, input_tokens: 70, output_tokens: 35, cached_tokens: 10, reasoning_tokens: 5 },
        { model: 'deepseek-v4-flash-free', total_requests: 1, success_count: 0, failure_count: 1, total_tokens: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0 },
      ],
      source_stats: [{ source: 'openai-prod', total_requests: 8, success_count: 7, failure_count: 1, total_tokens: 105 }],
      error_stats: [{ status_code: 401, count: 1, failure: '{"type":"error","error":{"type":"ModelError","message":"Model deepseek-v4-flash-free is not supported"}}' }],
      recent_events: Array.from({ length: 8 }, (_, i) => {
        const failed = i === 1;
        return {
          timestamp: new Date(1700000008000 - i * 1000).toISOString(),
          model: failed ? 'deepseek-v4-flash-free' : 'gpt-4.1',
          source: 'openai-prod',
          provider: 'openai',
          auth_index: 'auth-1',
          failed,
          status_code: failed ? 401 : 200,
          failure: failed ? '{"type":"error","error":{"type":"ModelError","message":"Model deepseek-v4-flash-free is not supported"}}' : '',
          latency_ms: failed ? 64 : 120,
          tokens: failed ? { total_tokens: 0 } : { input_tokens: 10, output_tokens: 5, total_tokens: 15 },
        };
      }),
      total_events: 8,
      generated_at: new Date().toISOString(),
    };
  }

  function requestHeaderValue(requestOptions, name) {
    const headers = requestOptions && requestOptions.headers;
    if (!headers) return '';
    if (typeof headers.get === 'function') return headers.get(name) || headers.get(String(name).toLowerCase()) || '';
    const target = String(name).toLowerCase();
    for (const [key, value] of Object.entries(headers)) {
      if (String(key).toLowerCase() === target) return Array.isArray(value) ? String(value[0] || '') : String(value || '');
    }
    return '';
  }

  function dashboardRoute(url) {
    const text = String(url);
    if (text.includes('dashboard-summary')) return 'dashboard-summary';
    if (text.includes('dashboard-api-detail')) return 'dashboard-api-detail';
    if (text.includes('dashboard-events') && !text.includes('dashboard-events-export')) return 'dashboard-events';
    return '';
  }

  function dashboardEtag(route, url) {
    if (route === 'dashboard-summary') return 'W/"summary-' + summaryLastRecordedAt + '"';
    return 'W/"' + route + '-' + Buffer.from(String(url)).toString('base64url') + '"';
  }

  function fetchHeaders(headers) {
    return {
      get(name) {
        const target = String(name).toLowerCase();
        for (const [key, value] of Object.entries(headers || {})) {
          if (String(key).toLowerCase() === target) return Array.isArray(value) ? String(value[0] || '') : String(value || '');
        }
        return '';
      },
    };
  }

  function fetchResponse(payload, route, url, requestOptions) {
    let status = 200;
    const headers = {};
    if (dashboardEtags && route) {
      const etag = dashboardEtag(route, url);
      headers.ETag = [etag];
      if (requestHeaderValue(requestOptions, 'If-None-Match') === etag) status = 304;
    }
    if (wrapDashboardResponses && route) {
      const result = {
        status_code: status,
        headers,
        body: status === 304 ? null : JSON.stringify(payload),
      };
      return {
        ok: true,
        status: 200,
        headers: fetchHeaders({}),
        text: async () => JSON.stringify({ ok: true, result: JSON.stringify(result) }),
      };
    }
    return {
      ok: status >= 200 && status < 300,
      status,
      headers: fetchHeaders(headers),
      text: async () => status === 304 ? '' : JSON.stringify(payload),
    };
  }

  const context = {
    console,
    Intl,
    Date,
    JSON,
    Math,
    Number,
    String,
    Array,
    Object,
    Map,
    Set,
    URL,
    URLSearchParams,
    document,
    localStorage,
    location: { pathname: options.pathname || '/v0/management/plugins/usage-statistics/dashboard', host: 'test.local' },
    navigator: { userAgent: 'node-test' },
    window: { innerWidth: 1200, innerHeight: 800 },
    setTimeout(_fn, delay) { timeoutDelays.push(delay); return timeoutDelays.length; },
    clearTimeout() {},
    alert(message) { downloads.push({ alert: message }); },
    fetch: async (url, options = {}) => {
      fetchCalls.push(String(url));
      fetchRequests.push({ url: String(url), options });
      let payload;
      const route = dashboardRoute(url);
      if (String(url).includes('model-prices')) {
        if (options.method === 'PUT') {
          const body = JSON.parse(options.body || '{}');
          prices[body.model] = body.price;
        } else if (options.method === 'DELETE') {
          const parsed = new URL(String(url), 'http://test.local/v0/management/plugins/usage-statistics/dashboard');
          delete prices[parsed.searchParams.get('model')];
        }
        payload = { prices, updated_at: new Date().toISOString(), storage: {} };
      } else if (String(url).includes('dashboard-summary')) {
        summary._meta.last_recorded_at = summaryLastRecordedAt;
        payload = summary;
      }
      else if (String(url).includes('dashboard-api-detail')) payload = apiDetailPayload(String(url));
      else if (String(url).includes('dashboard-events-export')) payload = eventsExport(String(url));
      else if (String(url).includes('dashboard-events')) payload = eventsPage(String(url));
      else if (String(url).includes('usage/export')) payload = { version: 1, usage: {} };
      else payload = {};
      if (typeof payload === 'string') {
        return {
          ok: true,
          status: 200,
          headers: fetchHeaders({ 'Content-Type': ['text/csv; charset=utf-8'] }),
          text: async () => payload,
        };
      }
      return fetchResponse(payload, route, String(url), options);
    },
    Blob: class FakeBlob {
      constructor(parts, options) {
        this.parts = parts;
        this.type = options && options.type;
      }
    },
  };
  if (options.managementKey) {
    localStorage.setItem('cli-proxy-auth', JSON.stringify({ state: { managementKey: options.managementKey } }));
  }
  context.window.document = document;
  context.window.localStorage = localStorage;
  context.URL.createObjectURL = (blob) => {
    const text = blob.parts.map((part) => String(part)).join('');
    downloads.push({ text, type: blob.type });
    return 'blob:fake';
  };
  context.URL.revokeObjectURL = () => {};

  vm.createContext(context);
  const helpers = fs.readFileSync(path.join(__dirname, 'helpers.js'), 'utf8');
  const script = fs.readFileSync(path.join(__dirname, 'script.js'), 'utf8');
  vm.runInContext(helpers + '\n' + script, context, { filename: 'dashboard-bundle.js' });

  const setVisibility = (state) => {
    visibilityState = state;
    (listeners.get('visibilitychange') || []).forEach((handler) => handler());
  };
  const setSummaryLastRecordedAt = (value) => {
    summaryLastRecordedAt = value;
  };

  return { context, document, fetchCalls, fetchRequests, downloads, timeoutDelays, setVisibility, setSummaryLastRecordedAt };
}

async function waitFor(fn) {
  for (let i = 0; i < 50; i++) {
    if (fn()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  throw new Error('condition not met');
}

function optionHeaderValue(options, name) {
  const headers = options && options.headers;
  if (!headers) return '';
  if (typeof headers.get === 'function') return headers.get(name) || headers.get(String(name).toLowerCase()) || '';
  const target = String(name).toLowerCase();
  for (const [key, value] of Object.entries(headers)) {
    if (String(key).toLowerCase() === target) return Array.isArray(value) ? String(value[0] || '') : String(value || '');
  }
  return '';
}

test('dashboard loads summary and export button uses backend event export', async () => {
  const { document, fetchCalls, downloads } = createDashboardHarness();

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events')));
  assert.strictEqual(document.getElementById('totalRequests').textContent, '1,200');
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$0.05');
  assert.strictEqual(document.getElementById('storageStatus').textContent, '未开启持久化');
  const apiDetail = document.getElementById('apiDetail').innerHTML;
  assert.match(apiDetail, /总花费/);
  assert.doesNotMatch(apiDetail, /Token\/请求/);
  await waitFor(() => /ModelError/.test(document.getElementById('apiDetail').innerHTML));
  const loadedApiDetail = document.getElementById('apiDetail').innerHTML;
  assert.match(loadedApiDetail, /US\$0\.00/);
  assert.match(loadedApiDetail, /总 token 数：105/);
  assert.match(loadedApiDetail, /缓存 token：10/);
  assert.match(loadedApiDetail, /思考 token：5/);
  assert.match(document.getElementById('apiDetail').innerHTML, /错误统计/);
  assert.match(document.getElementById('apiDetail').innerHTML, /最近请求/);
  assert.match(document.getElementById('apiDetail').innerHTML, /401/);
  assert.match(document.getElementById('apiDetail').innerHTML, /deepseek-v4-flash-free/);

  const pagedEventsCount = () => fetchCalls.filter((url) => url.includes('dashboard-events?')).length;
  const exportEventsCount = () => fetchCalls.filter((url) => url.includes('dashboard-events-export')).length;
  const beforePagedEvents = pagedEventsCount();
  const beforeExportEvents = exportEventsCount();
  await document.getElementById('exportRowsCsv').onclick();
  await waitFor(() => downloads.some((d) => d.text && d.text.startsWith('时间,模型')));
  await document.getElementById('exportRowsJson').onclick();
  await waitFor(() => downloads.some((d) => d.text && d.text.startsWith('[')));

  assert.strictEqual(pagedEventsCount(), beforePagedEvents);
  assert.strictEqual(exportEventsCount(), beforeExportEvents + 2);
  assert.ok(fetchCalls.some((url) => url.includes('dashboard-events-export') && new URL(url, 'http://test.local').searchParams.get('format') === 'csv'));
  const exported = JSON.parse(downloads.find((d) => d.text && d.text.startsWith('[')).text);
  assert.strictEqual(exported.length, 1200);
});

test('dashboard shows pending storage buffer status', async () => {
  const { document, fetchCalls } = createDashboardHarness({
    storage: {
      enabled: true,
      path: 'usage-statistics.jsonl',
      loaded_path: 'usage-statistics/usage-2026-06-28.jsonl',
      pending_buffered_records: 2,
    },
  });

  const el = document.getElementById('storageStatus');
  await waitFor(() => el.textContent === '持久化待同步');
  assert.strictEqual(el.textContent, '持久化待同步');
  assert.match(el.title, /2 条记录/);
});

test('dashboard shows pending storage write queue status', async () => {
  const { document } = createDashboardHarness({
    storage: {
      enabled: true,
      path: 'usage-statistics.jsonl',
      loaded_path: 'usage-statistics/usage-2026-06-28.jsonl',
      write_queue_length: 5,
      write_queue_capacity: 4096,
      pending_buffered_records: 2,
    },
  });

  const el = document.getElementById('storageStatus');
  await waitFor(() => el.textContent === '持久化排队中');
  assert.strictEqual(el.textContent, '持久化排队中');
  assert.match(el.title, /5 条记录/);
  assert.match(el.title, /4,096/);
});

test('dashboard shows pending storage snapshot status', async () => {
  const { document } = createDashboardHarness({
    storage: {
      enabled: true,
      path: 'usage-statistics.jsonl',
      loaded_path: 'usage-statistics/usage-2026-06-28.jsonl',
      last_flush_at: '2026-06-28T01:00:00Z',
      pending_snapshot_records: 3,
    },
  });

  const el = document.getElementById('storageStatus');
  await waitFor(() => el.textContent === '快照待更新');
  assert.strictEqual(el.textContent, '快照待更新');
  assert.match(el.title, /3 条记录/);
});

test('dashboard shows pending storage fsync status', async () => {
  const { document } = createDashboardHarness({
    storage: {
      enabled: true,
      path: 'usage-statistics.jsonl',
      loaded_path: 'usage-statistics/usage-2026-06-28.jsonl',
      last_flush_at: '2026-06-28T01:00:00Z',
      pending_unsynced_records: 4,
      pending_snapshot_records: 3,
    },
  });

  const el = document.getElementById('storageStatus');
  await waitFor(() => el.textContent === '持久化待落盘');
  assert.strictEqual(el.textContent, '持久化待落盘');
  assert.match(el.title, /4 条记录/);
});

test('dashboard uses a slower polling interval while hidden', async () => {
  const { fetchCalls, timeoutDelays, setVisibility } = createDashboardHarness({ visibilityState: 'hidden' });

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-summary')));
  await waitFor(() => timeoutDelays.includes(300000));
  assert.notStrictEqual(timeoutDelays[timeoutDelays.length - 1], 30000);

  const beforeVisibleFetches = fetchCalls.length;
  setVisibility('visible');
  await waitFor(() => fetchCalls.length > beforeVisibleFetches);
  await waitFor(() => timeoutDelays.includes(30000));
});

test('dashboard polling skips detail requests when no new records arrive', async () => {
  const { document, fetchCalls, setVisibility, setSummaryLastRecordedAt } = createDashboardHarness();
  const countCalls = (part) => fetchCalls.filter((url) => url.includes(part)).length;

  await waitFor(() => countCalls('dashboard-events') > 0 && countCalls('dashboard-api-detail') > 0);
  const beforeSummary = countCalls('dashboard-summary');
  const beforeEvents = countCalls('dashboard-events');
  const beforeApiDetail = countCalls('dashboard-api-detail');

  setVisibility('visible');
  await waitFor(() => countCalls('dashboard-summary') > beforeSummary);
  assert.strictEqual(countCalls('dashboard-events'), beforeEvents);
  assert.strictEqual(countCalls('dashboard-api-detail'), beforeApiDetail);

  setSummaryLastRecordedAt('2023-11-15T06:14:20Z');
  const beforeChangedEvents = countCalls('dashboard-events');
  const beforeChangedApiDetail = countCalls('dashboard-api-detail');
  setVisibility('visible');
  await waitFor(() => countCalls('dashboard-events') > beforeChangedEvents && countCalls('dashboard-api-detail') > beforeChangedApiDetail);

  const beforeManualEvents = countCalls('dashboard-events');
  const beforeManualApiDetail = countCalls('dashboard-api-detail');
  await document.getElementById('refreshBtn').onclick();
  assert.ok(countCalls('dashboard-events') > beforeManualEvents);
  assert.ok(countCalls('dashboard-api-detail') > beforeManualApiDetail);
});

test('dashboard summary polling reuses cached data on management 304', async () => {
  const { fetchCalls, fetchRequests, setVisibility } = createDashboardHarness({
    dashboardEtags: true,
    wrapDashboardResponses: true,
  });
  const summaryRequests = () => fetchRequests.filter((req) => req.url.includes('dashboard-summary'));
  const countCalls = (part) => fetchCalls.filter((url) => url.includes(part)).length;

  await waitFor(() => summaryRequests().length > 0 && countCalls('dashboard-events?') > 0 && countCalls('dashboard-api-detail') > 0);
  assert.strictEqual(optionHeaderValue(summaryRequests()[0].options, 'If-None-Match'), '');

  const beforeSummary = summaryRequests().length;
  const beforeEvents = countCalls('dashboard-events?');
  const beforeApiDetail = countCalls('dashboard-api-detail');
  setVisibility('visible');

  await waitFor(() => summaryRequests().length > beforeSummary);
  const latestSummary = summaryRequests().at(-1);
  assert.strictEqual(optionHeaderValue(latestSummary.options, 'If-None-Match'), 'W/"summary-2023-11-15T06:13:20Z"');
  assert.strictEqual(countCalls('dashboard-events?'), beforeEvents);
  assert.strictEqual(countCalls('dashboard-api-detail'), beforeApiDetail);
});

test('dashboard detail refresh sends conditional requests for events and api detail', async () => {
  const { document, fetchRequests } = createDashboardHarness({
    dashboardEtags: true,
    wrapDashboardResponses: true,
  });
  const eventRequests = () => fetchRequests.filter((req) => req.url.includes('dashboard-events?'));
  const apiDetailRequests = () => fetchRequests.filter((req) => req.url.includes('dashboard-api-detail'));

  await waitFor(() => eventRequests().length > 0 && apiDetailRequests().length > 0);
  const beforeEvents = eventRequests().length;
  const beforeApiDetail = apiDetailRequests().length;
  await document.getElementById('refreshBtn').onclick();

  await waitFor(() => eventRequests().length > beforeEvents && apiDetailRequests().length > beforeApiDetail);
  assert.match(optionHeaderValue(eventRequests().at(-1).options, 'If-None-Match'), /^W\/"dashboard-events-/);
  assert.match(optionHeaderValue(apiDetailRequests().at(-1).options, 'If-None-Match'), /^W\/"dashboard-api-detail-/);
  assert.match(document.getElementById('apiDetail').innerHTML, /最近请求/);
});

test('model price settings are loaded and saved through backend API', async () => {
  const { document, fetchRequests } = createDashboardHarness({
    pathname: '/v0/resource/plugins/usage-statistics/dashboard',
    managementKey: 'test-management-key',
  });

  await waitFor(() => /gpt-4\.1/.test(document.getElementById('priceList').innerHTML));
  assert.match(document.getElementById('priceList').innerHTML, /gpt-4\.1/);

  document.getElementById('priceModel').value = 'gpt-5';
  document.getElementById('pricePrompt').value = '1.25';
  document.getElementById('priceCompletion').value = '10';
  document.getElementById('priceCache').value = '';
  await document.getElementById('savePrice').onclick();

  const put = fetchRequests.find((req) => req.url.includes('model-prices') && req.options.method === 'PUT');
  assert.ok(put, 'expected PUT /model-prices');
  assert.strictEqual(put.url, '/v0/management/plugins/usage-statistics/model-prices');
  assert.strictEqual(put.options.headers.Authorization, 'Bearer test-management-key');
  assert.strictEqual(put.options.headers['x-management-key'], 'test-management-key');
  assert.deepStrictEqual(JSON.parse(put.options.body), {
    model: 'gpt-5',
    price: { prompt: 1.25, completion: 10, cache: 1.25 },
  });
  assert.match(document.getElementById('priceList').innerHTML, /gpt-5/);
});

test('event list is not implicitly filtered by selected upstream API', async () => {
  const { document, fetchCalls } = createDashboardHarness();

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events')));
  const isEventsCall = (url) => url.includes('dashboard-events');
  const isApiDetailCall = (url) => url.includes('dashboard-api-detail');
  const hasApiFilter = (url) => new URL(url, 'http://test.local').searchParams.has('api');
  const globalEventsCount = () => fetchCalls.filter((url) => isEventsCall(url) && !hasApiFilter(url)).length;
  const apiDetailCount = () => fetchCalls.filter(isApiDetailCall).length;
  const firstEventsCall = fetchCalls.find((url) => isEventsCall(url) && !hasApiFilter(url));
  assert.strictEqual(new URL(firstEventsCall, 'http://test.local').searchParams.get('api'), null);
  await waitFor(() => apiDetailCount() > 0);

  const beforeGlobal = globalEventsCount();
  const beforeApiDetail = apiDetailCount();
  document.getElementById('apiSelect').onchange();
  await waitFor(() => apiDetailCount() > beforeApiDetail);
  assert.strictEqual(
    globalEventsCount(),
    beforeGlobal,
    'changing upstream API detail selection should not reload event list'
  );

  document.getElementById('filterModel').value = 'gpt-4.1';
  await document.getElementById('filterModel').onchange();
  await waitFor(() => globalEventsCount() > beforeGlobal);
  const latestEventsCall = fetchCalls.filter((url) => isEventsCall(url) && !hasApiFilter(url)).at(-1);
  const params = new URL(latestEventsCall, 'http://test.local').searchParams;
  assert.strictEqual(params.get('model'), 'gpt-4.1');
  assert.strictEqual(params.get('api'), null);
});
