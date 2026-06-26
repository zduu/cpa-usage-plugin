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
  const sortButtons = ['requests', 'tokens', 'cost'].map((name) => {
    const el = new FakeElement('sort-' + name);
    el.dataset.apiSort = name;
    return el;
  });
  const downloads = [];
  const fetchCalls = [];
  const fetchRequests = [];
  let prices = { 'gpt-4.1': { prompt: 2, completion: 8, cache: 0.5 } };

  const document = {
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
          avg_latency_ms: 120,
          models: {
            'gpt-4.1': {
              total_requests: 1200,
              success_count: 1190,
              failure_count: 10,
              total_tokens: 24000,
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
    _meta: {},
  };

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
      events: Array.from({ length: count }, (_, i) => ({
        timestamp: new Date(1700000000000 + offset + i).toISOString(),
        model: 'gpt-4.1',
        source: 'openai-prod',
        provider: 'openai',
        auth_index: 'auth-1',
        failed: false,
        latency_ms: 120,
        tokens: { input_tokens: 10, output_tokens: 5, total_tokens: 15 },
      })),
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
    setTimeout() { return 1; },
    clearTimeout() {},
    alert(message) { downloads.push({ alert: message }); },
    fetch: async (url, options = {}) => {
      fetchCalls.push(String(url));
      fetchRequests.push({ url: String(url), options });
      let payload;
      if (String(url).includes('model-prices')) {
        if (options.method === 'PUT') {
          const body = JSON.parse(options.body || '{}');
          prices[body.model] = body.price;
        } else if (options.method === 'DELETE') {
          const parsed = new URL(String(url), 'http://test.local/v0/management/plugins/usage-statistics/dashboard');
          delete prices[parsed.searchParams.get('model')];
        }
        payload = { prices, updated_at: new Date().toISOString(), storage: {} };
      } else if (String(url).includes('dashboard-summary')) payload = summary;
      else if (String(url).includes('dashboard-events')) payload = eventsPage(String(url));
      else if (String(url).includes('usage/export')) payload = { version: 1, usage: {} };
      else payload = {};
      return {
        ok: true,
        status: 200,
        text: async () => JSON.stringify(payload),
      };
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

  return { context, document, fetchCalls, fetchRequests, downloads };
}

async function waitFor(fn) {
  for (let i = 0; i < 50; i++) {
    if (fn()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  throw new Error('condition not met');
}

test('dashboard loads summary and export button fetches all event pages', async () => {
  const { document, fetchCalls, downloads } = createDashboardHarness();

  await waitFor(() => fetchCalls.some((url) => url.includes('dashboard-events')));
  assert.strictEqual(document.getElementById('totalRequests').textContent, '1,200');
  assert.strictEqual(document.getElementById('totalCost').textContent, 'US$0.05');

  await document.getElementById('exportRowsJson').onclick();
  await waitFor(() => downloads.some((d) => d.text && d.text.startsWith('[')));

  const exportCalls = fetchCalls
    .filter((url) => url.includes('dashboard-events'))
    .slice(1);
  assert.deepStrictEqual(
    exportCalls.map((url) => new URL(url, 'http://test.local').searchParams.get('offset')),
    ['0', '500', '1000']
  );
  const exported = JSON.parse(downloads.find((d) => d.text && d.text.startsWith('[')).text);
  assert.strictEqual(exported.length, 1200);
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
