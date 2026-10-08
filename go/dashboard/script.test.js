const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { gzipSync } = require('node:zlib');

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
    // 真实的 classList:保留类名状态,否则测试无法断言 add/remove/toggle 的效果。
    const classes = new Set();
    this.classList = {
      add(...names) { names.forEach((name) => classes.add(name)); },
      remove(...names) { names.forEach((name) => classes.delete(name)); },
      contains(name) { return classes.has(name); },
      toggle(name, force) {
        const on = force === undefined ? !classes.has(name) : !!force;
        if (on) classes.add(name);
        else classes.delete(name);
        return on;
      },
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
  const hideZeroUpstreamButton = new FakeElement('hideZeroUpstream');
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
    __upstreamFilterButtons: { hideZeroUpstream: hideZeroUpstreamButton },
    body: new FakeElement('body'),
    documentElement: new FakeElement('html'),
    get visibilityState() {
      return visibilityState;
    },
    getElementById(id) {
      if (id === 'hideZeroUpstream') return hideZeroUpstreamButton;
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
  // 在页面脚本执行前预置,用于验证「上次已开启,刷新后继续生效」的持久化读取。
  if (options.hideZeroUpstream) localStorage.setItem('cpa-usage-hide-zero-upstream-v1', String(options.hideZeroUpstream));

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
  if (options.summaryModelProviders) summary.model_stats[0].providers = options.summaryModelProviders;
  if (options.storage) summary._meta.storage = options.storage;
  if (options.summaryCurrency) summary._meta.currency = options.summaryCurrency;
  if (options.clientApiStats) summary.client_api_stats = options.clientApiStats;
  if (options.credentialStats) summary.credential_stats = options.credentialStats;
  if (options.summaryUsage) Object.assign(summary.usage, options.summaryUsage);
  if (options.extraUpstreamApis) {
    Object.entries(options.extraUpstreamApis).forEach(([api, stats]) => {
      const merged = Object.assign({ total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0, avg_latency_ms: 0, models: {} }, stats);
      // 只在调用方压根没写 success_count 时才推导:给了 total_requests 和
      // failure_count 就按差值算,方便构造指定成功率的接口。显式传 undefined
      // (模拟旧快照缺字段)时 hasOwnProperty 为真,保留 undefined 交给 num() 兜底。
      if (!Object.prototype.hasOwnProperty.call(stats, 'success_count') && stats.total_requests !== undefined && stats.failure_count !== undefined) {
        merged.success_count = stats.total_requests - stats.failure_count;
      }
      summary.usage.apis[api] = merged;
    });
  }

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

  // 详情载荷带上被请求的 api 与专属模型名:迟到的旧响应因此和当前选中项渲染出
  // 不同内容,否则「丢弃过期响应」的测试无法区分两者。
  function apiDetailPayload(url) {
    const requestedApi = new URL(String(url || ''), 'http://test.local/v0/management/plugins/usage-dashboard-zduu/dashboard').searchParams.get('api') || 'openai';
    const base = {
      api: requestedApi,
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
    // 只有 openai 保持原有字面量(多个既有测试依赖它),其余 api 换成专属模型名,
    // 这样过期响应的正文内容与新选中项明显不同。
    if (requestedApi !== 'openai') {
      const tag = requestedApi + '-model';
      base.model_stats = base.model_stats.map((m) => Object.assign({}, m, { model: tag }));
      base.source_stats = base.source_stats.map((s) => Object.assign({}, s, { source: tag }));
      base.recent_events = base.recent_events.map((e) => Object.assign({}, e, { model: tag, source: tag }));
    }
    return base;
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
    TextDecoder,
    TextEncoder,
    atob: (value) => Buffer.from(value, 'base64').toString('binary'),
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
          if (manualPrices !== undefined) manualPrices[body.model] = body.price;
        } else if (options.method === 'DELETE') {
          const parsed = new URL(String(url), 'http://test.local/v0/management/plugins/usage-dashboard-zduu/dashboard');
          const model = parsed.searchParams.get('model');
          delete prices[model];
          if (manualPrices !== undefined) delete manualPrices[model];
        }
        payload = { prices, updated_at: new Date().toISOString(), storage: {} };
        if (manualPrices !== undefined) payload.manual_prices = manualPrices;
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
  if (options.denyStorageReads) localStorage.getItem = () => { throw new Error('storage denied'); };
  if (options.denyStorageWrites) localStorage.setItem = () => { throw new Error('storage denied'); };
  if (options.denyStorageAccess) Object.defineProperty(context, 'localStorage', { get() { throw new Error('storage denied'); } });

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

test('quota collector submits authenticated v8 observations once and strips unrelated signals', async () => {
  const { context } = createDashboardHarness({ pathname: '/proxy/v0/resource/plugins/usage-dashboard-zduu/dashboard' });
  await context.load();
  context.localStorage.setItem('managementKey', 'fixture-key');
  const calls = [];
  const observed = '2026-10-02T08:00:00Z';
  context.fetch = async (url, options) => {
    calls.push({ url, options });
    const payload = String(url).endsWith('/credentials') ? { files: [{
      provider: 'devin', auth_index: 'index', id: 'devin.json',
      quota: { observed_at: observed, signals: { weekly_quota_remaining_percent: '60%', weekly_quota_reset_at: '2026-10-05T08:00:00Z', access_token: 'must-not-be-submitted' } },
    }] } : { accepted: 1, skipped: 0, rejected: 0 };
    return { ok: true, status: 200, headers: { get() { return ''; } }, text: async () => JSON.stringify(payload) };
  };
  assert.equal(await context.collectQuotaObservations('first-instance'), true);
  assert.equal(await context.collectQuotaObservations('first-instance'), false);
  assert.equal(calls.filter(call => call.options.method === 'POST').length, 1);
  assert.equal(calls[0].url, '/proxy/v8/management/credentials');
  for (const call of calls) assert.equal(call.options.headers.Authorization, 'Bearer fixture-key');
  const body = JSON.parse(calls.find(call => call.options.method === 'POST').options.body);
  assert.equal(body.observations[0].observed_at, observed);
  assert.deepEqual(Object.keys(body.observations[0].signals).sort(), ['weekly_quota_remaining_percent', 'weekly_quota_reset_at']);
  assert.ok(!JSON.stringify(body).includes('must-not-be-submitted'));
  assert.equal(await context.collectQuotaObservations('restarted-instance'), true);
  assert.equal(calls.filter(call => call.options.method === 'POST').length, 2, 'restart must resubmit observations lost by an in-memory backend');
});

test('quota collector only retries rejected observations after a cooldown', async () => {
  const { context } = createDashboardHarness({ pathname: '/proxy/v0/resource/plugins/usage-dashboard-zduu/dashboard' });
  await context.load();
  context.localStorage.setItem('managementKey', 'fixture-key');
  const posts = [];
  const file = (index) => ({ provider: 'devin', auth_index: index, id: `${index}.json`,
    quota: { observed_at: '2026-10-02T08:00:00Z', signals: { weekly_quota_remaining_percent: '60%', weekly_quota_reset_at: '2026-10-05T08:00:00Z' } } });
  context.fetch = async (url, options) => {
    let payload = { files: [file('good'), file('bad')] };
    if (options.method === 'POST') {
      const observations = JSON.parse(options.body).observations;
      posts.push(observations.map((row) => row.auth_index));
      const rejected = observations.map((row, index) => row.auth_index === 'bad' ? index : -1).filter((index) => index >= 0);
      payload = { accepted: observations.length - rejected.length, skipped: 0, rejected: rejected.length, rejected_indexes: rejected };
    }
    return { ok: true, status: 200, headers: { get() { return ''; } }, text: async () => JSON.stringify(payload) };
  };
  const realNow = Date.now;
  let now = realNow();
  context.Date.now = () => now;
  try {
    await context.collectQuotaObservations('instance');
    await context.collectQuotaObservations('instance');
    assert.deepEqual(posts, [['good', 'bad']], 'neither accepted nor rejected rows are resent on the next poll');
    now += 300001;
    await context.collectQuotaObservations('instance');
    assert.deepEqual(posts, [['good', 'bad'], ['bad']], 'only the rejected row is retried after the cooldown');
  } finally {
    context.Date.now = realNow;
  }
});

test('collapsing during an Antigravity refresh keeps the earlier error', async () => {
  const { context } = createDashboardHarness({ language: 'en', pathname: '/proxy/v0/resource/plugins/usage-dashboard-zduu/dashboard' });
  await context.load();
  context.localStorage.setItem('managementKey', 'fixture-key');
  context.load = async () => {};
  let fetches = 0;
  context.fetch = async () => {
    fetches++;
    // The panel is collapsed while the credential list is loading.
    vm.runInContext('quotaExpandedApis.delete(selectedApi)', context);
    return { ok: true, status: 200, headers: { get() { return ''; } }, text: async () => JSON.stringify({ files: [] }) };
  };
  vm.runInContext('selectedApi = "antigravity-upstream"; quotaExpandedApis.add(selectedApi); antigravityQuotaErrors.add(selectedApi);', context);
  const refs = [{ provider: 'antigravity', auth_index: 'ag-index', auth_id: 'antigravity.json' }];
  // Rendering an expanded panel starts the automatic refresh.
  context.renderApiDetailContent({ total_requests: 1, models: {} }, { detail: { summary: { total_requests: 1 }, quota_credentials: refs } });
  await vm.runInContext('antigravityQuotaRequest', context);
  assert.equal(fetches, 1, 'the refresh must reach the credential list');
  assert.equal(vm.runInContext('antigravityQuotaErrors.has(selectedApi)', context), true);
});

test('quota periods keep unknown values and separate the previous model table', async () => {
  const { context } = createDashboardHarness({ language: 'en' });
  await context.load();
  const cycle = { start_at: '2026-10-02T08:00:00Z', end_at: '2026-10-02T13:00:00Z', used_percent: null,
    summary: { total_requests: 1, total_tokens: 1000, estimated_cost: 0 },
    model_stats: [{ model: '<previous-model>', total_requests: 1, estimated_cost: 0 }] };
  const html = context.quotaPeriodHtml(cycle, false);
  assert.match(html, /&lt;previous-model&gt;/);
  assert.match(html, /—/);
  assert.doesNotMatch(html, /Estimated quota|data-quota-reset|NaN|Infinity/);
  assert.equal(context.quotaValue(null, true), '—');
  assert.notEqual(context.quotaValue(0, true), '—');
  const weekly = context.quotaCyclesHtml([{ provider: 'codex', auth_index: 'index', groups: [{ group_id: 'shared:primary', window_seconds: 604800, current: cycle }] }]);
  assert.match(weekly, /Weekly quota/);
  assert.doesNotMatch(weekly, /5-hour|5h/);
});

test('quota current capacity uses the full amount only at exactly 100 percent', async () => {
  const { context } = createDashboardHarness({ language: 'en' });
  await context.load();
  const base = { start_at: '2026-10-02T08:00:00Z', end_at: '2026-10-02T13:00:00Z',
    summary: { estimated_cost: 3.57 }, model_stats: [], used_percent: 100,
    actual_total_usd: 3.57, estimated_total_usd: null, estimated_remaining_usd: 0 };
  const full = context.quotaPeriodHtml(base, true);
  assert.match(full, /Actual capacity/);
  assert.match(full, /3\.57/);
  assert.match(full, /0\.00/);
  assert.match(full, /Fully used/);
  assert.doesNotMatch(full, /Estimated capacity/);
  const partial = context.quotaPeriodHtml({ ...base, used_percent: 99.99, actual_total_usd: null, estimated_total_usd: 3.58 }, true);
  assert.match(partial, /Estimated capacity/);
  assert.match(partial, /&lt;100%/);
  assert.match(partial, /recorded spend ÷ used fraction/);
  assert.doesNotMatch(partial, /Actual capacity|Fully used/);
  const unknown = context.quotaPeriodHtml({ ...base, actual_total_usd: null, summary: { estimated_cost: null } }, true);
  assert.match(unknown, /Actual capacity/);
  assert.match(unknown, /—/);
  assert.doesNotMatch(unknown, /3\.57|NaN|Infinity/);
});

test('quota model table shows budget-based token capacity without a model-only cost column', async () => {
  const { context } = createDashboardHarness({ language: 'en' });
  await context.load();
  const cheap = { model: '<cheap>', total_requests: 3, success_count: 2, failure_count: 1, total_tokens: 1000000,
    input_tokens: 800000, output_tokens: 200000, cached_tokens: 400000, cache_write_tokens: 0,
    estimated_cost: 10, model_only_estimated_total_tokens: 10000000 };
  const expensive = { ...cheap, model: 'expensive', total_tokens: 500000, estimated_cost: 25, model_only_estimated_total_tokens: 2000000 };
  const cycle = { estimated_total_usd: 100, used_percent: 35, summary: { total_requests: 6, total_tokens: 1500000, estimated_cost: 35 }, model_stats: [cheap, expensive] };
  const html = context.quotaModelsHtml(cycle);
  const headers = Array.from(html.matchAll(/<th>(.*?)<\/th>/g), match => match[1]);
  assert.deepEqual(headers, ['Model', 'Requests', 'Success', 'Failure', 'Total', 'Cache Hit Rate', 'Actual cost', 'Cost share', 'Model-only estimated total tokens']);
  assert.match(html, /&lt;cheap&gt;/);
  assert.match(html, /1\.00 M/);
  assert.match(html, /0\.50 M/);
  assert.match(html, /50\.0%/);
  assert.match(html, /28\.6%/);
  assert.match(html, /10\.00 M/);
  assert.match(html, /2\.00 M/);
  assert.match(html, /estimated capacity × this model’s period tokens ÷ its actual cost/);
  assert.doesNotMatch(html, /Model-only estimated total cost|reference range|percentage points|\$100\.00/);
  assert.match(context.quotaPeriodHtml(cycle, true), /1\.50 M/);
  assert.equal(Array.from(html.matchAll(/<td[ >]/g)).length, headers.length * 2);
  for (const invalid of [null, undefined, NaN, Infinity, -1]) {
    const row = { ...cheap, model_only_estimated_total_tokens: invalid };
    const unknown = context.quotaModelsHtml({ ...cycle, model_stats: [row] });
    assert.match(unknown, /<td>—<\/td><\/tr>/);
    assert.doesNotMatch(unknown, /NaN|Infinity|10\.00 M/);
  }
  assert.equal(context.quotaTokens(0), '0.00 M');
  assert.doesNotMatch(context.quotaPeriodHtml({ summary: null, model_stats: [] }, true), /does not identify the models/);
  // Claude's prompt denominator includes its separate cache reads and writes.
  const claude = { ...cheap, providers: [{ provider: 'claude', input_tokens: 100, output_tokens: 20, cached_tokens: 40, cache_write_tokens: 10, total_tokens: 170 }] };
  assert.match(context.quotaModelsHtml({ ...cycle, model_stats: [claude] }), /26\.7%/);
});

test('quota panel sits between source distribution and activity, starts collapsed and retains its toggle state', async () => {
  const { context, document } = createDashboardHarness({ language: 'en' });
  await context.load();
  const cycle = { start_at: '2026-10-02T08:00:00Z', end_at: '2026-10-02T13:00:00Z', used_percent: 50,
    summary: { total_requests: 1, estimated_cost: 5 }, model_stats: [] };
  const detail = { summary: { total_requests: 1 }, model_stats: [], source_stats: [], error_stats: [], recent_events: [],
    credential_quota_cycles: [{ provider: 'codex', auth_index: 'index', groups: [{ group_id: 'shared:primary', window_seconds: 18000, current: cycle }] }] };
  context.renderApiDetailContent({ total_requests: 1, models: {} }, { detail });
  let html = document.getElementById('apiDetail').innerHTML;
  assert.match(html, /<details[^>]*id="quotaCycles">/);
  assert.ok(html.indexOf('Source Distribution') < html.indexOf('id="quotaCycles"'));
  assert.ok(html.indexOf('id="quotaCycles"') < html.indexOf('detailActivityGrid'));
  const panel = document.getElementById('quotaCycles');
  panel.open = true;
  panel.ontoggle();
  context.renderApiDetailContent({ total_requests: 1, models: {} }, { detail });
  html = document.getElementById('apiDetail').innerHTML;
  assert.match(html, /<details[^>]*id="quotaCycles" open>/);
  panel.open = false;
  panel.ontoggle();
  context.renderApiDetailContent({ total_requests: 1, models: {} }, { detail });
  assert.match(document.getElementById('apiDetail').innerHTML, /<details[^>]*id="quotaCycles">/);
});

test('Antigravity collector only probes selected credentials while expanded and throttles upstream requests', async () => {
  const { context, document, setVisibility } = createDashboardHarness({ language: 'en', pathname: '/proxy/v0/resource/plugins/usage-dashboard-zduu/dashboard' });
  context.atob = atob; // api-call returns plain JSON, matching browser decoding.
  await context.load();
  context.localStorage.setItem('managementKey', 'fixture-key');
  vm.runInContext('selectedApi = "antigravity-upstream";', context);
  const refs = [{ provider: 'antigravity', auth_index: 'ag-index', auth_id: 'antigravity.json' }];
  context.renderApiDetailContent({ total_requests: 1, models: {} }, { detail: { summary: { total_requests: 1 }, quota_credentials: refs } });
  const calls = [];
  let detailRefreshes = 0;
  context.renderApiDetail = async () => { detailRefreshes++; };
  context.fetch = async (url, options) => {
    calls.push({ url, options });
    let payload;
    if (url.endsWith('/credentials')) payload = { files: [
      { provider: 'antigravity', id: 'antigravity.json', auth_index: 'ag-index', project_id: 'fixture-project', access_token: 'private-token' },
      { provider: 'antigravity', id: 'unselected.json', auth_index: 'other-index', project_id: 'other-project' },
    ] };
    else if (url.endsWith('/api-call')) payload = { status_code: 200, body: JSON.stringify({ groups: [{ displayName: '<Claude>', access_token: 'private-token', buckets: [
      { bucketId: 'five-hour', window: '5h', remainingFraction: 0, resetTime: '2026-10-05T08:00:00Z' },
      { bucketId: 'weekly', window: 'weekly', remainingFraction: '0.6', resetTime: '2026-10-10T08:00:00Z' },
      { window: 'monthly', remainingFraction: 0.5, resetTime: '2026-10-10T08:00:00Z' },
    ] }] }) };
    else payload = { accepted: 1, rejected: 0 };
    return { ok: true, status: 200, headers: { get() { return ''; } }, text: async () => JSON.stringify(payload) };
  };
  await context.refreshAntigravityQuotaForSelectedApi();
  assert.equal(calls.length, 0, 'collapsed quota panel must not query upstream');
  const panel = document.getElementById('quotaCycles');
  panel.open = true;
  panel.ontoggle();
  await vm.runInContext('antigravityQuotaRequest', context);
  assert.equal(calls.length, 3, calls.map((call) => call.url).join('\n'));
  const probe = JSON.parse(calls[1].options.body);
  assert.equal(probe.auth_index, 'ag-index');
  assert.equal(probe.header.Authorization, 'Bearer $TOKEN$');
  assert.equal(probe.url, 'https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary');
  assert.equal(calls[1].url, '/proxy/v0/management/api-call');
  assert.equal(calls[1].options.headers.Authorization, 'Bearer fixture-key');
  const observation = JSON.parse(calls[2].options.body).observations[0];
  assert.equal(observation.provider, 'antigravity');
  assert.equal(observation.antigravity_buckets.length, 2, 'unsupported durations must not create guessed cycles');
  assert.equal(observation.antigravity_buckets[0].remaining_fraction, 0, 'zero is exhausted quota');
  assert.ok(!JSON.stringify(calls).includes('private-token'));
  assert.equal(detailRefreshes, 1);
  await context.refreshAntigravityQuotaForSelectedApi();
  assert.equal(calls.length, 3, 'ordinary polling must retain the five-minute throttle');
  context.load = async () => {}; // Isolate visibility gating from ordinary dashboard polling.
  setVisibility('hidden');
  await context.refreshAntigravityQuotaForSelectedApi(true);
  assert.equal(calls.length, 3, 'hidden pages must not probe upstream');
  setVisibility('visible');
  await context.refreshAntigravityQuotaForSelectedApi(true);
  assert.equal(calls.length, 6, 'manual refresh can bypass the throttle');
  const html = context.quotaPeriodHtml({ unmapped: true, used_percent: 100, start_at: '2026-10-02T08:00:00Z', end_at: '2026-10-02T13:00:00Z', summary: null, model_stats: null }, true);
  assert.match(html, /does not identify the models/);
});

test('quota previous capacity distinguishes fully used and estimated amounts', async () => {
  const { context } = createDashboardHarness({ language: 'en' });
  await context.load();
  const cycle = { start_at: '2026-10-02T08:00:00Z', end_at: '2026-10-02T13:00:00Z',
    used_percent: 40, estimated_total_usd: 50, actual_total_usd: null,
    summary: { total_requests: 1, total_tokens: 1000, estimated_cost: 20 }, model_stats: [] };
  const partial = context.quotaPeriodHtml(cycle, false);
  assert.match(partial, /Estimated capacity/);
  assert.match(partial, /40\.0%/);
  assert.match(partial, /50\.00/);
  assert.doesNotMatch(partial, /Actual capacity|Estimated remaining|data-quota-reset/);
  const full = context.quotaPeriodHtml({ ...cycle, used_percent: 100, actual_total_usd: 20, estimated_total_usd: null }, false);
  assert.match(full, /Actual capacity/);
  assert.match(full, /100\.0%/);
  assert.match(full, /20\.00/);
  assert.doesNotMatch(full, /Estimated capacity|Estimated remaining|data-quota-reset/);
  const unknown = context.quotaPeriodHtml({ ...cycle, estimated_total_usd: null }, false);
  assert.match(unknown, /40\.0%/);
  assert.match(unknown, /—/);
  const almostFull = context.quotaPeriodHtml({ ...cycle, used_percent: 99.99 }, false);
  assert.match(almostFull, /Estimated capacity/);
  assert.match(almostFull, /&lt;100%/);
  assert.doesNotMatch(almostFull, /Actual capacity/);
});

test('Antigravity query failures preserve history, back off, and allow manual fallback recovery', async () => {
  const { context } = createDashboardHarness({ language: 'en' });
  context.atob = atob;
  await context.load();
  vm.runInContext('selectedApi = "ag";', context);
  context.renderApiDetailContent({ total_requests: 1, models: {} }, { detail: { summary: { total_requests: 1 },
    quota_credentials: [{ provider: 'antigravity', auth_index: 'index', auth_id: 'ag.json' }] } });
  vm.runInContext('quotaExpandedApis.add("ag");', context);
  let probes = 0, submissions = 0, recovered = false;
  context.renderApiDetail = async () => {};
  context.renderApiDetailFromCache = () => {};
  context.fetch = async (url, options) => {
    let payload;
    if (url.endsWith('/credentials')) payload = { files: [{ provider: 'antigravity', auth_index: 'index', id: 'ag.json', project_id: 'project' }] };
    else if (url.endsWith('/api-call')) {
      probes++;
      const upstream = JSON.parse(options.body).url;
      payload = recovered && upstream.includes('.sandbox.') ? { status_code: 200, body: JSON.stringify({ groups: [{ display_name: 'Gemini', buckets: [{ bucket_id: 'weekly', window: 'week', remaining_fraction: 0.4, reset_time: '2026-10-10T08:00:00Z' }] }] }) } : { status_code: 503, body: 'Unavailable' };
    } else { submissions++; payload = { accepted: 1, rejected: 0 }; }
    return { ok: true, status: 200, headers: { get() { return ''; } }, text: async () => JSON.stringify(payload) };
  };
  await context.refreshAntigravityQuotaForSelectedApi();
  assert.equal(probes, 3);
  assert.equal(submissions, 0, 'failed quota fetch must not overwrite stored observations');
  assert.equal(vm.runInContext('antigravityQuotaErrors.has("ag")', context), true);
  await context.refreshAntigravityQuotaForSelectedApi();
  assert.equal(probes, 3, 'failed queries must not retry on every detail render');
  recovered = true;
  await context.refreshAntigravityQuotaForSelectedApi(true);
  assert.equal(probes, 5, 'manual refresh tries the primary endpoint then the sandbox fallback');
  assert.equal(submissions, 1);
  assert.equal(vm.runInContext('antigravityQuotaErrors.has("ag")', context), false);
});

test('unchanged summary polling still checks whether expanded Antigravity quota is due', async () => {
  const { context, document, fetchCalls } = createDashboardHarness({ dashboardEtags: true });
  await waitFor(() => document.getElementById('apiDetail').innerHTML.includes('deepseek-v4-flash-free'));
  let quotaChecks = 0;
  context.refreshAntigravityQuotaForSelectedApi = () => { quotaChecks++; };
  const before = fetchCalls.filter(url => url.includes('dashboard-api-detail')).length;
  await context.load();
  assert.equal(quotaChecks, 1, '304 summary must not suppress automatic quota collection');
  assert.equal(fetchCalls.filter(url => url.includes('dashboard-api-detail')).length, before);
});

test('quota detail remains selectable when retention or filters remove all main events', async () => {
  const { context, document } = createDashboardHarness();
  await context.load();
  vm.runInContext('summaryData._meta.quota_apis = ["retained-credential"]; summaryData.usage.apis = {}; selectedApi = "retained-credential";', context);
  context.renderApiStats();
  assert.equal(document.getElementById('apiSelect').value, 'retained-credential');
  assert.equal(document.getElementById('apiSelect').disabled, false);
  let requests = 0;
  context.fetchApiDetailData = async api => {
    requests++;
    assert.equal(api, 'retained-credential');
    return { summary: { total_requests: 0, total_tokens: 0 }, model_stats: [], recent_events: [],
      credential_quota_cycles: [{ provider: 'claude', auth_index: 'index', credential_name: 'account.json',
        groups: [{ group_id: 'shared:5h', window_seconds: 18000, previous: { start_at: '2026-10-02T08:00:00Z', end_at: '2026-10-02T13:00:00Z', used_percent: 30, summary: { total_requests: 1, estimated_cost: 5 }, model_stats: [{ model: 'retained-model', total_requests: 1, estimated_cost: 5 }] } }] }] };
  };
  await context.renderApiDetail();
  assert.equal(requests, 1);
  assert.match(document.getElementById('apiDetail').innerHTML, /quotaCycles/);
  vm.runInContext('selectedClientApi = { selector: "other-client" }; filteredSummaryData = { usage: { apis: {} } };', context);
  context.renderApiStats();
  assert.equal(document.getElementById('apiSelect').value, 'retained-credential');
  await context.renderApiDetail();
  assert.equal(requests, 2);
  context.renderApiDetailFromCache();
  assert.match(document.getElementById('apiDetail').innerHTML, /quotaCycles/);
  vm.runInContext('summaryData._meta.quota_apis = [];', context);
  context.renderApiStats();
  assert.equal(document.getElementById('apiSelect').disabled, true);
});

test('quota countdown updates locally and refreshes an expired period only once', async () => {
  const { context, document } = createDashboardHarness();
  await context.load();
  const element = new FakeElement('countdown');
  element.dataset.quotaReset = String(Date.now() - 1000);
  document.querySelectorAll = selector => selector === '[data-quota-reset]' ? [element] : [];
  let refreshes = 0;
  context.renderApiDetail = () => { refreshes++; };
  context.updateQuotaCountdowns();
  context.updateQuotaCountdowns();
  assert.equal(element.textContent, '00:00:00');
  assert.equal(refreshes, 1);
  assert.equal(context.quotaCountdownText(3661000, 0), '01:01:01');
});

test('quota collection failure retries without clearing stored observations', async () => {
  const { context } = createDashboardHarness();
  await context.load();
  vm.runInContext('quotaSubmittedObservations.set("existing", "observation")', context);
  let requests = 0;
  context.fetch = async () => { requests++; throw new Error('offline'); };
  assert.equal(await context.collectQuotaObservations(), false);
  assert.equal(await context.collectQuotaObservations(), false);
  assert.equal(requests, 1);
  assert.equal(vm.runInContext('quotaSubmittedObservations.get("existing")', context), 'observation');
});

async function waitFor(fn) {
  for (let i = 0; i < 50; i++) {
    if (fn()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  throw new Error('condition not met');
}

// 让所有已排队的微任务和宏任务跑完。waitFor(() => true) 会在第一次检查就返回、
// 一次都不 await,所以断言「某个迟到的响应没有生效」时必须用这个真正让出执行权。
async function flushTasks(rounds = 20) {
  for (let i = 0; i < rounds; i++) await new Promise((resolve) => setImmediate(resolve));
}

test('API detail ignores responses after the range or client filter changes before its next request', async () => {
  for (const filter of ['range', 'client']) for (const fails of [false, true]) {
    const { context, document } = createDashboardHarness();
    await context.load();
    let resolve, reject;
    context.fetchApiDetailData = () => new Promise((yes, no) => { resolve = yes; reject = no; });
    const renders = [];
    context.renderApiDetailContent = (_, state) => { renders.push(state); };
    const pending = context.renderApiDetail();
    if (filter === 'range') document.getElementById('range').value = '7d';
    else vm.runInContext('selectedClientApi = { selector: "another-client" };', context);
    if (fails) reject(new Error('old filter failed'));
    else resolve({ summary: { total_requests: 999 }, recent_events: [] });
    await pending;
    assert.equal(renders.length, 1, `${filter}/${fails}: old response rendered under the new filter`);
  }
});

test('cached API detail does not cross range or client filters during a local rerender', async () => {
  for (const filter of ['range', 'client']) {
    const { context, document } = createDashboardHarness();
    await context.load();
    const oldDetail = vm.runInContext('apiDetailLastRender.detailState.detail', context);
    if (filter === 'range') document.getElementById('range').value = '7d';
    else vm.runInContext('selectedClientApi = { selector: "another-client" }; filteredSummaryData = summaryData;', context);
    const renders = [];
    context.renderApiDetailContent = (_, state) => { renders.push(state); };
    context.renderApiDetailFromCache();
    assert.notEqual(renders[0].detail, oldDetail, `${filter}: stale filter data was reused`);
  }
});

test('events ignore a response when filters change while a new summary is loading', async () => {
  for (const filter of ['range', 'client']) for (const fails of [false, true]) {
    const { context, document } = createDashboardHarness();
    await context.load();
    const before = vm.runInContext('eventsData', context);
    let resolve, reject;
    context.fetchConditionalJsonPayload = () => new Promise((yes, no) => { resolve = yes; reject = no; });
    const pending = context.renderEvents();
    if (filter === 'range') document.getElementById('range').value = '7d';
    else vm.runInContext('selectedClientApi = { selector: "another-client" };', context);
    if (fails) reject(new Error('old filter failed'));
    else resolve({ events: [], total: 999, offset: 0, limit: 50 });
    await pending;
    assert.equal(vm.runInContext('eventsData', context), before, `${filter}/${fails}: obsolete events were applied`);
  }
});

test('tiny USD costs stay visible as escaped text in dashboard HTML', async () => {
  const { context, document } = createDashboardHarness({ language: 'zh-CN' });
  await context.load();
  const tinyCost = 0.00000001;
  const formatted = context.formatUsd(tinyCost);
  assert.ok(formatted.startsWith('<'), 'fixture must exercise the threshold marker');
  const escaped = formatted.replace('<', '&lt;');
  const bars = context.barsHtml('cost', [{ name: 'model', requests: 1, cost: tinyCost }], 1, '', false);
  assert.ok(bars.includes(escaped), 'a cost must not become an HTML tag');
  const cycle = { used_percent: 50, estimated_total_usd: tinyCost, summary: { estimated_cost: tinyCost },
    model_stats: [{ model: 'tiny', total_requests: 1, total_tokens: 1, estimated_cost: tinyCost }] };
  const quota = context.quotaPeriodHtml(cycle, true);
  assert.ok(quota.includes(escaped));
  assert.ok(!quota.includes(formatted));
  vm.runInContext('summaryData.model_stats = [{ model: "tiny", total_requests: 1, total_tokens: 1000000, estimated_cost: 0.00000001 }];', context);
  context.renderModelStats();
  const models = document.getElementById('modelStats').innerHTML;
  assert.ok(models.includes(escaped));
  assert.ok(!models.includes(formatted));
});

test('dashboard loads and changes range when browser storage is unavailable', async () => {
  for (const denied of ['denyStorageReads', 'denyStorageWrites', 'denyStorageAccess']) {
    const { document, fetchCalls } = createDashboardHarness({ [denied]: true });
    await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events?')));
    const range = document.getElementById('range');
    assert.strictEqual(range.value, '24h');
    range.value = '7d';
    assert.doesNotThrow(() => range.onchange());
    await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-summary?range=7d')));
  }
});

test('invalid price inputs are rejected before saving instead of becoming free prices', async () => {
  const { context, document, fetchRequests, downloads } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  document.getElementById('priceModel').value = 'model';
  const input = document.getElementById('pricePrompt');
  for (const value of ['invalid', '1e999', '-1']) {
    input.value = value;
    await document.getElementById('savePrice').onclick();
    assert.ok(!fetchRequests.some((r) => r.options.method === 'PUT'), value);
  }
  input.value = '';
  input.validity = { badInput: true };
  await document.getElementById('savePrice').onclick();
  assert.ok(!fetchRequests.some((r) => r.options.method === 'PUT'));
  assert.strictEqual(downloads.filter((d) => d.alert).length, 4);
  for (const value of ['invalid', '1e999', NaN, Infinity]) {
    vm.runInContext('timeRules = [{ name: "rule", start: "00:00", end: "01:00" }]', context);
    vm.runInContext('timeRules[0]', context).prompt = value;
    assert.throws(() => context.validateTimeRulesClient(context.serializedTimeRules()), /价格|prices/);
  }
});

async function openPriceSettings(document) {
  const settings = document.getElementById('priceSettings');
  settings.open = true;
  if (typeof settings.ontoggle === 'function') await settings.ontoggle();
  return settings;
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
  assert.match(loadedApiDetail, /US\$0\.000401/);
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

test('dashboard keeps the full price catalogue off the initial critical path', async () => {
  const { context, document, fetchCalls } = createDashboardHarness({
    summaryUsage: { total_cost: 0.05 },
  });

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events')));
  assert.ok(!fetchCalls.some((url) => url.includes('model-prices') && url.includes('scope=catalogue')));
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$0.05');

  await openPriceSettings(document);
  await waitFor(() => fetchCalls.some((url) => url.includes('model-prices') && url.includes('scope=catalogue')));
  assert.ok(Array.from(context.priceReferenceOptions()).length > 0);
});

test('dashboard events use bounded pages and navigate by offset', async () => {
  const { document, fetchRequests } = createDashboardHarness({ summaryUsage: { total_cost: 0.05 } });
  const eventRequests = () => fetchRequests.filter((request) => request.url.includes('dashboard-events?'));

  await waitFor(() => eventRequests().length > 0);
  assert.match(eventRequests()[0].url, /limit=50/);
  assert.match(eventRequests()[0].url, /offset=0/);
  assert.strictEqual(document.getElementById('eventsPage').textContent, '第 1 / 24 页');

  await document.getElementById('eventsNext').onclick();
  assert.match(eventRequests().at(-1).url, /offset=50/);
  assert.strictEqual(document.getElementById('eventsPage').textContent, '第 2 / 24 页');
  assert.strictEqual(document.getElementById('eventsPrev').disabled, false);
});

test('dashboard expands compact health grid payloads', () => {
  const { context } = createDashboardHarness({ summaryUsage: { total_cost: 0.05 } });
  const grid = context.dashboardHealthGrid({
    generated_at: '2026-07-28T00:00:00Z',
    health_grid_v2: {
      start: '2026-07-21T00:00:00Z',
      step_seconds: 900,
      count: 3,
      slots: [[1, 4, 2]],
    },
  });

  assert.strictEqual(grid.length, 3);
  assert.deepStrictEqual({ success: grid[1].success, failure: grid[1].failure, total: grid[1].total }, { success: 4, failure: 2, total: 6 });
  assert.strictEqual(grid[1].start, '2026-07-21T00:15:00.000Z');
  assert.strictEqual(grid[2].total, 0);
});

test('dashboard skips rerender when summary polling returns 304', async () => {
  const { context, fetchCalls } = createDashboardHarness({ dashboardEtags: true, summaryUsage: { total_cost: 0.05 } });
  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events')));
  const original = context.rerender;
  let rerenders = 0;
  context.rerender = async function(options) { rerenders++; return original(options) };

  await context.load();

  assert.strictEqual(rerenders, 0);
});

test('dashboard skips rerender when base and client-filtered summaries return 304', async () => {
  const { context, fetchCalls } = createDashboardHarness({ dashboardEtags: true, summaryUsage: { total_cost: 0.05 } });
  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events')));
  await context.selectClientApiCard('api_key_hash:client-a', [{ selector: 'api_key_hash:client-a', name: 'client-a' }]);
  const original = context.rerender;
  let rerenders = 0;
  context.rerender = async function(options) { rerenders++; return original(options) };

  await context.load();

  assert.strictEqual(rerenders, 0);
});

test('dashboard ignores an older events response that finishes after a newer page', async () => {
  const { context, document, fetchCalls } = createDashboardHarness({ summaryUsage: { total_cost: 0.05 } });
  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events')));
  const originalFetch = context.fetchConditionalJsonPayload;
  const pending = [];
  context.fetchConditionalJsonPayload = function(cacheKey, url, options) {
    if (!String(url).includes('dashboard-events?')) return originalFetch(cacheKey, url, options);
    return new Promise((resolve) => pending.push({ url: String(url), resolve }));
  };

  const older = document.getElementById('eventsNext').onclick();
  const newer = document.getElementById('eventsNext').onclick();
  await waitFor(() => pending.length === 2);
  pending[1].resolve({ total: 1200, limit: 50, offset: 100, events: [{ timestamp: new Date().toISOString(), model: 'newer', tokens: {} }] });
  await newer;
  pending[0].resolve({ total: 1200, limit: 50, offset: 50, events: [{ timestamp: new Date().toISOString(), model: 'older', tokens: {} }] });
  await older;

  assert.strictEqual(document.getElementById('eventsPage').textContent, '第 3 / 24 页');
  assert.match(document.getElementById('events').innerHTML, /newer/);
  assert.doesNotMatch(document.getElementById('events').innerHTML, /older/);
});

test('dashboard blob downloads keep object URLs alive for Safari', () => {
  const { context, document, downloads, timeoutDelays } = createDashboardHarness();
  context.download('usage.csv', 'a,b\n1,2', 'text/csv;charset=utf-8');

  assert.ok(downloads.some((d) => d.text === 'a,b\n1,2' && d.type === 'text/csv;charset=utf-8'));
  assert.ok(document.body.children.some((child) => child.download === 'usage.csv' && child.href === 'blob:fake'));
  assert.strictEqual(timeoutDelays.at(-1), 60000);
});

test('conditional cache does not let an older response replace newer data', async () => {
  const { context } = createDashboardHarness();
  await context.load();
  const pending = [];
  context.fetchJsonPayloadWithMeta = () => new Promise((resolve) => pending.push(resolve));
  const older = context.fetchConditionalJsonPayloadWithMeta('race', '/race');
  const newer = context.fetchConditionalJsonPayloadWithMeta('race', '/race');
  pending[1]({ statusCode: 200, headers: { ETag: 'new' }, data: { version: 2 } });
  await newer;
  pending[0]({ statusCode: 200, headers: { ETag: 'old' }, data: { version: 1 } });
  await older;
  context.fetchJsonPayloadWithMeta = async () => ({ statusCode: 304, headers: {} });
  const cached = await context.fetchConditionalJsonPayloadWithMeta('race', '/race');
  assert.strictEqual(cached.data.version, 2);
  assert.strictEqual(cached.etag, 'new');
});

test('filtered summary ignores older success and error for the same selection', async () => {
  const { context } = createDashboardHarness();
  await context.load();
  vm.runInContext('selectedClientApi = { selector: "test-client" }', context);
  const pending = [];
  context.fetchConditionalJsonPayloadWithMeta = () => new Promise((resolve, reject) => pending.push({ resolve, reject }));
  for (const failOlder of [false, true]) {
    const start = pending.length;
    const older = context.refreshFilteredSummary();
    const newer = context.refreshFilteredSummary();
    pending[start + 1].resolve({ data: { version: 2 }, notModified: false });
    await newer;
    if (failOlder) pending[start].reject(new Error('stale failure'));
    else pending[start].resolve({ data: { version: 1 }, notModified: false });
    await older;
    assert.strictEqual(vm.runInContext('filteredSummaryData.version', context), 2);
    assert.strictEqual(vm.runInContext('filteredSummaryError', context), null);
  }
});

test('API detail cache evicts old selections and conditional request tokens are released', async () => {
  const { context } = createDashboardHarness();
  await context.load();
  for (let i = 0; i < 80; i++) context.cacheApiDetail('selection-' + i, { version: i });
  assert.strictEqual(vm.runInContext('apiDetailCache.size', context), 32);
  assert.strictEqual(vm.runInContext('apiDetailCache.has("selection-0")', context), false);
  assert.strictEqual(vm.runInContext('apiDetailCache.get("selection-79").version', context), 79);
  context.fetchJsonPayloadWithMeta = async () => { throw new Error('network error'); };
  await assert.rejects(context.fetchConditionalJsonPayloadWithMeta('failure', '/failure'), /network error/);
  assert.strictEqual(vm.runInContext('conditionalPayloadRequests.size', context), 0);
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

test('dashboard fallback keeps ambiguous imported hashless client API groups separate', () => {
  const { context } = createDashboardHarness();
  const rows = context.coalesceLegacyHashlessClientApiStats([
    { api_key: 'sk******xx', api_key_hash: '', total_requests: 1, total_tokens: 40, models: [] },
    { api_key: 'sk******xx', api_key_hash: 'hash-a', total_requests: 1, total_tokens: 120, models: [] },
    { api_key: 'sk******xx', api_key_hash: 'hash-b', total_requests: 1, total_tokens: 60, models: [] },
  ]);

  assert.strictEqual(rows.length, 3);
  assert.strictEqual(rows[0].api_key_hash, '');
  assert.deepStrictEqual(rows.map((row) => row.total_requests), [1, 1, 1]);
  assert.deepStrictEqual(rows.map((row) => row.total_tokens), [40, 120, 60]);
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
  await openPriceSettings(document);
  setLanguage('en', { persisted: true });

  await waitFor(() => document.documentElement.lang === 'en');
  await waitFor(() => document.getElementById('successText').textContent.includes('Success requests:'));
  await waitFor(() => document.getElementById('eventsCount').textContent.includes('Total 1,200, showing 50'));
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

test('chunk export retries immutable offsets and decodes split UTF-8', async () => {
  const { context, document } = createDashboardHarness({ managementKey: 'chunk-management-key' });
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  const job = { id: 'chunks', status: 'succeeded', body_bytes: 10, chunk_size: 2, etag: 'W/"file-version"', total: 1, exported: 1, content_type: 'text/csv' };
  // Independent CRC-32/IEEE fixtures generated with Python zlib.crc32.
  const fixtures = [['5Lg=', 'be711fa9'], ['rfA=', 'e7cd2247'], ['n5k=', '3ed3afca'], ['guY=', '0105af7b'], ['loc=', '151e29e0']];
  const offsets = [];
  let removed = 0;
  let failed = false;
  context.createExportJob = async () => job;
  context.deleteExportJob = async (id) => { assert.strictEqual(id, job.id); removed++; };
  context.delay = async () => {};
  context.fetchJsonPayload = async (url, options) => {
    const parsed = new URL(url, 'http://test.local');
    const offset = Number(parsed.searchParams.get('offset'));
    offsets.push(offset);
    assert.strictEqual(parsed.searchParams.get('version'), job.etag);
    assert.strictEqual(options.headers.Authorization, 'Bearer chunk-management-key');
    if (offset === 2 && !failed) { failed = true; throw new Error('transient read failure'); }
    return { offset, total: 10, etag: job.etag, data: fixtures[offset / 2][0], checksum_crc32: fixtures[offset / 2][1] };
  };
  const result = await context.fetchExportJobResult(new URLSearchParams());
  assert.strictEqual(result.data, '中🙂文');
  assert.deepStrictEqual(offsets, [0, 2, 2, 4, 6, 8]);
  assert.strictEqual(result.headers['X-Exported-Count'][0], '1');
  assert.strictEqual(removed, 1);
  assert.strictEqual(context.exportChunkChecksum(Buffer.from('123456789')), 'cbf43926');
});

test('chunk export rejects corruption, missing bytes and changed versions and deletes its job', async () => {
  for (const invalid of [{ checksum_crc32: '00000000' }, { data: '' }, { offset: 2 }, { total: 3 }, { etag: 'another-file' }]) {
    const { context, document } = createDashboardHarness();
    await waitFor(() => document.getElementById('apiSelect').value === 'openai');
    const job = { id: 'bad-chunk', status: 'succeeded', body_bytes: 2, chunk_size: 2, etag: 'version' };
    let removed = 0;
    context.createExportJob = async () => job;
    context.deleteExportJob = async () => { removed++; };
    context.fetchJsonPayload = async () => Object.assign({ offset: 0, total: 2, etag: job.etag, data: '5Lg=', checksum_crc32: 'be711fa9' }, invalid);
    await assert.rejects(context.fetchExportJobResult(new URLSearchParams()));
    assert.strictEqual(removed, 1);
  }
});

test('chunk export uses an opaque version after stock CPA escapes ETag quotes', async () => {
  for (const chunkVersion of ['a'.repeat(64), 'another-file', undefined]) {
    const { context, document } = createDashboardHarness();
    await waitFor(() => document.getElementById('apiSelect').value === 'openai');
    const job = { id: 'transport-version', status: 'succeeded', body_bytes: 2, chunk_size: 2,
      etag: 'W/&#34;file-version&#34;', version: 'a'.repeat(64) };
    let removed = 0;
    context.createExportJob = async () => job;
    context.deleteExportJob = async () => { removed++; };
    context.fetchJsonPayload = async (url) => {
      assert.strictEqual(new URL(url, 'http://test.local').searchParams.get('version'), job.version);
      return { offset: 0, total: 2, etag: job.etag, version: chunkVersion, data: 'W10=', checksum_crc32: context.exportChunkChecksum(Buffer.from('[]')) };
    };
    if (chunkVersion === job.version) {
      const result = await context.fetchExportJobResult(new URLSearchParams(), true);
      assert.strictEqual(result.data, '[]');
    } else {
      await assert.rejects(context.fetchExportJobResult(new URLSearchParams()));
    }
    assert.strictEqual(removed, 1);
  }
});

test('CSV and JSON buttons download checked Blob parts without whole-file text or JSON conversion', async () => {
  for (const kind of ['csv', 'json']) {
    for (const api of [false, true]) {
      const { context, document } = createDashboardHarness();
      await waitFor(() => document.getElementById('apiSelect').value === 'openai');
      const payload = kind === 'csv' ? 'model,tokens\n中文🙂,9007199254740993\n' : '[\n  {"model":"中文🙂","tokens":9007199254740993}\n]';
      const bytes = Buffer.from(payload);
      const job = { id: 'file', status: 'succeeded', format: kind, json_rows: kind === 'json',
        body_bytes: bytes.length, chunk_size: 5, etag: 'immutable-file', total: 7, exported: 1, truncated: true,
        content_type: kind === 'csv' ? 'text/csv; charset=utf-8' : 'application/json; charset=utf-8' };
      const blobs = [];
      let deleted = 0;
      context.Blob = Blob;
      context.TextDecoder = class { constructor() { throw new Error('file download must not decode text'); } };
      context.URL.createObjectURL = (blob) => { blobs.push(blob); return 'blob:checked-file'; };
      context.createExportJob = async (params) => {
        assert.strictEqual(params.get('format'), kind);
        if (kind === 'json') assert.strictEqual(params.get('json_rows'), 'true');
        if (api) assert.ok(params.get('api'));
        return job;
      };
      context.deleteExportJob = async () => { deleted++; };
      context.fetchJsonPayload = async (url) => {
        const query = new URL(url, 'http://test').searchParams;
        const offset = Number(query.get('offset'));
        const data = bytes.subarray(offset, offset + Number(query.get('length')));
        return { offset, total: bytes.length, etag: job.etag, data: data.toString('base64'), checksum_crc32: context.exportChunkChecksum(data) };
      };
      const button = (api ? 'exportApi' : 'exportRows') + (kind === 'csv' ? 'Csv' : 'Json');
      await document.getElementById(button).onclick();
      assert.strictEqual(blobs.length, 1);
      assert.ok(blobs[0] instanceof Blob);
      assert.strictEqual(await blobs[0].text(), payload, 'bytes, UTF-8 boundaries and large integers must survive');
      assert.strictEqual(deleted, 1);
      assert.ok(document.body.children.some((el) => el.download && el.download.endsWith('.' + kind)));
    }
  }
});

test('a backend ignoring json_rows keeps the compatible text-envelope path', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  const bytes = Buffer.from('{"events":[],"total":0}');
  const job = { id: 'old-json', status: 'succeeded', format: 'json', body_bytes: bytes.length,
    chunk_size: 256 * 1024, etag: 'old-envelope', content_type: 'application/json' };
  context.createExportJob = async () => job;
  context.deleteExportJob = async () => {};
  context.fetchJsonPayload = async () => ({ offset: 0, total: bytes.length, etag: job.etag,
    data: bytes.toString('base64'), checksum_crc32: context.exportChunkChecksum(bytes) });
  const result = await context.fetchExportJobResult(new URLSearchParams({ json_rows: 'true' }), true);
  assert.strictEqual(result.data, bytes.toString());
  assert.strictEqual(result.headers['X-Total-Count'][0], '0');
});

test('dashboard bare model input can use a price-source value as an override starting point', async () => {
  const { context, document } = createDashboardHarness({
    prices: { 'gpt-4.1': { prompt: 1.25, completion: 10, cache: 0.125, cache_write: 1.5 } },
    manualPrices: {},
  });

  await openPriceSettings(document);
  await waitFor(() => Array.from(context.priceReferenceOptions()).includes('gpt-4.1'));
  assert.doesNotMatch(document.getElementById('priceModelOptions').innerHTML, /gpt-4\.1/);
  document.getElementById('priceModel').value = 'gpt-4.1';
  document.getElementById('priceModel').onchange();

  assert.strictEqual(document.getElementById('pricePrompt').value, 1.25);
  assert.strictEqual(document.getElementById('priceCompletion').value, 10);
  assert.strictEqual(document.getElementById('priceCache').value, 0.125);
  assert.strictEqual(document.getElementById('priceCacheWrite').value, 1.5);
});

test('dashboard separates upstream price settings from the read-only price source lookup', async () => {
  const { context, document } = createDashboardHarness({
    prices: {
      'gpt-4.1': { prompt: 1.25, completion: 10, cache: 0.125, cache_write: 1.5 },
      'openai/gpt-4.1': { prompt: 1.25, completion: 10, cache: 0.125, cache_write: 1.5 },
      'openrouter/gpt-4.1': { prompt: 9, completion: 90, cache: 0.9, cache_write: 0 },
      'catalog-only/model-x': { prompt: 4, completion: 12, cache: 1, cache_write: 2 },
    },
    manualPrices: {
      'openai/gpt-4.1': { prompt: 3, completion: 11, cache: 0.3, cache_write: 1 },
    },
    summaryModelProviders: [{ provider: 'openai' }, { provider: 'openrouter' }],
  });

  await openPriceSettings(document);
  await waitFor(() => Array.from(context.priceReferenceOptions()).includes('catalog-only/model-x'));

  assert.deepStrictEqual(Array.from(context.priceModelOptions()), ['openai/gpt-4.1', 'openrouter/gpt-4.1']);
  const settingOptions = document.getElementById('priceModelOptions').innerHTML;
  assert.match(settingOptions, /openai\/gpt-4\.1/);
  assert.match(settingOptions, /openrouter\/gpt-4\.1/);
  assert.doesNotMatch(settingOptions, /catalog-only\/model-x/);

  document.getElementById('priceReferenceModel').value = 'catalog-only/model-x';
  document.getElementById('priceReferenceModel').onchange();
  assert.match(document.getElementById('priceReferenceInfo').innerHTML, /catalog-only\/model-x/);
  assert.match(document.getElementById('priceReferenceInfo').innerHTML, /4\.0000/);
  assert.match(document.getElementById('priceReferenceInfo').innerHTML, /价格源/);
  assert.strictEqual(document.getElementById('pricePrompt').value, '');

  document.getElementById('priceReferenceModel').value = 'openai/gpt-4.1';
  document.getElementById('priceReferenceModel').onchange();
  assert.match(document.getElementById('priceReferenceInfo').innerHTML, /手动设置/);
  assert.match(document.getElementById('priceReferenceInfo').innerHTML, /3\.0000/);
});

test('price lookup filters a bounded custom result list and supports keyboard selection', async () => {
  const prices = {
    'openai/gpt-4.1': { prompt: 1, completion: 2 },
    'openrouter/gpt-4.1': { prompt: 3, completion: 4 },
    'catalog-only/model-x': { prompt: 5, completion: 6 },
  };
  for (let i = 0; i < 105; i++) prices['bulk/model-' + i] = { prompt: i, completion: i + 1 };
  const { context, document } = createDashboardHarness({ prices, manualPrices: {} });

  await openPriceSettings(document);
  await waitFor(() => Array.from(context.priceReferenceOptions()).includes('openrouter/gpt-4.1'));
  const input = document.getElementById('priceReferenceModel');
  const results = document.getElementById('priceReferenceOptions');

  input.value = 'OPENROUTER';
  input.oninput();
  assert.match(results.innerHTML, /openrouter\/gpt-4\.1/);
  assert.doesNotMatch(results.innerHTML, /openai\/gpt-4\.1|catalog-only\/model-x/);
  assert.strictEqual(input.getAttribute('aria-expanded'), 'true');

  input.onkeydown({ key: 'ArrowDown', preventDefault() {} });
  input.onkeydown({ key: 'Enter', preventDefault() {} });
  assert.strictEqual(input.value, 'openrouter/gpt-4.1');
  assert.match(document.getElementById('priceReferenceInfo').innerHTML, /3\.0000/);

  input.value = 'bulk/';
  input.oninput();
  assert.match(results.innerHTML, /bulk\/model-0/);
  assert.match(results.innerHTML, /显示前 100 项（共 105 项）/);
  assert.strictEqual((results.innerHTML.match(/class="searchComboOption/g) || []).length, 100);

  input.value = 'bulk/model-104';
  input.oninput();
  assert.match(results.innerHTML, /bulk\/model-104/);
  assert.strictEqual((results.innerHTML.match(/class="searchComboOption/g) || []).length, 1);
});

test('dashboard saves provider-scoped model prices as provider/modelname keys', async () => {
  const { context, document, fetchRequests } = createDashboardHarness({
    manualPrices: {},
  });

  await openPriceSettings(document);
  await waitFor(() => Array.from(context.priceReferenceOptions()).includes('gpt-4.1'));
  assert.strictEqual(document.getElementById('priceModelOptions').innerHTML, '');
  document.getElementById('priceModel').value = 'openrouter/gpt-4.1';
  document.getElementById('pricePrompt').value = '7';
  document.getElementById('priceCompletion').value = '21';
  document.getElementById('priceCache').value = '0.7';
  document.getElementById('priceCacheWrite').value = '1.4';
  await document.getElementById('savePrice').onclick();

  const put = fetchRequests.find((request) => request.url.includes('model-prices') && request.options.method === 'PUT');
  assert.ok(put);
  assert.deepStrictEqual(JSON.parse(put.options.body), {
    model: 'openrouter/gpt-4.1',
    price: { prompt: 7, completion: 21, cache: 0.7, cache_write: 1.4 },
  });
  assert.match(document.getElementById('priceList').innerHTML, /openrouter\/gpt-4\.1/);
});

test('closing price lookup discards pending search results', async () => {
  for (const action of ['Escape', 'Tab', 'select']) {
    const { context, document } = createDashboardHarness();
    await waitFor(() => document.getElementById('apiSelect').value === 'openai');
    await openPriceSettings(document);
    let runSearch, resolveSearch;
    context.setTimeout = (fn, delay) => { if (delay === 180) runSearch = fn; return 100; };
    context.fetchModelPrices = () => new Promise((resolve) => { resolveSearch = resolve; });
    const input = document.getElementById('priceReferenceModel');
    input.value = 'gpt';
    input.oninput();
    const pending = runSearch();
    if (action === 'select') context.selectPriceReferenceModel('gpt-4.1');
    else input.onkeydown({ key: action, preventDefault() {} });
    assert.strictEqual(document.getElementById('priceReferenceOptions').hidden, true);
    resolveSearch({ prices: { stale: { prompt: 9 } }, manual_prices: {} });
    await pending;
    assert.strictEqual(document.getElementById('priceReferenceOptions').hidden, true, action);
    assert.ok(!Array.from(context.priceReferenceOptions()).includes('stale'), action);
  }
});

test('price lookup shows the bare manual fallback used by a provider-scoped model', async () => {
  const { context, document, setLanguage } = createDashboardHarness({
    prices: {
      'openrouter/openai/same-model': { prompt: 9, completion: 90, cache: 0.9, cache_write: 0 },
    },
    manualPrices: {
      'same-model': { prompt: 3, completion: 6, cache: 0.3, cache_write: 0.6 },
    },
  });

  await openPriceSettings(document);
  await waitFor(() => Array.from(context.priceReferenceOptions()).includes('openrouter/openai/same-model'));
  document.getElementById('priceReferenceModel').value = 'openrouter/openai/same-model';
  document.getElementById('priceReferenceModel').onchange();
  assert.match(document.getElementById('priceReferenceInfo').innerHTML, /手动设置 · 匹配 same-model/);
  assert.match(document.getElementById('priceReferenceInfo').innerHTML, /3\.0000/);

  setLanguage('en');
  await waitFor(() => document.getElementById('priceReferenceInfo').innerHTML.includes('Manual setting'));
  assert.strictEqual(document.getElementById('priceReferenceModel').value, 'openrouter/openai/same-model');
  assert.match(document.getElementById('priceReferenceInfo').innerHTML, /matched same-model/);
});

test('price lookup handles empty data and escapes catalogue model keys', async () => {
  const empty = createDashboardHarness({ prices: {}, manualPrices: {} });
  await openPriceSettings(empty.document);
  await waitFor(() => empty.document.getElementById('priceReferenceInfo').innerHTML.includes('当前没有可查询的模型价格'));
  assert.strictEqual(empty.document.getElementById('priceReferenceModel').disabled, true);

  const unsafeModel = '<img src=x onerror=alert(1)>';
  const unsafe = createDashboardHarness({
    prices: { [unsafeModel]: { prompt: 2, completion: 4, cache: 1, cache_write: 0 } },
    manualPrices: {},
  });
  await openPriceSettings(unsafe.document);
  await waitFor(() => Array.from(unsafe.context.priceReferenceOptions()).includes(unsafeModel));
  assert.match(unsafe.document.getElementById('priceReferenceOptions').innerHTML, /&lt;img/);
  assert.doesNotMatch(unsafe.document.getElementById('priceReferenceOptions').innerHTML, /<img/);
  unsafe.document.getElementById('priceReferenceModel').value = unsafeModel;
  unsafe.document.getElementById('priceReferenceModel').onchange();
  assert.match(unsafe.document.getElementById('priceReferenceInfo').innerHTML, /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.doesNotMatch(unsafe.document.getElementById('priceReferenceInfo').innerHTML, /<img/);
});

test('model price settings use a details element that is closed by default', () => {
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const match = html.match(/<details\b[^>]*id="priceSettings"[^>]*>/);
  assert.ok(match, 'expected model price settings to use a details element');
  assert.doesNotMatch(match[0], /\sopen(?:\s|=|>)/i);
  const detailsStart = html.indexOf(match[0]);
  const detailsEnd = html.indexOf('</details>', detailsStart);
  const lookupIndex = html.indexOf('id="priceReferenceModel"');
  assert.ok(lookupIndex > detailsStart && lookupIndex < detailsEnd, 'expected price lookup inside the collapsible settings panel');
  assert.match(html, /id="priceReferenceModel"[^>]*role="combobox"/);
  assert.doesNotMatch(html, /<select[^>]*id="priceReferenceModel"/);
});

test('price rerenders preserve the settings details open state', async () => {
  const { context, document } = createDashboardHarness({ manualPrices: {} });
  await waitFor(() => Array.from(context.priceReferenceOptions()).includes('gpt-4.1'));
  const settings = document.getElementById('priceSettings');
  settings.open = true;
  context.renderPrices();
  assert.strictEqual(settings.open, true);
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

test('dashboard api detail shows reasoning effort, endpoint and generation speed', () => {
  const { context } = createDashboardHarness();
  const recentHtml = context.apiDetailRecentHtml([{
    timestamp_ms: Date.UTC(2026, 6, 21, 12, 0, 0),
    model: 'gpt-5.5',
    thinking: { intensity: 'xhigh' },
    endpoint: '/v1/responses',
    failed: false,
    stream: true,
    latency_ms: 11000,
    ttft_ms: 1000,
    total_tokens: 458,
    tokens: { input_tokens: 100, output_tokens: 358, total_tokens: 458 },
    source: 'codex',
    provider: 'codex',
  }], false, null);

  assert.match(recentHtml, />推理强度</);
  assert.match(recentHtml, />端点</);
  assert.match(recentHtml, />生成速度</);
  assert.match(recentHtml, />xhigh</);
  assert.match(recentHtml, />\/v1\/responses</);
  assert.match(recentHtml, />35\.8 t\/s</);
});

test('dashboard api detail suppresses invalid generation speeds', () => {
  const { context } = createDashboardHarness();
  assert.strictEqual(context.generationSpeedText({ stream: true, tokens: { output_tokens: 10 }, latency_ms: 1000, ttft_ms: 1000 }), '-');
  assert.strictEqual(context.generationSpeedText({ tokens: { output_tokens: 0 }, latency_ms: 1000, ttft_ms: 100 }), '-');
  assert.strictEqual(context.generationSpeedText({ tokens: { output_tokens: 10 }, latency_ms: 0, ttft_ms: 0 }), '-');
  assert.strictEqual(context.generationSpeedText({ tokens: { output_tokens: 36 }, latency_ms: 1009, ttft_ms: 1007 }), '35.7 t/s');
});

test('dashboard translations include recent request metadata columns', () => {
  const { context } = createDashboardHarness();
  for (const language of ['zh-CN', 'zh-TW', 'en', 'ru']) {
    for (const key of ['col_reasoning_effort', 'col_endpoint', 'col_generation_speed']) {
      assert.ok(context.I18N_MAP[language][key], language + ' missing ' + key);
    }
  }
});

test('dashboard translations include price lookup and scoped setting copy', () => {
  const { context } = createDashboardHarness();
  const keys = [
    'price_reference_title',
    'price_reference_subtle',
    'price_reference_select',
    'price_reference_search_placeholder',
    'price_reference_no_match',
    'price_reference_result_limit',
    'price_reference_none',
    'price_source_manual',
    'price_source_default',
    'price_match_key',
    'price_summary_hint',
    'price_settings_hint',
    'manual_price_list_title',
  ];
  for (const language of ['zh-CN', 'zh-TW', 'en', 'ru']) {
    for (const key of keys) assert.ok(context.I18N_MAP[language][key], language + ' missing ' + key);
  }
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

test('dashboard api detail uses server costs without loading model prices', () => {
  const { context, document } = createDashboardHarness();
  vm.runInContext(`
    modelPrices = {};
    manualModelPrices = {};
  `, context);
  context.renderApiDetailContent({ failure_count: 0, models: {} }, {
    loading: false,
    detail: {
      api: 'openai',
      summary: { total_requests: 2, success_count: 2, failure_count: 0, total_tokens: 1500000, estimated_cost: 6 },
      model_stats: [{
        model: 'gpt-4.1',
        total_requests: 2,
        success_count: 2,
        failure_count: 0,
        total_tokens: 1500000,
        input_tokens: 1000000,
        output_tokens: 500000,
        estimated_cost: 6,
      }],
      source_stats: [{ source: 'openai', provider: 'openai', total_requests: 2 }],
      error_stats: [],
      recent_events: [],
    },
  });

  const html = document.getElementById('apiDetail').innerHTML;
  assert.match(html, /gpt-4\.1<span class="barTokens">1\.5M<\/span><span class="barCost">US\$6\.00<\/span>/);
  assert.match(html, /总花费<\/div><div class="metricValue">US\$6\.00<\/div>/);
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

  assert.match(html, /<div class="panel full">\s*<div class="panelHead">\s*<div><h2 data-i18n="upstream_title">/);
  assert.match(html, /<div class="panel full">\s*<div class="panelHead"><h2 data-i18n="model_stats_title">/);
  assert.match(css, /\.detailActivityGrid\{grid-template-columns:minmax\(0,1fr\)\}/);
  assert.match(css, /\.barLabel\{[^}]*overflow-wrap:anywhere;[^}]*word-break:break-word/);
});

test('dashboard hides zero-success upstream APIs behind a toggle', async () => {
  const { document } = createDashboardHarness({
    extraUpstreamApis: {
      'ghost-free': { total_requests: 6, failure_count: 6, total_tokens: 0, models: {} },
      'healthy': { total_requests: 3, success_count: 3, failure_count: 0, total_tokens: 30, models: { 'gpt-4.1': { total_requests: 3, success_count: 3 } } },
    },
  });
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('openai'));

  const toggle = document.__upstreamFilterButtons.hideZeroUpstream;
  assert.strictEqual(toggle.disabled, false, 'toggle stays enabled while a 0% upstream exists');
  assert.match(document.getElementById('apiStats').innerHTML, /ghost-free/);
  assert.match(document.getElementById('apiStats').innerHTML, /healthy/);

  toggle.onclick();
  assert.doesNotMatch(document.getElementById('apiStats').innerHTML, /ghost-free/);
  assert.match(document.getElementById('apiStats').innerHTML, /healthy/);
  assert.match(document.getElementById('apiStats').innerHTML, /openai/);
  assert.strictEqual(toggle.getAttribute('aria-pressed'), 'true');
  assert.strictEqual(toggle.classList.contains('active'), true, 'active class drives the pressed styling');
  assert.match(toggle.title, /隐藏成功率为 0 的上游接口/);
  assert.match(document.getElementById('upstreamFilterStatus').textContent, /已隐藏 1 个成功率 0% 的上游接口/);
  // 被隐藏的上游仍然保留在下拉和详情里,否则用户无法再查看或导出它。
  assert.match(document.getElementById('apiSelect').innerHTML, /ghost-free/);

  // 从下拉选中一个被隐藏的上游,再重复渲染:选中项和详情不能被悄悄换掉。
  const select = document.getElementById('apiSelect');
  select.value = 'ghost-free';
  select.onchange();
  assert.strictEqual(select.value, 'ghost-free');
  assert.doesNotMatch(document.getElementById('apiStats').innerHTML, /ghost-free/);
  assert.strictEqual(document.getElementById('apiDetailTitle').textContent, 'ghost-free');

  // 开关只改表格:再次切换不能顺手把当前选中的上游重置成第一行。
  toggle.onclick();
  assert.match(document.getElementById('apiStats').innerHTML, /ghost-free/);
  assert.strictEqual(select.value, 'ghost-free');
  assert.strictEqual(document.getElementById('apiDetailTitle').textContent, 'ghost-free');
  assert.strictEqual(toggle.getAttribute('aria-pressed'), 'false');
  assert.strictEqual(toggle.classList.contains('active'), false);
  assert.strictEqual(document.getElementById('upstreamFilterStatus').textContent, '');
});

test('dashboard never hides upstream APIs that have no requests yet', async () => {
  const { document } = createDashboardHarness({
    extraUpstreamApis: {
      'ghost-free': { total_requests: 6, failure_count: 6, total_tokens: 0, models: {} },
      // 没有请求的上游按 100% 计,不应该被当成「失败率 100%」隐藏掉。
      'not-used-yet': { total_requests: 0, success_count: 0, failure_count: 0, total_tokens: 0, models: {} },
    },
  });
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('ghost-free'));

  document.__upstreamFilterButtons.hideZeroUpstream.onclick();
  const html = document.getElementById('apiStats').innerHTML;
  assert.doesNotMatch(html, /ghost-free/);
  assert.match(html, /not-used-yet/);
  assert.match(html, /100\.0%/);
  assert.match(document.getElementById('upstreamFilterStatus').textContent, /已隐藏 1 /);
  // 即便表格里还有可见行,下游的下拉也不能被当成空列表禁用。
  assert.strictEqual(document.getElementById('apiSelect').disabled, false);
});

test('dashboard resets the zero-success toggle state when no upstream data is available', async () => {
  // summaryUsage.apis = null 会让 panelData.usage.apis 变成 falsy,走 no-data 分支。
  const { document } = createDashboardHarness({ summaryUsage: { apis: null } });
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('暂无接口数据'));

  const toggle = document.__upstreamFilterButtons.hideZeroUpstream;
  assert.strictEqual(toggle.disabled, true);
  assert.strictEqual(toggle.getAttribute('aria-pressed'), 'false');
  assert.strictEqual(document.getElementById('upstreamFilterStatus').textContent, '');
});

test('dashboard keeps upstream APIs with unknown success counts visible', async () => {
  // 旧快照可能缺 success_count/failure_count，未知成功率不能被当成 0% 隐藏。
  const { document } = createDashboardHarness({
    extraUpstreamApis: {
      // 显式 undefined 才能覆盖 harness 的默认 0,模拟真正缺字段的旧快照。
      'legacy-partial': { total_requests: 5, success_count: undefined, failure_count: undefined, models: {} },
    },
  });
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('legacy-partial'));

  const html = document.getElementById('apiStats').innerHTML;
  assert.doesNotMatch(html, /NaN/);
  assert.match(html, /selectedRow[^>]*>[\s\S]*?legacy-partial/);
  assert.match(html, /bad">-</);
  document.__upstreamFilterButtons.hideZeroUpstream.onclick();
  assert.match(document.getElementById('apiStats').innerHTML, /legacy-partial/);
});

test('dashboard only hides an exact 0% success rate, not merely a low one', async () => {
  // 边界的另一半:成功率只要不是恰好 0 就不该被隐藏,否则「隐藏 0%」会变成
  // 「隐藏所有失败过的接口」。用 1/1000 构造 0.1%。
  const { document } = createDashboardHarness({
    extraUpstreamApis: {
      'barely-working': { total_requests: 1000, success_count: 1, failure_count: 999, total_tokens: 10, models: {} },
      'ghost-free': { total_requests: 6, failure_count: 6, total_tokens: 0, models: {} },
    },
  });
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('barely-working'));

  document.__upstreamFilterButtons.hideZeroUpstream.onclick();
  const html = document.getElementById('apiStats').innerHTML;
  assert.match(html, /barely-working/);
  assert.match(html, /0\.1%/);
  assert.doesNotMatch(html, /ghost-free/);
  assert.match(document.getElementById('upstreamFilterStatus').textContent, /已隐藏 1 /);
});

test('dashboard drops an in-flight upstream detail response when the selection changes', async () => {
  const { context, document } = createDashboardHarness({
    extraUpstreamApis: { 'slower-api': { total_requests: 1, success_count: 1, failure_count: 0, models: {} } },
  });
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('slower-api'));
  const originalFetch = context.fetchApiDetailData;
  let releaseSlow = null;
  // 只把 slower-api 的详情请求挂在途中,openai 的请求正常返回。
  context.fetchApiDetailData = function (api) {
    if (api !== 'slower-api') return originalFetch(api);
    return new Promise((resolve) => { releaseSlow = () => resolve(originalFetch(api)); });
  };

  const select = document.getElementById('apiSelect');
  select.value = 'slower-api';
  select.onchange();
  await waitFor(() => typeof releaseSlow === 'function');

  // 在 slower-api 的响应到达前切回 openai,并让它先渲染完成。
  select.value = 'openai';
  select.onchange();
  await waitFor(() => document.getElementById('apiDetailTitle').textContent === 'openai');
  const settled = document.getElementById('apiDetail').innerHTML;

  // 迟到的 slower-api 响应不能把标题或正文改回旧选中项。
  releaseSlow();
  await flushTasks();
  assert.strictEqual(select.value, 'openai');
  assert.strictEqual(document.getElementById('apiDetailTitle').textContent, 'openai');
  assert.strictEqual(document.getElementById('apiDetail').innerHTML, settled);
  assert.doesNotMatch(document.getElementById('apiDetail').innerHTML, /slower-api-model/);
});

test('dashboard explains when every upstream API was hidden', async () => {
  const { document } = createDashboardHarness({
    summaryUsage: { apis: {} },
    extraUpstreamApis: {
      'ghost-a': { total_requests: 2, failure_count: 2, total_tokens: 0, models: {} },
      'ghost-b': { total_requests: 4, failure_count: 4, total_tokens: 0, models: {} },
    },
  });
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('ghost-a'));

  document.__upstreamFilterButtons.hideZeroUpstream.onclick();
  const html = document.getElementById('apiStats').innerHTML;
  assert.doesNotMatch(html, /ghost-a/);
  assert.match(html, /全部 2 个上游接口成功率为 0，已全部隐藏/);
  assert.match(document.getElementById('upstreamFilterStatus').textContent, /全部 2 个上游接口成功率为 0/);
  assert.match(document.getElementById('apiSelect').innerHTML, /ghost-a/);
  // 表格里一行不剩,但下拉仍要可用:否则用户没法再选中被隐藏的接口。
  assert.strictEqual(document.getElementById('apiSelect').disabled, false);
});

test('dashboard keeps the zero-success toggle disabled without zero-success upstreams', async () => {
  const { document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('openai'));

  const toggle = document.__upstreamFilterButtons.hideZeroUpstream;
  assert.strictEqual(toggle.disabled, true);
  // 禁用状态不能让已开启的隐藏偏好无法关闭:点开后按钮必须保持可点。
  toggle.onclick();
  assert.strictEqual(toggle.disabled, false);
  assert.strictEqual(toggle.getAttribute('aria-pressed'), 'true');
  assert.match(document.getElementById('apiStats').innerHTML, /openai/);
  assert.strictEqual(document.getElementById('upstreamFilterStatus').textContent, '');
  toggle.onclick();
  assert.strictEqual(toggle.disabled, true);
  assert.strictEqual(toggle.getAttribute('aria-pressed'), 'false');
});

test('dashboard zero-success hiding covers translations and markup', () => {
  const { context } = createDashboardHarness();
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');

  assert.match(html, /<button class="btn" id="hideZeroUpstream" type="button" aria-pressed="false" data-i18n="upstream_hide_zero">/);
  for (const language of ['zh-CN', 'zh-TW', 'en', 'ru']) {
    for (const key of ['upstream_hide_zero', 'upstream_hide_zero_hint', 'upstream_hidden_count', 'upstream_all_hidden']) {
      assert.ok(context.I18N_MAP[language][key], language + ' missing ' + key);
    }
  }
});

test('dashboard persists the zero-success hiding preference across reloads', async () => {
  // 文档承诺「刷新页面后继续生效」:预置存储为 true 时,首屏就应处于已隐藏状态。
  const { context, document } = createDashboardHarness({
    hideZeroUpstream: true,
    extraUpstreamApis: { 'ghost-free': { total_requests: 6, failure_count: 6, total_tokens: 0, models: {} } },
  });
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('openai'));

  assert.strictEqual(vm.runInContext('hideZeroUpstream', context), true);
  assert.doesNotMatch(document.getElementById('apiStats').innerHTML, /ghost-free/);
  const toggle = document.__upstreamFilterButtons.hideZeroUpstream;
  assert.strictEqual(toggle.getAttribute('aria-pressed'), 'true');
  assert.strictEqual(toggle.classList.contains('active'), true);

  // 关闭后必须写回存储,否则刷新又会打开。
  toggle.onclick();
  assert.strictEqual(vm.runInContext('hideZeroUpstream', context), false);
  assert.strictEqual(context.localStorage.getItem('cpa-usage-hide-zero-upstream-v1'), 'false');
});

test('dashboard defaults the zero-success preference to off when storage is empty or denied', async () => {
  const fresh = createDashboardHarness();
  await waitFor(() => fresh.document.getElementById('apiStats').innerHTML.includes('openai'));
  assert.strictEqual(vm.runInContext('hideZeroUpstream', fresh.context), false, 'empty storage must not enable hiding');

  // 存储被拒时不能抛错,退回默认关闭。
  const denied = createDashboardHarness({ denyStorageReads: true });
  await waitFor(() => denied.document.getElementById('apiStats').innerHTML.includes('openai'));
  assert.strictEqual(vm.runInContext('hideZeroUpstream', denied.context), false);
});

test('dashboard renders sub-0.1% success rates without showing a misleading 0.0%', async () => {
  // 可见一位小数时,真实成功率 > 0 不能显示成 0.0%(那会被误读为「和 0% 一样差」)。
  const { context, document } = createDashboardHarness({
    extraUpstreamApis: {
      'tiny-rate': { total_requests: 10000, success_count: 1, failure_count: 9999, total_tokens: 5, models: {} },
      'exactly-zero': { total_requests: 6, failure_count: 6, total_tokens: 0, models: {} },
      // 0.2%:安全落在阈值之外,用来区分 0.05% 和更宽的阈值。
      'small-but-real': { total_requests: 5000, success_count: 10, failure_count: 4990, total_tokens: 5, models: {} },
    },
  });
  await waitFor(() => document.getElementById('apiStats').innerHTML.includes('tiny-rate'));

  const render = (api) => vm.runInContext(
    'JSON.stringify((function(){var p=dashboardPanelData();var r=upstreamApiRows(p.usage).filter(function(x){return x.api==="' + api + '"})[0];' +
    'return {rate: r.successRate, text: upstreamSuccessRateText(r.successRate), hidden: isZeroSuccessRate(r)}})())', context);
  const tiny = JSON.parse(render('tiny-rate'));
  const zero = JSON.parse(render('exactly-zero'));
  assert.strictEqual(tiny.text, '<0.1%', '非零的极小成功率不能显示为 0.0%');
  assert.strictEqual(tiny.hidden, false, '非零成功率不得被当作 0% 隐藏');
  assert.strictEqual(zero.text, '0.0%', '真正的 0% 仍显示 0.0%');
  assert.strictEqual(zero.hidden, true);
  const small = JSON.parse(render('small-but-real'));
  assert.strictEqual(small.text, '0.2%', '0.2% 应显示真实值,不能被 <0.1% 的阈值覆盖');
  assert.strictEqual(small.hidden, false);

  document.__upstreamFilterButtons.hideZeroUpstream.onclick();
  assert.match(document.getElementById('apiStats').innerHTML, /tiny-rate/);
  assert.doesNotMatch(document.getElementById('apiStats').innerHTML, /exactly-zero/);
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

test('dashboard renders cached summary when recovering from fallback or errors with 304', async () => {
  for (const failFallback of [false, true]) {
    const { context, document } = createDashboardHarness({ dashboardEtags: true });
    await waitFor(() => document.getElementById('apiDetail').innerHTML.includes('deepseek-v4-flash-free'));
    const fetch = context.fetch;
    let failing = true;
    context.fetch = (url, options) => {
      if (failing && (url.includes('dashboard-summary') || (failFallback && url.includes('dashboard-data')))) {
        return Promise.reject(new Error('temporarily unavailable'));
      }
      return fetch(url, options);
    };
    await context.load();
    assert.match(document.getElementById('updated').textContent, failFallback ? /temporarily unavailable/ : /兼容模式/);
    failing = false;
    await context.load();
    assert.strictEqual(document.getElementById('totalRequests').textContent, '1,200');
    assert.doesNotMatch(document.getElementById('updated').textContent, /兼容模式|temporarily unavailable/);
  }
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
  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('healthGrid').children.length === 672);

  const cells = document.getElementById('healthGrid').children.filter((cell) => String(cell.className).includes('healthCell')).length;
  assert.strictEqual(cells, 672);
  assert.strictEqual(document.getElementById('healthSuccess').textContent, '成功 1');
  assert.strictEqual(document.getElementById('healthFailure').textContent, '失败 1');
  assert.match(document.getElementById('updated').textContent, /兼容模式/);
});

test('dashboard fallback handles null summary payload', async () => {
  const { document, fetchCalls } = createDashboardHarness({ nullDashboardSummary: true });
  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-data')) && document.getElementById('healthGrid').children.length === 672);

  const cells = document.getElementById('healthGrid').children.filter((cell) => String(cell.className).includes('healthCell')).length;
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

test('dashboard fallback aggregates cache reads with v2 semantics', () => {
  const { context } = createDashboardHarness();
  const generatedAt = new Date().toISOString();
  context.cacheUsageFixture = {
    generated_at: generatedAt,
    usage: {
      apis: {
        claude: {
          models: {
            'claude-opus-5': {
              details: [{
                timestamp: generatedAt,
                model: 'claude-opus-5',
                provider: 'claude',
                failed: false,
                tokens: {
                  input_tokens: 2,
                  output_tokens: 0,
                  cached_tokens: 40,
                  cache_read_tokens: 40,
                  cache_tokens: 100,
                  cache_write_tokens: 60,
                  total_tokens: 102,
                },
              }],
            },
          },
        },
      },
    },
  };

  const result = JSON.parse(vm.runInContext('JSON.stringify(buildSummaryFromFullUsage(cacheUsageFixture, "24h"))', context));
  const model = result.model_stats[0];
  assert.strictEqual(result.usage.cached_tokens, 40);
  assert.strictEqual(result.usage.cache_write_tokens, 60);
  assert.strictEqual(model.cached_tokens, 40);
  assert.strictEqual(model.providers[0].cached_tokens, 40);
  context.cacheModelRow = model;
  assert.ok(Math.abs(vm.runInContext('cacheRate(cacheModelRow)', context) - (40 / 102 * 100)) < 1e-9);
});

test('dashboard fallback includes archived accounting in ranges and detail-derived aggregates', () => {
  const { context } = createDashboardHarness();
  const now = Date.now();
  const detail = (hours, failed) => ({
    timestamp: new Date(now - hours * 3600000).toISOString(),
    provider: 'openai', model: 'gpt-4.1', api_key: 'sk-******test', api_key_hash: 'test-hash',
    failed, latency_ms: 100, tokens: { input_tokens: 10, output_tokens: 5, total_tokens: 15 },
  });
  const snapshot = {
    total_requests: 3, success_count: 2, failure_count: 1, total_tokens: 45,
    input_tokens: 30, output_tokens: 15,
  };
  const data = {
    generated_at: new Date(now).toISOString(),
    usage: { ...snapshot, apis: { openai: { ...snapshot, models: {
      'gpt-4.1': { ...snapshot, details: [detail(1, false)], accounting: [detail(2, true), detail(48, false)] },
    } } } },
  };
  for (const range of ['24h', 'all']) {
    const result = context.buildSummaryFromFullUsage(data, range);
    const count = range === '24h' ? 2 : 3;
    assert.strictEqual(result.usage.total_requests, count, range);
    assert.strictEqual(result.usage.total_tokens, count * 15, range);
    assert.strictEqual(result.model_stats[0].total_requests, count, range);
    assert.strictEqual(result.source_stats[0].total_requests, count, range);
    assert.strictEqual(result.client_api_stats[0].total_requests, count, range);
    assert.strictEqual(result.health_grid.reduce((total, cell) => total + cell.total, 0), 3, range);
  }
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
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$0.000176');
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
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$0.00064');
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

test('resource dashboard data and model prices use authenticated management routes', async () => {
  const { document, fetchRequests } = createDashboardHarness({
    pathname: '/v0/resource/plugins/usage-dashboard-zduu/dashboard',
    managementKey: 'test-management-key',
  });

  await openPriceSettings(document);
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
  assert.ok(fetchRequests.some(req => req.url.includes('dashboard-summary')));
  assert.ok(fetchRequests.some(req => req.url.includes('dashboard-events?')));
  for (const req of fetchRequests) {
    assert.match(req.url, /^\/v0\/management\/plugins\/usage-dashboard-zduu\//);
    assert.strictEqual(req.options.headers.Authorization, 'Bearer test-management-key');
  }
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

// 汇率来源和更新时间后端一直在返回、前端也读进了 state,却从未渲染;状态文案还是
// 硬编码中文,英文/俄文界面照样显示中文。
test('currency status shows the rate, its source and update time in the active language', async () => {
  const { document, setLanguage } = createDashboardHarness({
    summaryCurrency: {
      base: 'USD',
      supported_display: ['USD', 'CNY'],
      usd_cny_rate: 7.1234,
      source: 'open.er-api.com',
      fetched_at: '2026-08-22T03:00:00Z',
      status: 'cached',
      error: 'timeout',
    },
  });

  await waitFor(() => document.getElementById('currencyCNY').hidden === false);
  document.getElementById('currencyCNY').onclick();

  await waitFor(() => document.getElementById('currencyStatus').textContent.includes('7.1234'));
  const zh = document.getElementById('currencyStatus').textContent;
  assert.match(zh, /1 USD = 7\.1234 CNY/);
  assert.match(zh, /缓存汇率/);
  assert.doesNotMatch(zh, /open\.er-api\.com/);
  assert.doesNotMatch(zh, /更新于/);
  assert.match(document.getElementById('currencyStatus').title, /open\.er-api\.com/);
  assert.match(document.getElementById('currencyStatus').title, /更新于/);
  assert.match(document.getElementById('currencyStatus').title, /timeout/);

  setLanguage('en', { persisted: true });
  await waitFor(() => document.getElementById('currencyStatus').textContent.includes('cached rate'));
  const en = document.getElementById('currencyStatus').textContent;
  assert.match(document.getElementById('currencyStatus').title, /source · open\.er-api\.com|source open\.er-api\.com/);
  assert.doesNotMatch(en, /[一-龥]/);
});

// 金额格式化器是按 (currency, 小数位) 缓存的,切换语言必须把缓存整体丢弃,否则
// 金额会一直停留在旧 locale 的货币排版上。
test('money formatting follows the language switch', async () => {
  const { document, setLanguage } = createDashboardHarness({ summaryUsage: { total_cost: 0.05 } });

  await waitFor(() => document.getElementById('totalCost').textContent === 'US$0.05');
  setLanguage('en', { persisted: true });
  await waitFor(() => document.getElementById('totalCost').textContent === '$0.05');

  setLanguage('zh', { persisted: true });
  await waitFor(() => document.getElementById('totalCost').textContent === 'US$0.05');
});

test('currency switch stays hidden and USD-only when the backend reports no usable rate', async () => {
  const { document } = createDashboardHarness({
    summaryCurrency: { base: 'USD', supported_display: ['USD'], status: 'disabled' },
  });

  await waitFor(() => document.getElementById('totalCost').textContent.length > 0);
  assert.strictEqual(document.getElementById('currencyCNY').hidden, true);
  assert.strictEqual(document.getElementById('currencyStatus').textContent, '');
});

// 查询面板只渲染四项基础价格时,带分时价格的模型看起来和全天单价没有区别。
test('price lookup renders the time rules of the selected model', async () => {
  const { document } = createDashboardHarness({
    prices: {
      'openai/gpt-4.1': {
        prompt: 3,
        completion: 11,
        cache: 0.3,
        cache_write: 1,
        time_rules: [
          { id: 'night', name: '夜间半价', start: '22:00', end: '06:00', prompt: 1.5 },
        ],
      },
    },
  });

  await openPriceSettings(document);
  document.getElementById('priceReferenceModel').value = 'openai/gpt-4.1';
  document.getElementById('priceReferenceModel').onchange();

  const info = document.getElementById('priceReferenceInfo').innerHTML;
  assert.match(info, /夜间半价/);
  assert.match(info, /22:00–06:00/);
  // 覆盖了输入价,其余三项回落到基础价格,与后端 effectivePrice 的语义一致。
  assert.match(info, /1\.5000/);
  assert.match(info, /11\.0000/);
});

// 删除时段按钮的 aria-label 必须带上模型名:价格设置区可在多个模型间切换,只念
// 「删除时段 夜间半价」读屏用户无从判断删的是哪个模型的规则。
test('time rule remove buttons name the model they belong to', async () => {
  const { document } = createDashboardHarness({
    manualPrices: {
      'openai/gpt-4.1': {
        prompt: 3, completion: 11, cache: 0.3, cache_write: 1,
        time_rules: [
          { id: 'night', name: '夜间半价', start: '22:00', end: '06:00', prompt: 1.5 },
          { id: 'noon', name: '', start: '12:00', end: '13:00', prompt: 2 },
        ],
      },
    },
  });

  await openPriceSettings(document);
  document.getElementById('priceModel').value = 'openai/gpt-4.1';
  document.getElementById('priceModel').onchange();

  const html = document.getElementById('timeRulesEditor').innerHTML;
  assert.match(html, /aria-label="删除时段 openai\/gpt-4\.1 夜间半价"/);
  // 未命名的规则回落到「时段 N」,同样带模型名。
  assert.match(html, /aria-label="删除时段 openai\/gpt-4\.1 时段 2"/);
});

// 时段规则的星期维度:编辑器要渲染出 7 个星期开关和三个预设,并把当前范围标成
// 选中态。测试桩没有 querySelector,断言直接打在渲染出的 HTML 上。
test('time rule editor renders weekday chips and marks the active preset', async () => {
  const { document } = createDashboardHarness({
    manualPrices: {
      'openai/gpt-4.1': {
        prompt: 3, completion: 11, cache: 0.3, cache_write: 1,
        time_rules: [
          { id: 'night', name: '夜间半价', days: [1, 2, 3, 4, 5], start: '22:00', end: '06:00', prompt: 1.5 },
        ],
      },
    },
  });

  await openPriceSettings(document);
  document.getElementById('priceModel').value = 'openai/gpt-4.1';
  document.getElementById('priceModel').onchange();

  const html = document.getElementById('timeRulesEditor').innerHTML;
  assert.strictEqual((html.match(/data-day-toggle="/g) || []).length, 7);
  assert.match(html, /class="timeRuleDayPreset active" data-day-preset="workday"/);
  assert.doesNotMatch(html, /class="timeRuleDayPreset active" data-day-preset="weekend"/);
  // 周一到周五勾选,周六、周日不勾选。
  [1, 2, 3, 4, 5].forEach((day) => {
    assert.match(html, new RegExp('data-day-toggle="' + day + '"[^>]*checked'));
  });
  [0, 6].forEach((day) => {
    assert.doesNotMatch(html, new RegExp('data-day-toggle="' + day + '"[^>]*checked'));
  });
});

// 「每天」在存储里就是不带 days 的形态,与旧价格文件同形;限定了星期的规则才写
// days,并且排序去重。
test('serialized time rules omit days for every-day rules', async () => {
  const { context, document } = createDashboardHarness({
    manualPrices: {
      'openai/gpt-4.1': {
        prompt: 3, completion: 11, cache: 0.3, cache_write: 1,
        time_rules: [
          { id: 'all', name: '全天', start: '08:00', end: '09:00', prompt: 1 },
          { id: 'weekend', name: '周末', days: [6, 0], start: '10:00', end: '11:00', prompt: 2 },
          { id: 'full', name: '整周', days: [0, 1, 2, 3, 4, 5, 6], start: '12:00', end: '13:00', prompt: 3 },
        ],
      },
    },
  });

  await openPriceSettings(document);
  document.getElementById('priceModel').value = 'openai/gpt-4.1';
  document.getElementById('priceModel').onchange();

  const serialized = context.serializedTimeRules();
  assert.strictEqual(serialized.length, 3);
  assert.ok(!Object.prototype.hasOwnProperty.call(serialized[0], 'days'), '缺省 days 的规则不应写出 days');
  assert.deepStrictEqual(serialized[1].days, [0, 6], 'days 应排序');
  assert.ok(!Object.prototype.hasOwnProperty.call(serialized[2], 'days'), '覆盖整周应收敛成「每天」');
});

// 一条规则至少要落在一天上,否则 days 变空就等于「每天」,勾选状态会整排跳回全选。
test('weekday toggle refuses to clear the last remaining day', () => {
  const { context } = createDashboardHarness({});
  // vm context 返回的数组来自另一个 realm,deepStrictEqual 会比对原型,先转回本 realm。
  const days = (value) => (value === null ? null : Array.from(value));
  assert.deepStrictEqual(days(context.nextRuleDays([1, 5], 5, false)), [1]);
  assert.deepStrictEqual(days(context.nextRuleDays([1], 5, true)), [1, 5]);
  assert.strictEqual(context.nextRuleDays([1], 1, false), null, '取消最后一天必须被拒绝');
  // 勾满七天等于「每天」,归一化成空列表。
  assert.deepStrictEqual(days(context.nextRuleDays([0, 1, 2, 3, 4, 5], 6, true)), []);
});

// 星期不相交的两条规则永远不会同时命中,时间段重叠也不算冲突——这正是「工作日
// 夜间半价 / 周末夜间原价」能共存的前提。
test('client validation treats disjoint weekdays as non-overlapping', () => {
  const { context } = createDashboardHarness({});
  const validate = context.validateTimeRulesClient;
  const workday = { id: 'a', name: 'a', days: [1, 2, 3, 4, 5], start: '22:00', end: '06:00', prompt: 1 };
  const weekend = { id: 'b', name: 'b', days: [0, 6], start: '22:00', end: '06:00', prompt: 2 };
  assert.doesNotThrow(() => validate([workday, weekend]));
  assert.throws(() => validate([workday, Object.assign({}, weekend, { days: [5, 6] })]), /重叠|overlap/i);
  // 缺省 days 表示每天,与任何规则都相交。
  assert.throws(() => validate([workday, Object.assign({}, weekend, { days: undefined })]), /重叠|overlap/i);
  assert.throws(() => validate([Object.assign({}, workday, { days: [1, 1] })]), /./);
  assert.throws(() => validate([Object.assign({}, workday, { days: [7] })]), /./);
});

// 可见文字是缩写,读屏念「一」近乎无意义;每个星期开关必须带本地化全名的
// aria-label,并且随语言切换。
test('weekday toggles carry a localized full-name aria-label', async () => {
  const { document, setLanguage } = createDashboardHarness({
    manualPrices: {
      'openai/gpt-4.1': {
        prompt: 3, completion: 11, cache: 0.3, cache_write: 1,
        time_rules: [{ id: 'mon', name: '周一', days: [1], start: '08:00', end: '09:00', prompt: 1.5 }],
      },
    },
  });

  await openPriceSettings(document);
  document.getElementById('priceModel').value = 'openai/gpt-4.1';
  document.getElementById('priceModel').onchange();
  assert.match(document.getElementById('timeRulesEditor').innerHTML, /data-day-toggle="1"[^>]*aria-label="星期一"/);

  setLanguage('en', { persisted: true });
  document.getElementById('priceModel').onchange();
  const en = document.getElementById('timeRulesEditor').innerHTML;
  assert.match(en, /data-day-toggle="1"[^>]*aria-label="Monday"/);
  assert.match(en, /data-day-toggle="0"[^>]*aria-label="Sunday"/);
  assert.match(en, /data-day-toggle="6"[^>]*aria-label="Saturday"/);
});

// 来源/更新时间/错误原本只在原生 title 里,而 currencyStatus 是不可聚焦的 span:
// 触屏和键盘用户拿不到,aria-live 也只播报压缩后的正文。改为可聚焦的详情开关。
test('currency detail is reachable without hovering the status text', async () => {
  const { document } = createDashboardHarness({
    summaryCurrency: {
      base: 'USD',
      supported_display: ['USD', 'CNY'],
      usd_cny_rate: 7.1234,
      source: 'open.er-api.com',
      fetched_at: '2026-08-22T03:00:00Z',
      status: 'cached',
      error: 'timeout',
    },
  });

  await waitFor(() => document.getElementById('currencyCNY').hidden === false);
  document.getElementById('currencyCNY').onclick();
  await waitFor(() => document.getElementById('currencyStatus').textContent.includes('7.1234'));

  const info = document.getElementById('currencyStatusInfo');
  const detail = document.getElementById('currencyStatusDetail');
  assert.strictEqual(info.hidden, false, '有详情时开关必须可见');
  assert.strictEqual(info.getAttribute('aria-expanded'), 'false');
  assert.strictEqual(info.getAttribute('aria-controls'), 'currencyStatusDetail');
  assert.ok(info.getAttribute('aria-label'), '开关必须有可读名称');

  // 详情只放正文没有的那几项,不与紧凑正文重复。
  assert.match(detail.textContent, /open\.er-api\.com/);
  assert.match(detail.textContent, /更新于/);
  assert.match(detail.textContent, /timeout/);
  assert.doesNotMatch(detail.textContent, /7\.1234/);

  info.onclick();
  assert.strictEqual(info.getAttribute('aria-expanded'), 'true');
  assert.strictEqual(detail.hidden, false);
  info.onclick();
  assert.strictEqual(info.getAttribute('aria-expanded'), 'false');
  assert.strictEqual(detail.hidden, true);
});

// 切回 USD 后详情必须清空并收起,不能留下一个空的展开区。
test('currency detail collapses when the status is cleared', async () => {
  const { document } = createDashboardHarness({
    summaryCurrency: {
      base: 'USD', supported_display: ['USD', 'CNY'], usd_cny_rate: 7.1234,
      source: 'open.er-api.com', fetched_at: '2026-08-22T03:00:00Z', status: 'cached',
    },
  });

  await waitFor(() => document.getElementById('currencyCNY').hidden === false);
  document.getElementById('currencyCNY').onclick();
  await waitFor(() => document.getElementById('currencyStatus').textContent.includes('7.1234'));
  document.getElementById('currencyStatusInfo').onclick();
  assert.strictEqual(document.getElementById('currencyStatusDetail').hidden, false);

  document.getElementById('currencyUSD').onclick();
  await waitFor(() => document.getElementById('currencyStatus').textContent === '');
  assert.strictEqual(document.getElementById('currencyStatusInfo').hidden, true);
  assert.strictEqual(document.getElementById('currencyStatusDetail').hidden, true);
  assert.strictEqual(document.getElementById('currencyStatusDetail').textContent, '');
  assert.strictEqual(document.getElementById('currencyStatusInfo').getAttribute('aria-expanded'), 'false');
});

// 坏 days(手改或导入的价格文件)必须被前端拒绝,而不是被 serializedTimeRules
// 悄悄清洗后照常保存——后端 validateModelPriceRules 是拒绝的,两边必须一致。
test('invalid weekday values reach the validator instead of being sanitized', async () => {
  const { context, document } = createDashboardHarness({
    manualPrices: {
      'openai/gpt-4.1': {
        prompt: 3, completion: 11, cache: 0.3, cache_write: 1,
        time_rules: [{ id: 'bad', name: '坏规则', days: [7, 1], start: '08:00', end: '09:00', prompt: 1.5 }],
      },
    },
  });

  await openPriceSettings(document);
  document.getElementById('priceModel').value = 'openai/gpt-4.1';
  document.getElementById('priceModel').onchange();

  const serialized = context.serializedTimeRules();
  assert.deepStrictEqual(Array.from(serialized[0].days), [7, 1], '非法 days 必须原样带到校验');
  assert.throws(() => context.validateTimeRulesClient(serialized), /时段规则无效|Invalid time rule/);

  // 合法值仍然照常排序去重,整周收敛成「每天」(空列表)。
  [[[5, 1, 5], [1, 5]], [[0, 1, 2, 3, 4, 5, 6], []]].forEach(([input, expected]) => {
    const normalized = context.normalizedRuleDays(context.ruleDaySet({ days: input }));
    assert.deepStrictEqual(Array.from(normalized), expected);
  });
});

test('negotiated JSON array survives whole-response download fallback', async () => {
  for (const api of [false, true]) {
    const { context, document } = createDashboardHarness();
    await waitFor(() => document.getElementById('apiSelect').value === 'openai');
    const payload = '[{"model":"中文🙂","tokens":9007199254740993}]';
    const job = { id: 'array-fallback', status: 'succeeded', format: 'json', json_rows: true, content_type: 'application/json' };
    const blobs = [];
    let deleted = 0;
    context.Blob = Blob;
    context.URL.createObjectURL = (blob) => { blobs.push(blob); return 'blob:fallback'; };
    context.createExportJob = async () => job;
    context.deleteExportJob = async () => { deleted++; };
    context.fetchTextPayloadWithMeta = async () => ({ data: payload, headers: {}, statusCode: 200 });
    await document.getElementById(api ? 'exportApiJson' : 'exportRowsJson').onclick();
    assert.strictEqual(blobs.length, 1);
    assert.strictEqual(await blobs[0].text(), payload);
    assert.strictEqual(deleted, 1);
  }
});

test('full usage backup keeps large integers on an older backend', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  const payload = '{"version":1,"detail_count":1,"usage":{"total_tokens":9007199254740993,"apis":{"中文🙂":{"models":{}}}}}';
  const blobs = [];
  context.Blob = Blob;
  context.URL.createObjectURL = blob => { blobs.push(blob); return 'blob:usage-backup'; };
  context.fetch = async url => String(url).includes('usage/export-jobs')
    ? { ok: false, status: 404, text: async () => 'not found' }
    : { ok: true, status: 200, text: async () => payload };
  await document.getElementById('exportBtn').onclick();
  assert.strictEqual(blobs.length, 1);
  assert.ok((await blobs[0].text()).includes('9007199254740993'), 'full usage backup rounded an int64');
});

test('legacy full backup retains exact JSON through every supported ABI envelope', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  context.Blob = Blob;
  context.atob = atob; // Match strict browser base64 handling, including raw JSON bodies.
  const payload = '{ "version": 1, "usage": { "total_tokens": 9007199254740993, "value": "中文🙂, \\\" } ], : ", "nested": [{"result":0}] } }';
  const response = '{"status_code":200,"headers":{"Content-Type":["application/json"]},"body":' + payload + '}';
  const variants = [
    payload,
    response,
    JSON.stringify({ status_code: 200, body: payload }),
    JSON.stringify({ status_code: 200, body: Buffer.from(payload).toString('base64') }),
    JSON.stringify({ status_code: 200, body: Array.from(Buffer.from(payload)) }),
    '{"ok":true,"result":' + payload + '}',
    JSON.stringify({ ok: true, result: payload }),
    '{"ok":true,"result":' + response + '}',
    JSON.stringify({ ok: true, result: response }),
    '{"ok":true,"result":{"wrong":1},"nested":{"result":0},"\\u0072esult":' + payload + '}',
  ];
  for (const [index, raw] of variants.entries()) {
    const calls = [];
    context.fetch = async (url, options) => {
      calls.push([String(url), options.method || 'GET']);
      return String(url).includes('usage/export-jobs')
        ? { ok: false, status: [404, 405, 501][index % 3], text: async () => 'not supported' }
        : { ok: true, status: 200, text: async () => raw };
    };
    const file = await context.fetchUsageExportFile();
    assert.ok(file instanceof Blob);
    assert.strictEqual(await file.text(), payload, 'envelope ' + index + ' altered JSON bytes');
    assert.strictEqual(calls.length, 2);
    assert.strictEqual(calls[0][1], 'POST');
    assert.strictEqual(calls[1][1], 'GET');
  }
});

test('full usage backup streams authenticated Blob chunks and restores the button', async () => {
  const { context, document } = createDashboardHarness({ managementKey: 'synthetic-backup-key', pathname: '/v0/resource/plugins/usage-dashboard-zduu/dashboard' });
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  const payload = '{"version":1,"detail_count":123,"usage":{"total_tokens":9007199254740993,"source":"中文🙂"}}';
  const bytes = Buffer.from(payload);
  const job = { id: 'full-backup', kind: 'usage', status: 'succeeded', format: 'json', content_type: 'application/json',
    body_bytes: bytes.length, chunk_size: 7, version: 'f'.repeat(64), total: 123, exported: 123 };
  let creates = 0, polls = 0, deletes = 0, injected = 0;
  const offsets = [], blobs = [];
  context.Blob = Blob;
  context.TextDecoder = class { constructor() { throw new Error('chunked backup must not decode a complete file'); } };
  context.delay = async () => {};
  context.URL.createObjectURL = blob => { blobs.push(blob); return 'blob:full-backup'; };
  context.fetchJsonPayload = async (url, options) => {
    assert.strictEqual(options.headers.Authorization, 'Bearer synthetic-backup-key');
    const parsed = new URL(url, 'http://test.local');
    assert.ok(parsed.pathname.startsWith('/v0/management/plugins/usage-dashboard-zduu/usage/'));
    if (parsed.pathname.endsWith('/export-jobs')) {
      if (options.method === 'POST') { creates++; return { ...job, status: 'queued' }; }
      assert.strictEqual(parsed.searchParams.get('id'), job.id);
      if (options.method === 'DELETE') { deletes++; return {}; }
      polls++;
      return job;
    }
    assert.ok(parsed.pathname.endsWith('/export-download'));
    const offset = Number(parsed.searchParams.get('offset'));
    offsets.push(offset);
    assert.strictEqual(parsed.searchParams.get('version'), job.version);
    if (!injected++) throw new Error('transient read failure');
    const data = bytes.subarray(offset, offset + Number(parsed.searchParams.get('length')));
    return { offset, total: bytes.length, version: job.version, data: data.toString('base64'), checksum_crc32: context.exportChunkChecksum(data) };
  };
  const button = document.getElementById('exportBtn');
  const pending = button.onclick();
  assert.strictEqual(button.disabled, true);
  await button.onclick();
  await pending;
  assert.strictEqual(button.disabled, false);
  assert.strictEqual(creates, 1);
  assert.strictEqual(polls, 1);
  assert.strictEqual(deletes, 1);
  assert.deepStrictEqual(offsets.slice(0, 2), [0, 0]);
  assert.strictEqual(blobs.length, 1);
  assert.strictEqual(await blobs[0].text(), payload);
  assert.ok(document.body.children.some(el => /^usage-export-.*\.json$/.test(el.download)));
});

test('full backup does not fall back on auth, resource, server or transport failures', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  const failures = [
    ...[401, 403, 429, 500, 503].map(status => ({ ok: false, status, body: 'request failed' })),
    { ok: false, status: 503, body: JSON.stringify({ error: { code: 'not_found', message: 'masked server failure' } }) },
    { ok: true, status: 200, body: JSON.stringify({ status_code: 429, body: 'too many jobs' }) },
    { ok: true, status: 200, body: JSON.stringify({ ok: false, error: { code: 'internal', message: 'failed' } }) },
    { network: true },
  ];
  for (const failure of failures) {
    const calls = [];
    context.fetch = async url => {
      calls.push(String(url));
      if (failure.network) throw new Error('offline');
      return { ok: failure.ok, status: failure.status, text: async () => failure.body };
    };
    await assert.rejects(context.fetchUsageExportFile());
    assert.strictEqual(calls.length, 1, 'a failed new endpoint must not trigger a different backup');
  }
});

test('full backup negotiates gzip and saves lossless JSON through native decompression streams', async () => {
  const { context, document } = createDashboardHarness({ managementKey: 'synthetic-gzip-key' });
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  context.Blob = Blob;
  context.DecompressionStream = DecompressionStream;
  context.TextDecoder = class { constructor() { throw new Error('backup must not decode whole-file text'); } };
  const payload = '{"version":1,"usage":{"total_tokens":9007199254740993,"source":"' + '中文🙂'.repeat(12000) + '"}}';
  const raw = Buffer.from(payload), compressed = gzipSync(raw);
  const job = { id: 'compressed-backup', kind: 'usage', status: 'succeeded', format: 'json', gzip: true,
    content_type: 'application/json', body_bytes: compressed.length, raw_bytes: raw.length,
    chunk_size: 13, version: 'c'.repeat(64) };
  let creates = 0, deletes = 0, chunks = 0;
  context.fetchJsonPayload = async (url, options) => {
    assert.strictEqual(options.headers.Authorization, 'Bearer synthetic-gzip-key');
    const parsed = new URL(url, 'http://test.local');
    if (options.method === 'DELETE') { deletes++; return {}; }
    if (options.method === 'POST') {
      creates++;
      assert.strictEqual(parsed.searchParams.get('gzip'), '1');
      return job;
    }
    chunks++;
    const offset = Number(parsed.searchParams.get('offset'));
    const data = compressed.subarray(offset, offset + Number(parsed.searchParams.get('length')));
    return { offset, total: compressed.length, version: job.version, data: data.toString('base64'), checksum_crc32: context.exportChunkChecksum(data) };
  };
  const file = await context.fetchUsageExportFile();
  assert.ok(file instanceof Blob);
  assert.strictEqual(await file.text(), payload);
  assert.strictEqual(file.size, raw.length);
  assert.strictEqual(file.type, 'application/json');
  assert.strictEqual(creates, 1);
  assert.strictEqual(deletes, 1);
  assert.ok(chunks > 2);
});

test('full backup rejects corrupt gzip and wrong decoded lengths without a legacy retry', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  context.Blob = Blob;
  context.DecompressionStream = DecompressionStream;
  const raw = Buffer.from('{"version":1,"usage":{"source":"中文🙂"}}'), compressed = gzipSync(raw);
  const corrupt = Buffer.from(compressed);
  corrupt[corrupt.length - 8] ^= 1;
  const cases = [
    { data: corrupt }, { data: compressed.subarray(0, compressed.length - 1) },
    { data: Buffer.concat([compressed, Buffer.from('trailing garbage')]) },
    ...[raw.length - 1, raw.length + 1, 0, -1, undefined, '100', 1.5, Number.MAX_SAFE_INTEGER + 1].map(rawBytes => ({ rawBytes })),
  ];
  for (const variant of cases) {
    const data = variant.data || compressed;
    const job = { id: 'bad-gzip', kind: 'usage', status: 'succeeded', format: 'json', gzip: true,
      content_type: 'application/json', body_bytes: data.length,
      raw_bytes: Object.hasOwn(variant, 'rawBytes') ? variant.rawBytes : raw.length,
      chunk_size: 256, version: 'c'.repeat(64) };
    let deletes = 0, legacy = 0;
    context.fetchJsonPayload = async (_url, options) => {
      if (options.method === 'DELETE') { deletes++; return {}; }
      if (options.method === 'POST') return job;
      return { offset: 0, total: data.length, version: job.version, data: data.toString('base64'), checksum_crc32: context.exportChunkChecksum(data) };
    };
    context.fetchTextPayloadWithMeta = async () => { legacy++; throw new Error('unexpected legacy retry'); };
    await assert.rejects(context.fetchUsageExportFile());
    assert.strictEqual(deletes, 1);
    assert.strictEqual(legacy, 0);
  }
});

test('full backup stays uncompressed when gzip streams cannot be constructed', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  context.Blob = Blob;
  context.DecompressionStream = class { constructor() { throw new TypeError('gzip unavailable'); } };
  const bytes = Buffer.from('{"version":1,"usage":{}}');
  const job = { id: 'plain-backup', kind: 'usage', status: 'succeeded', format: 'json',
    content_type: 'application/json', body_bytes: bytes.length, chunk_size: 256, version: 'd'.repeat(64) };
  let creates = 0;
  context.fetchJsonPayload = async (url, options) => {
    if (options.method === 'DELETE') return {};
    if (options.method === 'POST') {
      creates++;
      assert.strictEqual(new URL(url, 'http://test.local').searchParams.has('gzip'), false);
      return job;
    }
    return { offset: 0, total: bytes.length, version: job.version, data: bytes.toString('base64'), checksum_crc32: context.exportChunkChecksum(bytes) };
  };
  assert.strictEqual(await (await context.fetchUsageExportFile()).text(), bytes.toString());
  assert.strictEqual(creates, 1);
});

test('backup decompression cancels and releases a stream before retaining excess decoded bytes', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  context.Blob = Blob;
  let reads = 0, canceled = 0, released = 0;
  const reader = {
    async read() { reads++; return { done: false, value: Buffer.alloc(10) }; },
    async cancel() { canceled++; },
    releaseLock() { released++; },
  };
  const decompressor = {};
  const file = { stream: () => ({ pipeThrough(value) {
    assert.strictEqual(value, decompressor);
    return { getReader: () => reader };
  } }) };
  await assert.rejects(context.decompressUsageExportFile(file, 5, decompressor));
  assert.strictEqual(reads, 1);
  assert.strictEqual(canceled, 1);
  assert.strictEqual(released, 1);
});

test('base64 chunks avoid collecting a string iterator through TypedArray.from', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  context.Uint8Array = class extends Uint8Array {
    static from() { throw new Error('must not allocate an intermediate iterator list'); }
  };
  const bytes = Buffer.from(Array.from({ length: 256 * 1024 }, (_, i) => i & 255));
  assert.deepStrictEqual(Buffer.from(context.decodeBase64Bytes(bytes.toString('base64'))), bytes);
});

test('full backup rejects event jobs, partial negotiation and changed poll identities', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  const ready = { id: 'backup', kind: 'usage', status: 'succeeded', format: 'json', version: 'a'.repeat(64), chunk_size: 256, body_bytes: 2 };
  context.delay = async () => {};
  for (const change of [{ kind: 'events' }, { kind: undefined }, { format: 'csv' }, { json_rows: true },
    { gzip: true }, { truncated: true }, { version: '' }, { chunk_size: 0 }, { status: 'failed', error: 'disk full' }]) {
    let deleted = 0, downloaded = 0;
    context.fetchJsonPayload = async (url, options) => {
      if (options.method === 'DELETE') { deleted++; return {}; }
      if (String(url).includes('export-download')) { downloaded++; throw new Error('wrong download'); }
      return { ...ready, ...change };
    };
    await assert.rejects(context.fetchUsageExportFile());
    assert.strictEqual(deleted, 1);
    assert.strictEqual(downloaded, 0);
  }
  for (const polled of [{ ...ready, id: 'different' }, { ...ready, kind: 'events' }]) {
    let deleted = 0;
    context.fetchJsonPayload = async (_url, options) => {
      if (options.method === 'DELETE') { deleted++; return {}; }
      return options.method === 'POST' ? { ...ready, status: 'running' } : polled;
    };
    await assert.rejects(context.fetchUsageExportFile());
    assert.strictEqual(deleted, 1);
  }
});

test('full backup cleans up corrupt downloads and restores its button after failure', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  let deleted = 0, legacy = 0;
  const job = { id: 'corrupt-backup', kind: 'usage', status: 'succeeded', format: 'json', version: 'a'.repeat(64), chunk_size: 2, body_bytes: 2 };
  context.fetchJsonPayload = async (url, options) => {
    if (options.method === 'DELETE') { deleted++; return {}; }
    if (String(url).includes('export-download')) return { offset: 0, total: 2, version: job.version, data: 'e30=', checksum_crc32: '00000000' };
    return job;
  };
  context.fetchTextPayloadWithMeta = async () => { legacy++; return { data: '{}' }; };
  await assert.rejects(context.fetchUsageExportFile());
  assert.strictEqual(deleted, 1);
  assert.strictEqual(legacy, 0);
  context.fetchUsageExportFile = async () => { throw new Error('failed'); };
  await document.getElementById('exportBtn').onclick();
  assert.strictEqual(document.getElementById('exportBtn').disabled, false);
});

test('full backup handles old not-found envelopes but rejects invalid fallback documents', async () => {
  const { context, document } = createDashboardHarness();
  await waitFor(() => document.getElementById('apiSelect').value === 'openai');
  context.Blob = Blob;
  context.atob = atob;
  const payload = '{"version":1,"usage":{"total_tokens":9007199254740993}}';
  for (const raw of [payload, 'null', '<html>not a backup</html>', '{"version":1,"events":[]}', '{"version":2,"usage":{}}',
    JSON.stringify({ status_code: 403, body: Buffer.from('denied').toString('base64') }),
    JSON.stringify({ ok: false, error: { code: 'internal', message: 'failed' } })]) {
    const calls = [];
    context.fetch = async url => {
      calls.push(String(url));
      const body = String(url).includes('export-jobs') ? JSON.stringify({ ok: false, error: { code: 'not_found', message: 'no endpoint' } }) : raw;
      return { ok: true, status: 200, text: async () => body };
    };
    if (raw === payload) assert.strictEqual(await (await context.fetchUsageExportFile()).text(), payload);
    else await assert.rejects(context.fetchUsageExportFile());
    assert.strictEqual(calls.length, 2);
  }
});

test('quota capacity shows a single token estimate and keeps the reset explanation', async () => {
  const { context } = createDashboardHarness({ language: 'en' });
  await context.load();
  const html = context.quotaPeriodHtml({
    reset_baseline_used_percent: 20, used_percent: 30, estimated_total_usd: 5 / .3,
    summary: { estimated_cost: 5, total_requests: 1, total_tokens: 1e6 },
    model_stats: [{ model: 'm', estimated_cost: 5, total_tokens: 1e6, model_only_estimated_total_tokens: 1e6 / .3,
      model_only_tokens_low: 909e6, model_only_tokens_high: 1111e6 }]
  }, true);
  assert.match(html, /3\.333 M/);
  assert.doesNotMatch(html, /909\.00 M|1,111\.00 M|Model-only estimated total cost/);
  assert.match(html, /Quota reset detected/);
  assert.match(html, /No minimum consumption threshold applies/);
});

test('Antigravity malformed quota entries cannot hide valid pools or become reset readings', async () => {
  const { context } = createDashboardHarness({ language: 'en' });
  await context.load();
  const valid = { window: 'weekly', remainingFraction: '0.6', resetTime: '2026-10-10T08:00:00Z' };
  const malformed = [true, false, [0], [1], {}, '0x0', '0b1'];
  const buckets = context.antigravityQuotaBuckets({ groups: [{ displayName: 'Claude', buckets: malformed.map(remainingFraction => ({ ...valid, remainingFraction })) }] });
  assert.equal(buckets.length, 0, 'malformed values must not produce full or exhausted quota');
  const mixed = context.antigravityQuotaBuckets({ groups: [null, [], { displayName: 'Claude', buckets: [null, [], valid] }] });
  assert.equal(mixed.length, 1, 'invalid entries must not hide the remaining valid pools');
  assert.equal(mixed[0].remaining_fraction, 0.6);
  const badNames = context.antigravityQuotaBuckets({ groups: [{ displayName: {}, buckets: [valid] }, { displayName: 'Claude', buckets: [{ ...valid, bucketId: {} }] }] });
  assert.equal(badNames.length, 0, 'objects must not become invented pool identities');
});
