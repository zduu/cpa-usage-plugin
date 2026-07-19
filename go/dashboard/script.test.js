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
    this.children = [];
    this.parentNode = null;
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
  appendChild(child) {
    child.parentNode = this;
    this.children.push(child);
    return child;
  }
  removeChild(child) {
    const index = this.children.indexOf(child);
    if (index >= 0) this.children.splice(index, 1);
    child.parentNode = null;
    return child;
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
  const windowListeners = new Map();
  let visibilityState = options.visibilityState || 'visible';
  const sortButtons = ['requests', 'tokens', 'cost'].map((name) => {
    const el = new FakeElement('sort-' + name);
    el.dataset.apiSort = name;
    return el;
  });
  const clientApiSelectButton = new FakeElement('client-api-select');
  const downloads = [];
  const fetchCalls = [];
  const fetchRequests = [];
  const timeoutDelays = [];
  let summaryLastRecordedAt = options.lastRecordedAt || '2023-11-15T06:13:20Z';
  let summaryVersion = options.summaryVersion || 1;
  let prices = options.prices || { 'gpt-4.1': { prompt: 2, completion: 8, cache: 0.5, cache_write: 0.5 } };
  let manualPrices = options.manualPrices;
  const dashboardEtags = !!options.dashboardEtags;
  const wrapDashboardResponses = !!options.wrapDashboardResponses;
  const emptyConditionalEtagOk = !!options.emptyConditionalEtagOk;
  const failDashboardSummary = !!options.failDashboardSummary;
  const failFilteredDashboardSummary = !!options.failFilteredDashboardSummary;
  let failDashboardEvents = !!options.failDashboardEvents;
  const failModelPrices = !!options.failModelPrices;
  const forceSummaryNotModified = !!options.forceSummaryNotModified;
  const nullDashboardSummary = !!options.nullDashboardSummary;
  const nullDashboardData = !!options.nullDashboardData;
  const nullDashboardApiDetail = !!options.nullDashboardApiDetail;
  const filteredSummaryPayload = options.filteredSummary;
  const apiFailureCount = Number.isFinite(Number(options.apiFailureCount)) ? Number(options.apiFailureCount) : 10;
  const exportJobs = new Map();
  let exportJobSeq = 0;

  const document = {
    body: new FakeElement('body'),
    documentElement: new FakeElement('html'),
    get visibilityState() {
      return visibilityState;
    },
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    querySelectorAll(selector) {
      if (selector === '[data-api-sort]') return sortButtons;
      if (selector === '[data-client-api-select]') return [clientApiSelectButton];
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
  if (options.language) localStorage.setItem('cli-proxy-language', options.language);
  if (options.range) localStorage.setItem('cpa-usage-range-v1', options.range);

  const summary = {
    generated_at: options.generatedAt || new Date().toISOString(),
    usage: {
      total_requests: 1200,
      success_count: 1190,
      failure_count: 10,
      total_tokens: 24000,
      cached_tokens: 100,
      cache_write_tokens: 25,
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
      summary_version: summaryVersion,
      current_detail_count: 1200,
      current_hour: Number.isFinite(Number(options.currentHour)) ? Number(options.currentHour) : new Date(options.generatedAt || Date.now()).getHours(),
      storage: { enabled: false, path: 'usage-statistics.jsonl' },
    },
  };
  summary.usage.failure_count = apiFailureCount;
  summary.usage.success_count = summary.usage.total_requests - apiFailureCount;
  summary.usage.apis.openai.failure_count = apiFailureCount;
  summary.usage.apis.openai.success_count = summary.usage.apis.openai.total_requests - apiFailureCount;
  summary.source_stats[0].failure_count = apiFailureCount;
  summary.source_stats[0].success_count = summary.source_stats[0].total_requests - apiFailureCount;
  summary.model_stats[0].failure_count = apiFailureCount;
  summary.model_stats[0].success_count = summary.model_stats[0].total_requests - apiFailureCount;
  if (options.storage) summary._meta.storage = options.storage;
  if (options.clientApiStats) summary.client_api_stats = options.clientApiStats;
  if (options.credentialStats) summary.credential_stats = options.credentialStats;
  if (options.summaryUsage) Object.assign(summary.usage, options.summaryUsage);

  function eventsPage(url) {
    const parsed = new URL(url, 'http://test.local/v0/management/plugins/usage-dashboard-zduu/dashboard');
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
          ttft_ms: 35,
          tokens: { input_tokens: 10, output_tokens: 5, cached_tokens: 7, cache_write_tokens: 2, total_tokens: 15 },
        };
      }),
    };
  }

  function eventsExport(url) {
    const parsed = new URL(url, 'http://test.local/v0/management/plugins/usage-dashboard-zduu/dashboard');
    const api = parsed.searchParams.get('api');
    const totalRows = api ? 8 : 1200;
    if (parsed.searchParams.get('format') === 'csv') {
      return '时间,模型,来源,凭证,结果,延迟毫秒,TTFT毫秒,非缓存输入 token,输出 token,思考 token,缓存 token,缓存写入 token,总 token,状态码,错误\n' +
        eventsPage('http://test.local/dashboard-events?limit=' + totalRows + '&offset=0').events.slice(0, totalRows)
          .map((event) => [event.timestamp, event.model, event.source, event.auth_index, event.failed ? '失败' : '成功', event.latency_ms, '', 1, event.tokens.output_tokens, '', 7, 2, event.tokens.total_tokens, '', ''].join(','))
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

  function exportJobHeaders(payload) {
    const total = payload && typeof payload === 'object' ? payload.total : 1200;
    const exported = payload && typeof payload === 'object' && Array.isArray(payload.events) ? payload.events.length : total;
    return {
      'Content-Type': [typeof payload === 'string' ? 'text/csv; charset=utf-8' : 'application/json; charset=utf-8'],
      'X-Total-Count': [String(total)],
      'X-Exported-Count': [String(exported)],
      'X-Export-Truncated': ['false'],
    };
  }

  function exportJobResponse(job) {
    return {
      id: job.id,
      status: job.status,
      format: job.format,
      gzip: false,
      created_at: new Date().toISOString(),
      finished_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 900000).toISOString(),
      total: job.total,
      exported: job.exported,
      truncated: false,
      body_bytes: typeof job.payload === 'string' ? job.payload.length : JSON.stringify(job.payload).length,
      content_type: job.headers['Content-Type'][0],
      download_path: '/dashboard-events-export-download?id=' + job.id,
    };
  }

  function createExportJob(url) {
    const parsed = new URL(url, 'http://test.local/v0/management/plugins/usage-dashboard-zduu/dashboard');
    const payload = eventsExport(url);
    const id = 'job-' + (++exportJobSeq);
    const job = {
      id,
      status: 'succeeded',
      format: parsed.searchParams.get('format') || 'json',
      payload,
      headers: exportJobHeaders(payload),
      total: payload && typeof payload === 'object' ? payload.total : 1200,
      exported: payload && typeof payload === 'object' && Array.isArray(payload.events) ? payload.events.length : 1200,
    };
    exportJobs.set(id, job);
    return exportJobResponse(job);
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
        cache_write_tokens: 3,
        reasoning_tokens: 5,
        avg_latency_ms: 113,
      },
      model_stats: [
        { model: 'gpt-4.1', total_requests: 7, success_count: 7, failure_count: 0, total_tokens: 105, input_tokens: 70, output_tokens: 35, cached_tokens: 10, cache_write_tokens: 3, reasoning_tokens: 5 },
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
          ttft_ms: failed ? 0 : 40,
          tokens: failed ? { total_tokens: 0 } : { input_tokens: 10, output_tokens: 5, total_tokens: 15 },
        };
      }),
      total_events: 8,
      generated_at: new Date().toISOString(),
    };
  }

  function dashboardDataPayload() {
    const now = Number.isFinite(Number(options.dashboardDataNowMs)) ? Number(options.dashboardDataNowMs) : Date.now();
    const dashboardDataDetailModel = options.dashboardDataDetailModel || 'gpt-4.1';
    const dashboardDataModelKey = options.dashboardDataModelKey || dashboardDataDetailModel;
    const aggregateRequests = options.trimmedDashboardData ? 4 : 2;
    const aggregateSuccess = options.trimmedDashboardData ? 3 : 1;
    const aggregateTokens = options.trimmedDashboardData ? 45 : 15;
    const aggregateInput = options.trimmedDashboardData ? 30 : 10;
    const aggregateOutput = options.trimmedDashboardData ? 15 : 5;
    const aggregateCached = options.trimmedDashboardData ? 2 : 0;
    const aggregateCacheWrite = options.trimmedDashboardData ? 1 : 0;
    const aggregateReasoning = options.trimmedDashboardData ? 4 : 0;
    const aggregateLatency = options.trimmedDashboardData ? 110 : 100;
    const details = [
      {
        timestamp: options.dashboardDataRecentTimestamp || new Date(now - 5 * 60 * 1000).toISOString(),
        model: dashboardDataDetailModel,
        source: 'openai-prod',
        provider: 'openai',
        auth_index: 'auth-1',
        failed: false,
        latency_ms: 120,
        tokens: { input_tokens: 10, output_tokens: 5, total_tokens: 15 },
      },
      {
        timestamp: new Date(now - 10 * 60 * 1000).toISOString(),
        model: dashboardDataDetailModel,
        source: 'openai-prod',
        provider: 'openai',
        auth_index: 'auth-1',
        failed: true,
        status_code: 429,
        failure: 'rate limited',
        latency_ms: 80,
        tokens: { total_tokens: 0 },
      },
    ];
    if (options.dashboardDataOldDetailHours) {
      details[1].timestamp = new Date(now - options.dashboardDataOldDetailHours * 60 * 60 * 1000).toISOString();
    }
    return {
      generated_at: options.dashboardDataGeneratedAt || new Date(now).toISOString(),
      usage: {
        total_requests: aggregateRequests,
        success_count: aggregateSuccess,
        failure_count: 1,
        total_tokens: aggregateTokens,
        input_tokens: aggregateInput,
        output_tokens: aggregateOutput,
        cached_tokens: aggregateCached,
        cache_write_tokens: aggregateCacheWrite,
        reasoning_tokens: aggregateReasoning,
        avg_latency_ms: aggregateLatency,
        requests_by_day: {},
        requests_by_hour: {},
        tokens_by_day: {},
        tokens_by_hour: {},
        apis: {
          openai: {
            total_requests: aggregateRequests,
            success_count: aggregateSuccess,
            failure_count: 1,
            total_tokens: aggregateTokens,
            input_tokens: aggregateInput,
            output_tokens: aggregateOutput,
            cached_tokens: aggregateCached,
            cache_write_tokens: aggregateCacheWrite,
            reasoning_tokens: aggregateReasoning,
            avg_latency_ms: aggregateLatency,
            models: {
              [dashboardDataModelKey]: {
                total_requests: aggregateRequests,
                success_count: aggregateSuccess,
                failure_count: 1,
                total_tokens: aggregateTokens,
                input_tokens: aggregateInput,
                output_tokens: aggregateOutput,
                cached_tokens: aggregateCached,
                cache_write_tokens: aggregateCacheWrite,
                reasoning_tokens: aggregateReasoning,
                avg_latency_ms: aggregateLatency,
                providers: options.dashboardDataProviders,
                details,
              },
            },
          },
        },
      },
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
    if (forceSummaryNotModified && route === 'dashboard-summary' && !String(url).includes('_ts=')) {
      status = 304;
    }
    if (dashboardEtags && route) {
      const etag = dashboardEtag(route, url);
      headers.ETag = [etag];
      if (requestHeaderValue(requestOptions, 'If-None-Match') === etag) status = 304;
    }
    if (emptyConditionalEtagOk && status === 304) {
      return {
        ok: true,
        status: 200,
        headers: fetchHeaders(headers),
        text: async () => '',
      };
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
    location: { pathname: options.pathname || '/v0/management/plugins/usage-dashboard-zduu/dashboard', host: 'test.local' },
    navigator: { userAgent: 'node-test', language: options.navigatorLanguage || 'zh-CN' },
    window: { innerWidth: 1200, innerHeight: 800 },
    setTimeout(_fn, delay) { timeoutDelays.push(delay); return timeoutDelays.length; },
    clearTimeout() {},
    setInterval() { return 1; },
    clearInterval() {},
    alert(message) { downloads.push({ alert: message }); },
    fetch: async (url, options = {}) => {
      fetchCalls.push(String(url));
      fetchRequests.push({ url: String(url), options });
      let payload;
      const route = dashboardRoute(url);
      if (String(url).includes('model-prices')) {
        if (failModelPrices) {
          return {
            ok: false,
            status: 503,
            headers: fetchHeaders({}),
            text: async () => 'prices failed',
          };
        }
        if (options.method === 'PUT') {
          const body = JSON.parse(options.body || '{}');
          prices[body.model] = body.price;
        } else if (options.method === 'DELETE') {
          const parsed = new URL(String(url), 'http://test.local/v0/management/plugins/usage-dashboard-zduu/dashboard');
          delete prices[parsed.searchParams.get('model')];
        }
        payload = { prices, updated_at: new Date().toISOString(), storage: {} };
        if (manualPrices) payload.manual_prices = manualPrices;
      } else if (String(url).includes('dashboard-summary')) {
        const parsed = new URL(String(url), 'http://test.local/v0/management/plugins/usage-dashboard-zduu/dashboard');
        if (failDashboardSummary || (failFilteredDashboardSummary && parsed.searchParams.has('client_api'))) {
          return {
            ok: false,
            status: 500,
            headers: fetchHeaders({}),
            text: async () => 'summary failed',
          };
        }
        summary._meta.last_recorded_at = summaryLastRecordedAt;
        summary._meta.summary_version = summaryVersion;
        payload = nullDashboardSummary ? null : (parsed.searchParams.has('client_api') && filteredSummaryPayload ? filteredSummaryPayload : summary);
      }
      else if (String(url).includes('dashboard-api-detail')) payload = nullDashboardApiDetail ? null : apiDetailPayload(String(url));
      else if (String(url).includes('dashboard-data')) payload = nullDashboardData ? null : dashboardDataPayload();
      else if (String(url).includes('dashboard-events-export-download')) {
        const parsed = new URL(String(url), 'http://test.local/v0/management/plugins/usage-dashboard-zduu/dashboard');
        const job = exportJobs.get(parsed.searchParams.get('id'));
        payload = job ? job.payload : {};
        if (typeof payload === 'string') {
          return {
            ok: true,
            status: 200,
            headers: fetchHeaders(job.headers),
            text: async () => payload,
          };
        }
        return fetchResponse(payload, route, String(url), options);
      }
      else if (String(url).includes('dashboard-events-export-jobs')) {
        const parsed = new URL(String(url), 'http://test.local/v0/management/plugins/usage-dashboard-zduu/dashboard');
        if (options.method === 'POST') {
          payload = createExportJob(String(url));
        } else if (options.method === 'DELETE') {
          exportJobs.delete(parsed.searchParams.get('id'));
          payload = { status: 'deleted' };
        } else {
          const job = exportJobs.get(parsed.searchParams.get('id'));
          payload = job ? exportJobResponse(job) : { error: 'not found' };
        }
      }
      else if (String(url).includes('dashboard-events-export')) payload = eventsExport(String(url));
      else if (String(url).includes('dashboard-events')) {
        if (failDashboardEvents) {
          return {
            ok: false,
            status: 503,
            headers: fetchHeaders({}),
            text: async () => 'events failed',
          };
        }
        payload = eventsPage(String(url));
      }
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
  context.window.parent = context.window;
  context.window.navigator = context.navigator;
  context.window.location = context.location;
  context.window.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  context.window.addEventListener = (type, handler) => {
    const handlers = windowListeners.get(type) || [];
    handlers.push(handler);
    windowListeners.set(type, handlers);
  };
  context.URL.createObjectURL = (blob) => {
    const text = blob.parts.map((part) => String(part)).join('');
    downloads.push({ text, type: blob.type });
    return 'blob:fake';
  };
  context.URL.revokeObjectURL = () => {};

  vm.createContext(context);
  const i18n = fs.readFileSync(path.join(__dirname, 'i18n.js'), 'utf8');
  const helpers = fs.readFileSync(path.join(__dirname, 'helpers.js'), 'utf8');
  const script = fs.readFileSync(path.join(__dirname, 'script.js'), 'utf8');
  vm.runInContext(i18n + '\n' + helpers + '\n' + script, context, { filename: 'dashboard-bundle.js' });

  const setVisibility = (state) => {
    visibilityState = state;
    (listeners.get('visibilitychange') || []).forEach((handler) => handler());
  };
  const setLanguage = (lang, options = {}) => {
    const value = options.persisted ? JSON.stringify({ state: { language: lang }, version: 0 }) : lang;
    localStorage.setItem('cli-proxy-language', value);
    (windowListeners.get('storage') || []).forEach((handler) => handler({ key: 'cli-proxy-language', newValue: value }));
  };
  const setSummaryLastRecordedAt = (value) => {
    summaryLastRecordedAt = value;
  };
  const setSummaryVersion = (value) => {
    summaryVersion = value;
  };
  const setDashboardEventsFailure = (value) => {
    failDashboardEvents = !!value;
  };

  return { context, document, fetchCalls, fetchRequests, downloads, timeoutDelays, setVisibility, setLanguage, setSummaryLastRecordedAt, setSummaryVersion, setDashboardEventsFailure };
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
  const { document, fetchCalls, fetchRequests, downloads } = createDashboardHarness();

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events')));
  assert.strictEqual(document.getElementById('totalRequests').textContent, '1,200');
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$0.05');
  assert.strictEqual(document.getElementById('cacheWriteText').textContent, '缓存创建 token：25');
  assert.strictEqual(document.getElementById('storageStatus').textContent, '未开启持久化');
  const eventsTable = document.getElementById('events').innerHTML;
  assert.match(eventsTable, /缓存命中/);
  assert.match(eventsTable, /缓存创建/);
  assert.match(eventsTable, /用时 \/ 首字/);
  assert.match(eventsTable, /120ms \/ 35ms/);
  assert.match(eventsTable, /<td>1<\/td><td>5<\/td><td>0<\/td><td>7<\/td><td>2<\/td><td>15<\/td>/);
  assert.match(eventsTable, /<td>7<\/td><td>2<\/td><td>15<\/td>/);
  const apiDetail = document.getElementById('apiDetail').innerHTML;
  assert.match(apiDetail, /总花费/);
  assert.doesNotMatch(apiDetail, /Token\/请求/);
  await waitFor(() => /ModelError/.test(document.getElementById('apiDetail').innerHTML));
  const loadedApiDetail = document.getElementById('apiDetail').innerHTML;
  assert.match(loadedApiDetail, /US\$0\.000405/);
  assert.match(loadedApiDetail, /总 token 数：105/);
  assert.match(loadedApiDetail, /缓存命中 token：10/);
  assert.match(loadedApiDetail, /缓存创建 token：3/);
  assert.match(loadedApiDetail, /思考 token：5/);
  assert.match(document.getElementById('apiDetail').innerHTML, /错误统计/);
  assert.match(document.getElementById('apiDetail').innerHTML, /最近请求/);
  assert.match(document.getElementById('apiDetail').innerHTML, /用时 \/ 首字/);
  assert.match(document.getElementById('apiDetail').innerHTML, /120ms \/ 40ms/);
  assert.match(document.getElementById('apiDetail').innerHTML, /64ms \/ -/);
  assert.match(document.getElementById('apiDetail').innerHTML, /401/);
  assert.match(document.getElementById('apiDetail').innerHTML, /deepseek-v4-flash-free/);
  assert.match(document.getElementById('apiDetail').innerHTML, /class="splitGrid detailActivityGrid"/);

  const pagedEventsCount = () => fetchCalls.filter((url) => url.includes('dashboard-events?')).length;
  const exportJobCreateCount = () => fetchRequests.filter((request) => request.url.includes('dashboard-events-export-jobs') && request.options.method === 'POST').length;
  const syncExportEventsCount = () => fetchCalls.filter((url) => /dashboard-events-export\?/.test(url)).length;
  const exportDownloadCount = () => fetchCalls.filter((url) => url.includes('dashboard-events-export-download')).length;
  const beforePagedEvents = pagedEventsCount();
  const beforeExportJobs = exportJobCreateCount();
  const beforeSyncExports = syncExportEventsCount();
  const beforeExportDownloads = exportDownloadCount();
  await document.getElementById('exportRowsCsv').onclick();
  await waitFor(() => downloads.some((d) => d.text && d.text.startsWith('时间,模型')));
  await document.getElementById('exportRowsJson').onclick();
  await waitFor(() => downloads.some((d) => d.text && d.text.startsWith('[')));

  assert.strictEqual(pagedEventsCount(), beforePagedEvents);
  assert.strictEqual(exportJobCreateCount(), beforeExportJobs + 2);
  assert.strictEqual(syncExportEventsCount(), beforeSyncExports);
  assert.strictEqual(exportDownloadCount(), beforeExportDownloads + 2);
  assert.ok(fetchRequests.some((request) => request.url.includes('dashboard-events-export-jobs') && request.options.method === 'POST' && new URL(request.url, 'http://test.local').searchParams.get('format') === 'csv'));
  const csvExport = downloads.find((d) => d.text && d.text.startsWith('时间,模型'));
  assert.match(csvExport.text, /非缓存输入 token/);
  assert.match(csvExport.text, /,1,5,,7,2,15,/);
  const exported = JSON.parse(downloads.find((d) => d.text && d.text.startsWith('[')).text);
  assert.strictEqual(exported.length, 1200);
});

test('dashboard blob downloads keep object URLs alive for Safari', () => {
  const { context, document, downloads, timeoutDelays } = createDashboardHarness();
  context.download('usage.csv', 'a,b\n1,2', 'text/csv;charset=utf-8');

  assert.ok(downloads.some((d) => d.text === 'a,b\n1,2' && d.type === 'text/csv;charset=utf-8'));
  assert.ok(document.body.children.some((child) => child.download === 'usage.csv' && child.href === 'blob:fake'));
  assert.strictEqual(timeoutDelays.at(-1), 60000);
});

test('dashboard fallback merges legacy hashless client API stats into a unique hashed group', () => {
  const { context } = createDashboardHarness();
  const rows = context.coalesceLegacyHashlessClientApiStats([
    {
      api_key: 'sk******xx',
      api_key_hash: '',
      total_requests: 1,
      success_count: 1,
      failure_count: 0,
      total_tokens: 120,
      input_tokens: 100,
      output_tokens: 20,
      models: [{ model: 'gpt-4.1', total_requests: 1, success_count: 1, failure_count: 0, total_tokens: 120, input_tokens: 100, output_tokens: 20 }],
    },
    {
      api_key: 'sk******xx',
      api_key_hash: 'hash-a',
      total_requests: 1,
      success_count: 1,
      failure_count: 0,
      total_tokens: 40,
      input_tokens: 30,
      output_tokens: 10,
      models: [{ model: 'gpt-4.1', total_requests: 1, success_count: 1, failure_count: 0, total_tokens: 40, input_tokens: 30, output_tokens: 10 }],
    },
  ]);

  assert.strictEqual(rows.length, 1);
  assert.strictEqual(rows[0].api_key_hash, 'hash-a');
  assert.strictEqual(rows[0].total_requests, 2);
  assert.strictEqual(rows[0].total_tokens, 160);
  assert.strictEqual(rows[0].models[0].total_requests, 2);
});

test('dashboard fallback merges imported hashless client API group with historical hashes', () => {
  const { context } = createDashboardHarness();
  const rows = context.coalesceLegacyHashlessClientApiStats([
    { api_key: 'sk******xx', api_key_hash: '', total_requests: 1, total_tokens: 40, models: [] },
    { api_key: 'sk******xx', api_key_hash: 'hash-a', total_requests: 1, total_tokens: 120, models: [] },
    { api_key: 'sk******xx', api_key_hash: 'hash-b', total_requests: 1, total_tokens: 60, models: [] },
  ]);

  assert.strictEqual(rows.length, 1);
  assert.strictEqual(rows[0].api_key_hash, '');
  assert.strictEqual(rows[0].total_requests, 3);
  assert.strictEqual(rows[0].total_tokens, 220);
});

test('dashboard fallback keeps different live hashes separate without an imported hashless group', () => {
  const { context } = createDashboardHarness();
  const rows = context.coalesceLegacyHashlessClientApiStats([
    { api_key: 'sk******xx', api_key_hash: 'hash-a', total_requests: 1, total_tokens: 120, models: [] },
    { api_key: 'sk******xx', api_key_hash: 'hash-b', total_requests: 1, total_tokens: 60, models: [] },
  ]);

  assert.strictEqual(rows.length, 2);
  assert.deepStrictEqual(rows.map((row) => row.total_tokens), [120, 60]);
});

test('dashboard credential filter uses summary credential stats beyond current event page', async () => {
  const { document } = createDashboardHarness({
    credentialStats: [
      { auth_index: 'auth-old', total_requests: 1, success_count: 1, failure_count: 0, total_tokens: 15 },
      { auth_index: 'auth-1', total_requests: 1200, success_count: 1190, failure_count: 10, total_tokens: 24000 },
      { auth_index: '(空)', total_requests: 2, success_count: 2, failure_count: 0, total_tokens: 20 },
    ],
  });
  await waitFor(() => document.getElementById('filterAuth').innerHTML.includes('auth-old'));

  const html = document.getElementById('filterAuth').innerHTML;
  assert.match(html, /auth-old/);
  assert.match(html, /auth-1/);
  assert.doesNotMatch(html, /\(空\)/);
});

test('dashboard follows runtime language changes', async () => {
  const { document, setLanguage } = createDashboardHarness();

  await waitFor(() => document.getElementById('eventsCount').textContent.includes('共'));
  setLanguage('en', { persisted: true });

  await waitFor(() => document.documentElement.lang === 'en');
  await waitFor(() => document.getElementById('successText').textContent.includes('Success requests:'));
  await waitFor(() => document.getElementById('eventsCount').textContent.includes('Total 1,200, showing 500'));
  await waitFor(() => document.getElementById('modelStats').innerHTML.includes('Success Rate'));
  await waitFor(() => document.getElementById('priceList').innerHTML.includes('Edit'));
  await waitFor(() => document.getElementById('updated').textContent.includes('Updated at:'));

  assert.strictEqual(document.documentElement.lang, 'en');
  assert.strictEqual(document.getElementById('rpmMeta').textContent, 'Last hour requests: 1,200');
  assert.strictEqual(document.getElementById('costMeta').textContent, 'Total tokens: 24k');
  assert.strictEqual(document.getElementById('apiDetailTitle').textContent, 'openai');
  assert.match(document.getElementById('storageStatus').textContent, /Storage disabled/);
  assert.match(document.getElementById('trendMetric').innerHTML, /Daily Cost/);
});

test('dashboard language changes do not translate API key labels', async () => {
  const apiLabel = '成功模型凭证';
  const { document, setLanguage } = createDashboardHarness({
    clientApiStats: [{
      api_key: apiLabel,
      total_requests: 1296,
      success_count: 1230,
      failure_count: 66,
      total_tokens: 99,
      models: [],
    }],
  });

  await waitFor(() => document.getElementById('clientApiStats').innerHTML.includes(apiLabel));
  setLanguage('en');

  await waitFor(() => document.getElementById('clientApiStats').innerHTML.includes('Requests'));
  assert.match(document.getElementById('clientApiStats').innerHTML, /<div class="apiName">成功模型凭证<\/div>/);
  assert.doesNotMatch(document.getElementById('clientApiStats').innerHTML, /apiArrow|▶/);
  assert.match(document.getElementById('clientApiStats').innerHTML, /<span class="ok">1,230<\/span>&nbsp;<span class="bad">66<\/span>/);
});

test('dashboard trend chart escapes data labels', async () => {
  const maliciousDay = '2026-07-03<script>alert(1)</script>';
  const { document } = createDashboardHarness({
    range: '7d',
    summaryUsage: {
      requests_by_day: { [maliciousDay]: 3 },
      tokens_by_day: { [maliciousDay]: 30 },
      cost_by_day: { [maliciousDay]: 0.00003 },
      requests_by_hour: {},
      tokens_by_hour: {},
      cost_by_hour: {},
    },
  });

  await waitFor(() => document.getElementById('trendChart').innerHTML.includes('&lt;script&gt;'));
  const html = document.getElementById('trendChart').innerHTML;
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
});

test('dashboard trend chart does not show anomaly spike banner', async () => {
  const costByDay = {};
  for (let i = 1; i <= 6; i++) costByDay['2026-06-0' + i] = 1;
  for (let i = 7; i <= 9; i++) costByDay['2026-06-0' + i] = 10;
  const { document } = createDashboardHarness({
    range: 'all',
    summaryUsage: {
      requests_by_day: Object.fromEntries(Object.keys(costByDay).map((day) => [day, costByDay[day]])),
      tokens_by_day: Object.fromEntries(Object.keys(costByDay).map((day) => [day, costByDay[day] * 100])),
      cost_by_day: costByDay,
    },
  });

  await waitFor(() => document.getElementById('trendChart').innerHTML.includes('06-09'));

  assert.strictEqual(document.getElementById('anomalyBar').className, 'anomalyBar');
  assert.strictEqual(document.getElementById('anomalyBar').innerHTML, '');
});

test('dashboard hourly trend rotates midnight to the end of the timeline', async () => {
  const { document } = createDashboardHarness({
    range: '24h',
    generatedAt: '2026-01-02T16:30:00Z',
    currentHour: 0,
    summaryUsage: {
      requests_by_hour: { '00': 2, '17': 1, '18': 1, '19': 1, '20': 1, '21': 1, '22': 1, '23': 1 },
      tokens_by_hour: { '00': 20, '17': 10, '18': 10, '19': 10, '20': 10, '21': 10, '22': 10, '23': 10 },
      cost_by_hour: {},
    },
  });

  await waitFor(() => document.getElementById('trendChart').innerHTML.includes('00:00'));
  const html = document.getElementById('trendChart').innerHTML;

  assert.ok(html.indexOf('17:00') < html.indexOf('23:00'), html);
  assert.ok(html.indexOf('23:00') < html.indexOf('00:00'), html);
});

test('dashboard api detail export buttons create filtered export jobs', async () => {
  const { document, fetchRequests, downloads } = createDashboardHarness();

  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  await document.getElementById('exportApiCsv').onclick();
  await waitFor(() => downloads.some((d) => d.text && d.text.startsWith('时间,模型')));
  await document.getElementById('exportApiJson').onclick();
  await waitFor(() => downloads.some((d) => d.text && d.text.startsWith('[')));

  const creates = fetchRequests.filter((request) => request.url.includes('dashboard-events-export-jobs') && request.options.method === 'POST');
  const apiCreates = creates.filter((request) => new URL(request.url, 'http://test.local').searchParams.get('api') === 'openai');
  assert.strictEqual(apiCreates.length, 2);
  assert.deepStrictEqual(
    apiCreates.map((request) => new URL(request.url, 'http://test.local').searchParams.get('format')).sort(),
    ['csv', 'json']
  );
  assert.ok(downloads.some((d) => d.text && d.text.startsWith('[') && JSON.parse(d.text).length === 8));
  assert.strictEqual(downloads.some((d) => d.alert === '导出失败'), false);
});

test('dashboard api detail export uses management endpoints from management shell', async () => {
  const { document, fetchRequests, downloads } = createDashboardHarness({ pathname: '/management.html', managementKey: 'test-management-key' });

  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  await document.getElementById('exportApiCsv').onclick();
  await waitFor(() => downloads.some((d) => d.text && d.text.startsWith('时间,模型')));

  const create = fetchRequests.find((request) => request.url.includes('dashboard-events-export-jobs') && request.options.method === 'POST');
  assert.ok(create, 'expected an export job create request');
  assert.match(create.url, /^\/v0\/management\/plugins\/usage-dashboard-zduu\/dashboard-events-export-jobs\?/);
  assert.strictEqual(create.options.headers.Authorization, 'Bearer test-management-key');
  assert.strictEqual(create.options.headers['x-management-key'], 'test-management-key');
});

test('dashboard api detail export uses management endpoints from resource iframe', async () => {
  const { document, fetchRequests, downloads } = createDashboardHarness({ pathname: '/v0/resource/plugins/usage-dashboard-zduu/dashboard', managementKey: 'test-management-key' });

  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  await document.getElementById('exportApiJson').onclick();
  await waitFor(() => downloads.some((d) => d.text && d.text.startsWith('[')));

  const creates = fetchRequests.filter((request) => request.url.includes('dashboard-events-export-jobs') && request.options.method === 'POST');
  const downloadsReq = fetchRequests.filter((request) => request.url.includes('dashboard-events-export-download'));
  assert.ok(creates.length > 0, 'expected an export job create request');
  assert.ok(downloadsReq.length > 0, 'expected an export job download request');
  assert.match(creates[0].url, /^\/v0\/management\/plugins\/usage-dashboard-zduu\/dashboard-events-export-jobs\?/);
  assert.match(downloadsReq[0].url, /^\/v0\/management\/plugins\/usage-dashboard-zduu\/dashboard-events-export-download\?/);
});

test('dashboard price form shows models.dev effective prices', async () => {
  const { document } = createDashboardHarness({
    prices: { 'gpt-4.1': { prompt: 1.25, completion: 10, cache: 0.125, cache_write: 1.5 } },
    manualPrices: {},
  });

  await waitFor(() => document.getElementById('priceModelOptions').innerHTML.includes('gpt-4.1'));
  document.getElementById('priceModel').value = 'gpt-4.1';
  document.getElementById('priceModel').onchange();

  assert.strictEqual(document.getElementById('pricePrompt').value, 1.25);
  assert.strictEqual(document.getElementById('priceCompletion').value, 10);
  assert.strictEqual(document.getElementById('priceCache').value, 0.125);
  assert.strictEqual(document.getElementById('priceCacheWrite').value, 1.5);
});

test('dashboard export truncation headers produce a user notice', () => {
  const { context, downloads } = createDashboardHarness();
  const info = context.exportTruncationFromHeaders({
    'X-Export-Truncated': ['true'],
    'X-Total-Count': ['1200'],
    'X-Exported-Count': ['500'],
  });

  context.notifyExportTruncated(info);

  assert.deepStrictEqual(downloads.find((d) => d.alert), { alert: '导出已截断：共 1,200 条，已导出 500 条' });
});

test('dashboard api detail renders long error and source cells with safe wrappers', () => {
  const { context } = createDashboardHarness();
  const errorHtml = context.apiDetailErrorHtml([
    {
      status_code: 520,
      count: 1,
      failure: '<!DOCTYPE html> <html class="no-js" lang="en-US"><head><title>example.invalid | 520</title></head>',
    },
  ], false, null);
  const recentHtml = context.apiDetailRecentHtml([
    {
      timestamp_ms: Date.UTC(2026, 5, 29, 4, 56, 2),
      model: 'deepseek-v4-pro',
      failed: false,
      latency_ms: 4520,
      ttft_ms: 810,
      total_tokens: 48035,
      source: 'openai-compatible-example-go',
      provider: 'openai-compatible-example-go',
    },
  ], false, null);
  const barsHtml = context.barsHtml('来源分布', [{
    name: 'xpspwc9mfb@privaterelay.appleid.com-extra-long-credential-name',
    requests: 8,
  }], 8, '暂无来源数据');

  assert.match(errorHtml, /<td><span class="errorText">&lt;!DOCTYPE html&gt;/);
  assert.doesNotMatch(errorHtml, /<td class="errorText">/);
  assert.match(recentHtml, /<td class="nameCell">openai-compatible-example-go<\/td>/);
  assert.match(recentHtml, /4\.52s \/ 810ms/);
  assert.match(barsHtml, /class="barLabel" title="xpspwc9mfb@privaterelay\.appleid\.com-extra-long-credential-name"/);
  assert.match(barsHtml, /class="barValue">8 请求/);
});

test('dashboard api detail shows the full upstream interface for source rows', () => {
  const { context, document } = createDashboardHarness();
  const api = 'codex · 上游 b374b8e7c98ca23c';
  context.renderApiDetailContent({ failure_count: 0, models: {} }, {
    loading: false,
    detail: {
      api,
      summary: { total_requests: 1, success_count: 1, failure_count: 0, total_tokens: 10 },
      model_stats: [],
      source_stats: [{ source: 'codex', provider: 'codex', total_requests: 1, success_count: 1, failure_count: 0, total_tokens: 10 }],
      error_stats: [],
      recent_events: [{ api, timestamp_ms: Date.UTC(2026, 6, 16, 5, 0, 0), model: 'gpt-5.5', source: 'codex', provider: 'codex', failed: false, latency_ms: 100, total_tokens: 10 }],
    },
  });

  const html = document.getElementById('apiDetail').innerHTML;
  assert.match(html, /class="barLabel" title="codex · 上游 b374b8e7c98ca23c"/);
  assert.match(html, /<td class="nameCell">codex · 上游 b374b8e7c98ca23c<\/td>/);
});

test('dashboard api detail labels every model with its token usage and cost', () => {
  const { context, document } = createDashboardHarness();
  vm.runInContext(`
    modelPrices = { 'gpt-4.1': { prompt: 2, completion: 8 } };
    manualModelPrices = {};
  `, context);
  context.renderApiDetailContent({ failure_count: 0, models: {} }, {
    loading: false,
    detail: {
      api: 'openai',
      summary: { total_requests: 2, success_count: 2, failure_count: 0, total_tokens: 1500000 },
      model_stats: [{
        model: 'gpt-4.1',
        total_requests: 2,
        success_count: 2,
        failure_count: 0,
        total_tokens: 1500000,
        input_tokens: 1000000,
        output_tokens: 500000,
      }],
      source_stats: [{ source: 'openai', provider: 'openai', total_requests: 2 }],
      error_stats: [],
      recent_events: [],
    },
  });

  const html = document.getElementById('apiDetail').innerHTML;
  assert.match(html, /gpt-4\.1<span class="barTokens">1\.5M<\/span><span class="barCost">US\$6\.00<\/span>/);
  assert.doesNotMatch(html, /openai<span class="barTokens">/);
  assert.doesNotMatch(html, /openai<span class="barCost">/);
});

test('dashboard event rows show the full upstream interface as source', () => {
  const { context, document } = createDashboardHarness();
  vm.runInContext(`
    eventsData = {
      total: 1,
      limit: 500,
      offset: 0,
      events: [{
        api: 'claude · 上游 f85c45252fee',
        timestamp: '2026-07-16T05:00:00Z',
        model: 'deepseek-v4-pro',
        source: 'claude',
        provider: 'claude',
        auth_index: '',
        failed: false,
        latency_ms: 100,
        tokens: { total_tokens: 10 }
      }]
    };
    renderEventsContent();
  `, context);

  assert.match(document.getElementById('events').innerHTML, /<td class="nameCell">claude · 上游 f85c45252fee<\/td>/);
});

test('dashboard wide statistic panels span the full layout width', () => {
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const css = fs.readFileSync(path.join(__dirname, 'style.css'), 'utf8');

  assert.match(html, /<div class="panel full">\s*<div class="panelHead"><h2 data-i18n="upstream_title">/);
  assert.match(html, /<div class="panel full">\s*<div class="panelHead"><h2 data-i18n="model_stats_title">/);
  assert.match(css, /\.detailActivityGrid\{grid-template-columns:minmax\(0,1fr\)\}/);
  assert.match(css, /\.barLabel\{[^}]*overflow-wrap:anywhere;[^}]*word-break:break-word/);
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
  await waitFor(() => el.textContent === '持久化已开启');
  assert.strictEqual(el.textContent, '持久化已开启');
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
  await waitFor(() => el.textContent === '持久化已开启');
  assert.strictEqual(el.textContent, '持久化已开启');
  assert.match(el.title, /5 条记录/);
  assert.match(el.title, /4,096/);
});

test('dashboard omits storage queue capacity when unavailable', async () => {
  const { document } = createDashboardHarness({
    storage: {
      enabled: true,
      path: 'usage-statistics.jsonl',
      loaded_path: 'usage-statistics/usage-2026-06-28.jsonl',
      write_queue_length: 5,
      write_queue_capacity: 0,
    },
  });

  const el = document.getElementById('storageStatus');
  await waitFor(() => el.textContent === '持久化已开启');
  assert.match(el.title, /5 条记录等待后台写入/);
  assert.doesNotMatch(el.title, /队列容量/);
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
  await waitFor(() => el.textContent === '持久化已开启');
  assert.strictEqual(el.textContent, '持久化已开启');
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
  await waitFor(() => el.textContent === '持久化已开启');
  assert.strictEqual(el.textContent, '持久化已开启');
  assert.match(el.title, /4 条记录/);
});

test('dashboard shows storage writer batch metrics in title', async () => {
  const { document } = createDashboardHarness({
    storage: {
      enabled: true,
      path: 'usage-statistics.jsonl',
      last_flush_at: '2026-06-28T01:00:00Z',
      last_write_batch_records: 12,
      last_write_batch_duration_ms: 1.6,
      last_write_queue_wait_ms: 3.2,
      write_batch_avg_duration_ms: 2.4,
      write_batch_p95_duration_ms: 5.6,
      write_batch_p99_duration_ms: 7.8,
      write_queue_wait_avg_ms: 1.2,
      write_queue_wait_p95_ms: 8.4,
      write_queue_wait_p99_ms: 10.2,
      write_pressure: 'normal',
    },
  });

  const el = document.getElementById('storageStatus');
  await waitFor(() => el.textContent === '持久化已开启');
  assert.strictEqual(el.textContent, '持久化已开启');
  assert.match(el.title, /最近批量写入 12 条/);
  assert.match(el.title, /写入压力：正常/);
  assert.match(el.title, /平均耗时/);
  assert.match(el.title, /耗时 p95/);
  assert.match(el.title, /最长排队/);
  assert.match(el.title, /排队 p95/);
});

test('dashboard warns when storage writer is slow without queue backlog', async () => {
  const { document } = createDashboardHarness({
    storage: {
      enabled: true,
      path: 'usage-statistics.jsonl',
      last_flush_at: '2026-06-28T01:00:00Z',
      last_write_batch_records: 8,
      write_queue_wait_avg_ms: 250,
      write_pressure: 'slow',
    },
  });

  const el = document.getElementById('storageStatus');
  await waitFor(() => el.textContent === '持久化已开启');
  assert.strictEqual(el.textContent, '持久化已开启');
  assert.match(el.title, /写入压力：写入偏慢/);
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

test('dashboard keeps previous event rows when event refresh fails', async () => {
  const { document, fetchCalls, setVisibility, setSummaryVersion, setDashboardEventsFailure } = createDashboardHarness();
  const countCalls = (part) => fetchCalls.filter((url) => url.includes(part)).length;

  await waitFor(() => document.getElementById('eventsCount').textContent.includes('1,200'));
  assert.ok(document.getElementById('events').innerHTML.includes('gpt-4.1'));
  const beforeEvents = countCalls('dashboard-events?');

  setDashboardEventsFailure(true);
  setSummaryVersion(2);
  setVisibility('visible');
  await waitFor(() => countCalls('dashboard-events?') > beforeEvents);

  assert.ok(document.getElementById('eventsCount').textContent.includes('1,200'));
  assert.ok(!document.getElementById('eventsCount').textContent.includes('共 0 条'));
  assert.ok(document.getElementById('events').innerHTML.includes('gpt-4.1'));
});

test('dashboard does not reuse previous event rows for a failed changed filter', async () => {
  const { context, document, setDashboardEventsFailure } = createDashboardHarness();

  await waitFor(() => document.getElementById('eventsCount').textContent.includes('1,200'));
  document.getElementById('filterModel').value = 'gpt-4.1';
  setDashboardEventsFailure(true);
  await context.renderEvents();

  assert.ok(document.getElementById('eventsCount').textContent.includes('共 0 条'));
  assert.ok(!document.getElementById('events').innerHTML.includes('gpt-4.1'));
});

test('dashboard polling refreshes details when summary version changes within the same second', async () => {
  const { fetchCalls, setVisibility, setSummaryVersion } = createDashboardHarness();
  const countCalls = (part) => fetchCalls.filter((url) => url.includes(part)).length;

  await waitFor(() => countCalls('dashboard-events') > 0 && countCalls('dashboard-api-detail') > 0);
  const beforeEvents = countCalls('dashboard-events');
  const beforeApiDetail = countCalls('dashboard-api-detail');

  setSummaryVersion(2);
  setVisibility('visible');
  await waitFor(() => countCalls('dashboard-events') > beforeEvents && countCalls('dashboard-api-detail') > beforeApiDetail);
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

test('dashboard summary polling treats empty 200 with matching etag as cached 304', async () => {
  const { document, fetchCalls, fetchRequests, setVisibility } = createDashboardHarness({
    dashboardEtags: true,
    emptyConditionalEtagOk: true,
  });
  const summaryRequests = () => fetchRequests.filter((req) => req.url.includes('dashboard-summary'));

  await waitFor(() => document.getElementById('eventsCount').textContent.includes('1,200'));
  const beforeSummary = summaryRequests().length;
  setVisibility('visible');

  await waitFor(() => summaryRequests().length > beforeSummary);
  const latestSummary = summaryRequests().at(-1);
  assert.strictEqual(optionHeaderValue(latestSummary.options, 'If-None-Match'), 'W/"summary-2023-11-15T06:13:20Z"');
  assert.ok(!fetchCalls.some((url) => url.includes('dashboard-data')), 'empty conditional response must not trigger dashboard-data fallback');
  assert.ok(document.getElementById('eventsCount').textContent.includes('1,200'));
  assert.ok(!document.getElementById('eventsCount').textContent.includes('共 0 条'));
});

test('dashboard api detail refresh keeps cached content while loading', async () => {
  const { context, document } = createDashboardHarness();

  await waitFor(() => document.getElementById('apiDetail').innerHTML.includes('deepseek-v4-flash-free'));
  const promise = context.renderApiDetail();

  assert.match(document.getElementById('apiDetail').innerHTML, /deepseek-v4-flash-free/);
  assert.doesNotMatch(document.getElementById('apiDetail').innerHTML, /正在加载接口请求明细/);
  await promise;
});

test('dashboard api detail follows current time range selector', async () => {
  const { document, fetchRequests } = createDashboardHarness();
  const apiDetailRequests = () => fetchRequests.filter((req) => req.url.includes('dashboard-api-detail'));

  await waitFor(() => apiDetailRequests().length > 0);
  // Default range is 24h (set from localStorage fallback).
  assert.strictEqual(new URL(apiDetailRequests().at(-1).url, 'http://test.local').searchParams.get('range'), '24h');

  document.getElementById('range').value = '7d';
  await document.getElementById('range').onchange();

  await waitFor(() => apiDetailRequests().length > 1);
  assert.strictEqual(new URL(apiDetailRequests().at(-1).url, 'http://test.local').searchParams.get('range'), '7d');
});

test('dashboard api detail uses summary failure count when backend returns null payload', async () => {
  const { document, fetchCalls } = createDashboardHarness({ nullDashboardApiDetail: true, apiFailureCount: 0 });

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-api-detail')) && document.getElementById('apiDetail').innerHTML.includes('请求明细加载失败'));

  const html = document.getElementById('apiDetail').innerHTML;
  assert.match(html, /失败 0/);
  assert.match(html, /metricValue">1,200</);
  assert.match(html, /请求明细加载失败/);
  assert.match(html, /class="splitGrid detailActivityGrid"/);
  assert.doesNotMatch(html, /错误统计/);
  assert.doesNotMatch(html, /暂无失败请求/);
  assert.doesNotMatch(html, /请求明细加载失败：dashboard-api-detail 返回空数据/);
});

test('dashboard fallback keeps health grid visible when summary endpoint fails', async () => {
  const { document, fetchCalls } = createDashboardHarness({ failDashboardSummary: true });
  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('healthGrid').innerHTML.includes('healthCell'));

  const cells = (document.getElementById('healthGrid').innerHTML.match(/healthCell/g) || []).length;
  assert.strictEqual(cells, 672);
  assert.strictEqual(document.getElementById('healthSuccess').textContent, '成功 1');
  assert.strictEqual(document.getElementById('healthFailure').textContent, '失败 1');
  assert.match(document.getElementById('updated').textContent, /兼容模式/);
});

test('dashboard fallback handles null summary payload', async () => {
  const { document, fetchCalls } = createDashboardHarness({ nullDashboardSummary: true });
  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('healthGrid').innerHTML.includes('healthCell'));

  const cells = (document.getElementById('healthGrid').innerHTML.match(/healthCell/g) || []).length;
  assert.strictEqual(cells, 672);
  assert.match(document.getElementById('updated').textContent, /兼容模式/);
});

test('dashboard fallback keeps selected range instead of full aggregates', async () => {
  const { document, fetchCalls } = createDashboardHarness({
    failDashboardSummary: true,
    range: '7h',
    dashboardDataOldDetailHours: 8,
    prices: { 'openai/gpt-4.1': { prompt: 100000, completion: 200000, cache: 0 } },
  });

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('updated').textContent.includes('兼容模式'));

  assert.strictEqual(document.getElementById('range').value, '7h');
  assert.strictEqual(document.getElementById('totalRequests').textContent, '1');
  assert.strictEqual(document.getElementById('totalTokens').textContent, '15');
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$2.00');
  assert.match(document.getElementById('successText').textContent, /成功请求：1/);
  assert.match(document.getElementById('failureText').textContent, /失败请求：0/);
  assert.match(document.getElementById('modelStats').innerHTML, /120ms/);
});

test('dashboard fallback range uses detail model instead of outer alias key', async () => {
  const { context, document, fetchCalls } = createDashboardHarness({
    failDashboardSummary: true,
    range: '7h',
    dashboardDataModelKey: 'claude-sonnet',
    dashboardDataDetailModel: 'gpt-4.1',
    dashboardDataOldDetailHours: 8,
    prices: { 'openai/gpt-4.1': { prompt: 100000, completion: 200000, cache: 0 } },
  });

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('modelStats').innerHTML.includes('gpt-4.1'));

  assert.strictEqual(document.getElementById('totalRequests').textContent, '1');
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$2.00');
  assert.match(document.getElementById('modelStats').innerHTML, /gpt-4\.1/);
  assert.doesNotMatch(document.getElementById('modelStats').innerHTML, /claude-sonnet/);
  const credentialStats = JSON.parse(vm.runInContext('JSON.stringify(summaryData.credential_stats)', context));
  assert.deepStrictEqual(credentialStats, [{ auth_index: 'auth-1', total_requests: 1, success_count: 1, failure_count: 0, total_tokens: 15 }]);
});

test('dashboard fallback buckets offset timestamps by their source hour', async () => {
  const generatedAt = '2026-01-03T00:00:00+08:00';
  const { document, fetchCalls } = createDashboardHarness({
    failDashboardSummary: true,
    range: '7h',
    dashboardDataNowMs: Date.parse(generatedAt),
    dashboardDataGeneratedAt: generatedAt,
    dashboardDataRecentTimestamp: '2026-01-02T23:30:00+08:00',
    dashboardDataOldDetailHours: 8,
  });

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('trendChart').innerHTML.includes('23:00'));

  assert.match(document.getElementById('trendChart').innerHTML, /23:00/);
  assert.doesNotMatch(document.getElementById('trendChart').innerHTML, /15:00/);
});

test('dashboard keeps summary path when model prices fail', async () => {
  const { document, fetchCalls } = createDashboardHarness({
    failModelPrices: true,
    range: '7h',
    summaryUsage: { total_requests: 7, success_count: 7, failure_count: 0, total_tokens: 70 },
  });

  await waitFor(() => fetchCalls.some((url) => url.includes('model-prices')) && document.getElementById('totalRequests').textContent === '7');

  assert.ok(fetchCalls.some((url) => url.includes('dashboard-summary?range=7h')));
  assert.ok(!fetchCalls.some((url) => url.includes('dashboard-data')), 'model price failure must not trigger dashboard-data fallback');
  assert.strictEqual(document.getElementById('updated').textContent.includes('兼容模式'), false);
});

test('dashboard summary retries without falling back when browser returns 304 without local cache', async () => {
  const { document, fetchCalls } = createDashboardHarness({ forceSummaryNotModified: true });

  await waitFor(() => fetchCalls.filter((url) => url.includes('dashboard-summary')).length >= 2);
  assert.ok(fetchCalls.some((url) => url.includes('dashboard-summary') && url.includes('_ts=')), 'expected cache-busting retry');
  assert.ok(!fetchCalls.some((url) => url.includes('dashboard-data')), 'should not fall back to dashboard-data');
  assert.doesNotMatch(document.getElementById('updated').textContent, /兼容模式/);
});

test('dashboard load reports null fallback payload without throwing', async () => {
  const { document, fetchCalls } = createDashboardHarness({ failDashboardSummary: true, nullDashboardData: true });
  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('updated').textContent.includes('dashboard-data 返回空数据'));

  assert.strictEqual(document.getElementById('updated').textContent, 'dashboard-data 返回空数据');
});

test('dashboard fallback keeps upstream aggregates when details are trimmed', async () => {
  const { document, fetchCalls } = createDashboardHarness({ failDashboardSummary: true, trimmedDashboardData: true, range: 'all' });

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('apiStats').innerHTML.includes('openai'));

  assert.strictEqual(document.getElementById('totalRequests').textContent, '4');
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$0.000177');
  assert.match(document.getElementById('apiStats').innerHTML, /4 <span class="ok">\(3<\/span> <span class="bad">1\)<\/span>/);
  assert.match(document.getElementById('modelStats').innerHTML, /100ms/);
});

test('dashboard fallback uses persisted provider aggregates when details are trimmed', async () => {
  const { document, fetchCalls } = createDashboardHarness({
    failDashboardSummary: true,
    trimmedDashboardData: true,
    range: 'all',
    prices: {
      'gpt-4.1': { prompt: 2, completion: 8, cache: 0.5, cache_write: 2.5 },
      'openai/gpt-4.1': { prompt: 20, completion: 0, cache: 0, cache_write: 100 },
    },
    dashboardDataProviders: [{
      provider: 'openai', total_requests: 4, success_count: 3, failure_count: 1,
      total_tokens: 45, input_tokens: 30, output_tokens: 15,
      cached_tokens: 2, cache_write_tokens: 1, reasoning_tokens: 4,
    }],
  });

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('totalCost').textContent !== '-');
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$0.00066');
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
    pathname: '/v0/resource/plugins/usage-dashboard-zduu/dashboard',
    managementKey: 'test-management-key',
  });

  await waitFor(() => /gpt-4\.1/.test(document.getElementById('priceList').innerHTML));
  assert.match(document.getElementById('priceList').innerHTML, /gpt-4\.1/);

  document.getElementById('priceModel').value = 'gpt-5';
  document.getElementById('pricePrompt').value = '1.25';
  document.getElementById('priceCompletion').value = '10';
  document.getElementById('priceCache').value = '';
  document.getElementById('priceCacheWrite').value = '';
  await document.getElementById('savePrice').onclick();

  const put = fetchRequests.find((req) => req.url.includes('model-prices') && req.options.method === 'PUT');
  assert.ok(put, 'expected PUT /model-prices');
  assert.strictEqual(put.url, '/v0/management/plugins/usage-dashboard-zduu/model-prices');
  assert.strictEqual(put.options.headers.Authorization, 'Bearer test-management-key');
  assert.strictEqual(put.options.headers['x-management-key'], 'test-management-key');
  assert.deepStrictEqual(JSON.parse(put.options.body), {
    model: 'gpt-5',
    price: { prompt: 1.25, completion: 10, cache: 1.25, cache_write: 0 },
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

test('dashboard client API selection filters linked panels and sort buttons restore all data', async () => {
  const selector = 'h.' + 'a'.repeat(56) + '.c2sqKioqKip4eA';
  const filteredSummary = {
    generated_at: new Date().toISOString(),
    usage: {
      total_requests: 2,
      success_count: 2,
      failure_count: 0,
      total_tokens: 140,
      cached_tokens: 0,
      cache_write_tokens: 0,
      reasoning_tokens: 0,
      avg_latency_ms: 80,
      apis: {
        'openai-filtered': {
          total_requests: 2,
          success_count: 2,
          failure_count: 0,
          total_tokens: 140,
          avg_latency_ms: 80,
          models: { 'gpt-filtered': { total_requests: 2, success_count: 2, failure_count: 0, total_tokens: 140 } },
        },
      },
      requests_by_hour: { '12': 2 },
      tokens_by_hour: { '12': 140 },
      cost_by_hour: { '12': 0.01 },
      requests_by_day: {},
      tokens_by_day: {},
      cost_by_day: {},
    },
    health_grid: [],
    source_stats: [],
    credential_stats: [],
    client_api_stats: [],
    model_stats: [{ model: 'gpt-filtered', total_requests: 2, success_count: 2, failure_count: 0, total_tokens: 140 }],
    _meta: { summary_version: 2, current_hour: 12, storage: { enabled: false } },
  };
  const { context, document, fetchCalls } = createDashboardHarness({
    clientApiStats: [{
      api_key: 'sk******xx',
      api_key_hash: 'a'.repeat(56),
      selector,
      total_requests: 2,
      success_count: 2,
      failure_count: 0,
      total_tokens: 140,
      models: [],
    }],
    filteredSummary,
  });

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events?')));
  document.querySelectorAll('[data-client-api-select]')[0].onclick();
  await context.selectClientApiCard(selector, [{ selector, name: 'sk******xx' }]);

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-summary') && new URL(url, 'http://test.local').searchParams.get('client_api') === selector));
  const filteredEvent = fetchCalls.filter((url) => url.includes('dashboard-events?')).at(-1);
  const filteredDetail = fetchCalls.filter((url) => url.includes('dashboard-api-detail')).at(-1);
  assert.strictEqual(new URL(filteredEvent, 'http://test.local').searchParams.get('client_api'), selector);
  assert.strictEqual(new URL(filteredDetail, 'http://test.local').searchParams.get('client_api'), selector);
  assert.match(document.getElementById('apiStats').innerHTML, /openai-filtered/);
  assert.match(document.getElementById('modelStats').innerHTML, /gpt-filtered/);
  assert.match(document.getElementById('clientApiStats').innerHTML, /已选中/);
  assert.match(document.getElementById('clientApiFilterStatus').innerHTML, /当前筛选：sk\*\*\*\*\*\*xx/);

  await document.querySelectorAll('[data-api-sort]')[0].onclick();
  const restoredEvent = fetchCalls.filter((url) => url.includes('dashboard-events?')).at(-1);
  assert.strictEqual(new URL(restoredEvent, 'http://test.local').searchParams.get('client_api'), null);
  assert.match(document.getElementById('apiStats').innerHTML, /openai/);
  assert.doesNotMatch(document.getElementById('clientApiFilterStatus').innerHTML, /当前筛选/);
});

test('dashboard explains that client API filtering is unavailable in compatibility mode', async () => {
  const { document } = createDashboardHarness({ failDashboardSummary: true });
  await waitFor(() => document.getElementById('updated').textContent.includes('兼容'));
  document.querySelectorAll('[data-client-api-select]')[0].onclick();
  assert.match(document.getElementById('clientApiFilterStatus').innerHTML, /兼容数据模式无法可靠应用 API Key 筛选/);
});

test('dashboard compatibility warning preserves the selected filter and load error', async () => {
  const selector = 'm.c2sQKioqKip4eA';
  const { context, document } = createDashboardHarness({
    failFilteredDashboardSummary: true,
    clientApiStats: [{
      api_key: 'sk******xx',
      total_requests: 2,
      success_count: 2,
      failure_count: 0,
      total_tokens: 140,
      models: [],
    }],
  });
  document.querySelectorAll('[data-client-api-select]')[0].onclick();
  await context.selectClientApiCard(selector, [{ selector, name: 'sk******xx' }]);

  const status = document.getElementById('clientApiFilterStatus').innerHTML;
  assert.match(status, /当前筛选：sk\*\*\*\*\*\*xx/);
  assert.match(status, /API Key 筛选数据加载失败/);
  assert.match(status, /兼容数据模式无法可靠应用 API Key 筛选/);
});

test('dashboard trend shows the client API filter error when filtered summary fails', async () => {
  const selector = 'm.c2sQKioqKip4eA';
  const { context, document } = createDashboardHarness({
    failFilteredDashboardSummary: true,
    clientApiStats: [{
      api_key: 'sk******xx',
      selector,
      total_requests: 2,
      success_count: 2,
      failure_count: 0,
      total_tokens: 140,
      models: [],
    }],
  });
  document.querySelectorAll('[data-client-api-select]')[0].onclick();
  await context.selectClientApiCard(selector, [{ selector, name: 'sk******xx' }]);

  assert.match(document.getElementById('trendChart').innerHTML, /API Key 筛选数据加载失败/);
  assert.doesNotMatch(document.getElementById('trendChart').innerHTML, /暂无趋势数据/);
});
