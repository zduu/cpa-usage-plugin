#!/usr/bin/env node
// Invoked by validate-stock-cpa.py --browser. The temporary loopback URL and
// test-only key arrive over stdin, never argv, logs or a saved browser profile.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const { chromium } = require(process.env.CPA_PLAYWRIGHT || 'playwright');

(async () => {
  let input = '';
  for await (const chunk of process.stdin) input += chunk;
  const config = JSON.parse(input);
  const base = new URL(config.base);
  assert.equal(base.hostname, '127.0.0.1');
  assert.equal(base.protocol, 'http:');
  const browser = await chromium.launch({ headless: true,
    ...(process.env.CPA_CHROME ? { executablePath: process.env.CPA_CHROME } : {}) });
  try {
    const context = await browser.newContext({ acceptDownloads: true, viewport: { width: 1440, height: 1000 } });
    await context.addInitScript(key => localStorage.setItem('managementKey', key), config.key);
    await context.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (url.origin !== base.origin) return route.abort();
      // Only the static bootstrap is public. The application itself must
      // attach management credentials to every subsequent data request.
      return route.continue();
    });
    const page = await context.newPage();
    page.setDefaultTimeout(20000);
    page.setDefaultNavigationTimeout(20000);
    const errors = [], unauthenticated = [], resourceDataRequests = [], dialogs = [];
    let apiRequests = 0;
    page.on('pageerror', error => errors.push(error.message));
    page.on('dialog', async dialog => { dialogs.push(dialog.message()); await dialog.dismiss(); });
    page.on('request', request => {
      const path = new URL(request.url()).pathname;
      if (path.startsWith('/v0/resource/plugins/') && !path.endsWith('/dashboard')) resourceDataRequests.push(path);
      if (path.startsWith(base.pathname)) {
        apiRequests++;
        if (request.headers().authorization !== 'Bearer ' + config.key) unauthenticated.push(path);
      }
    });
    const start = performance.now();
    assert.equal((await page.goto(base.origin + '/v0/resource/plugins/usage-dashboard-zduu/dashboard')).status(), 200);
    await page.selectOption('#range', 'all');
    await page.waitForFunction(count => document.querySelector('#totalRequests').textContent.replace(/\D/g, '') === String(count), config.records);
    await page.waitForFunction(() => document.querySelectorAll('#events tbody tr').length > 0);
    const firstRenderMs = performance.now() - start;
    await page.selectOption('#filterModel', 'model-1');
    await page.selectOption('#filterModel', 'model-0');
    await page.waitForFunction(() => document.querySelector('#eventsCount').textContent.includes('5,000') && document.querySelector('#events tbody').textContent.includes('model-0'));
    await page.click('#eventsNext');
    await page.waitForFunction(() => document.querySelector('#eventsPage').textContent.match(/\d+/)?.[0] === '2');
    assert.equal(await page.inputValue('#filterModel'), 'model-0');

    let injectedFailures = 0, chunks = 0, opaqueVersions = 0;
    await page.route('**/dashboard-events-export-download?*', async route => {
      const url = new URL(route.request().url());
      if (url.searchParams.get('chunk') === '1') {
        chunks++;
        if (/^[0-9a-f]{64}$/.test(url.searchParams.get('version'))) opaqueVersions++;
        if (!injectedFailures) {
          injectedFailures++;
          await route.fulfill({ status: 503, body: 'temporary test fault' });
          return;
        }
      }
      await route.continue();
    });
    const downloads = [];
    for (const [button, format] of [['#exportRowsJson', 'json'], ['#exportRowsCsv', 'csv']]) {
      const started = performance.now();
      const pending = page.waitForEvent('download');
      await page.click(button);
      const download = await pending;
      assert.equal(await download.failure(), null);
      const bytes = await fs.readFile(await download.path());
      const text = bytes.toString('utf8');
      assert.ok(download.suggestedFilename().endsWith('.' + format));
      assert.ok(text.includes('9007199254740993'), 'int64 precision lost in the downloaded file');
      if (format === 'json') {
        const rows = JSON.parse(text);
        assert.equal(rows.length, 5000);
        assert.ok(rows.every(row => row.model === 'model-0' && Object.hasOwn(row, 'cost_usd')));
      } else {
        assert.equal(text.trim().split('\n').length, 5001);
        assert.ok(text.split('\n')[0].includes('cost_usd'));
      }
      downloads.push({ format, bytes: bytes.length, durationMs: performance.now() - started });
    }

    let backupChunks = 0, backupVersions = 0, backupFailures = 0;
    await page.route('**/usage/export-download?*', async route => {
      const url = new URL(route.request().url());
      assert.equal(url.searchParams.get('chunk'), '1');
      backupChunks++;
      if (/^[0-9a-f]{64}$/.test(url.searchParams.get('version'))) backupVersions++;
      if (!backupFailures) {
        backupFailures++;
        return route.fulfill({ status: 503, body: 'temporary backup read fault' });
      }
      await route.continue();
    });
    const cdp = await context.newCDPSession(page);
    await cdp.send('Performance.enable');
    const heap = async () => (await cdp.send('Performance.getMetrics')).metrics.find(item => item.name === 'JSHeapUsedSize').value;
    await page.evaluate(() => {
      globalThis.__stockBackupDecompressor = globalThis.DecompressionStream;
      globalThis.__stockBackupURLs = [];
      const createObjectURL = URL.createObjectURL.bind(URL);
      URL.createObjectURL = value => {
        const url = createObjectURL(value);
        globalThis.__stockBackupURLs.push(url);
        return url;
      };
    });
    assert.equal(await page.evaluate(() => typeof DecompressionStream), 'function');
    const fullBackups = [];
    let missingEndpointInjected = 0;
    // Keep the fault-recovery sample separate from the clean gzip timing.
    // The other two paths exercise older browsers and older plugin backends.
    for (const transport of ['gzip-chunks-retry', 'gzip-chunks', 'chunks', 'legacy-fallback']) {
      const legacy = transport === 'legacy-fallback';
      await page.evaluate(plain => { globalThis.DecompressionStream = plain ? undefined : globalThis.__stockBackupDecompressor; }, transport === 'chunks');
      if (legacy) await page.route(url => url.pathname.endsWith('/usage/export-jobs'), async route => {
          if (route.request().method() === 'POST') {
            missingEndpointInjected++;
            return route.fulfill({ status: 404, body: 'simulated older backend without backup jobs' });
          }
          await route.continue();
        });
      const jobResponses = [];
      const observeJob = response => {
        if (new URL(response.url()).pathname.endsWith('/usage/export-jobs') && response.request().method() !== 'DELETE' && response.ok()) {
          jobResponses.push(response.json());
        }
      };
      page.on('response', observeJob);
      // Reset only browser-test garbage between paths. Blob URLs are revoked
      // after Playwright has consumed the file, not while the app downloads it.
      await cdp.send('HeapProfiler.collectGarbage');
      const initialHeap = await heap(), initialChunks = backupChunks, initialFailures = backupFailures;
      let sampledPeakHeap = initialHeap;
      const timer = setInterval(() => { heap().then(value => { sampledPeakHeap = Math.max(sampledPeakHeap, value); }).catch(() => {}); }, 25);
      try {
        const started = performance.now();
        const pending = page.waitForEvent('download');
        await page.click('#exportBtn');
        const download = await pending;
        assert.equal(await download.failure(), null);
        const bytes = await fs.readFile(await download.path());
        const text = bytes.toString('utf8');
        const payload = JSON.parse(text);
        assert.match(download.suggestedFilename(), /^usage-export-.*\.json$/);
        assert.ok(text.includes('9007199254740993'), 'full backup lost int64 precision');
        assert.equal(payload.version, 1);
        assert.equal(payload.detail_count, config.records);
        assert.equal(payload.usage.total_requests, config.records);
        let details = 0, accounting = 0;
        const models = new Set();
        for (const api of Object.values(payload.usage.apis)) for (const [name, model] of Object.entries(api.models)) {
          models.add(name);
          details += model.details.length;
          accounting += (model.accounting || []).length;
        }
        assert.equal(details, 10000);
        assert.equal(details + accounting, config.records);
        assert.deepEqual([...models].sort(), ['model-0', 'model-1']);
        await page.waitForFunction(() => !document.querySelector('#exportBtn').disabled);
        assert.equal(await page.inputValue('#filterModel'), 'model-0');
        const durationMs = performance.now() - started;
        const job = (await Promise.all(jobResponses)).findLast(value => value.status === 'succeeded');
        if (!legacy) {
          assert.ok(job, 'missing completed backup metadata');
          assert.equal(job.gzip, transport !== 'chunks');
          assert.equal(job.raw_bytes, bytes.length);
          assert.equal(job.chunk_size, 256 * 1024);
        }
        fullBackups.push({ transport, bytes: bytes.length, wireFileBytes: job?.body_bytes || bytes.length,
          chunks: backupChunks - initialChunks, injectedFailures: backupFailures - initialFailures,
          durationMs, records: payload.detail_count, details, accounting, initialHeap, sampledPeakHeap, finalHeap: await heap() });
      } finally {
        clearInterval(timer);
        page.off('response', observeJob);
        await Promise.allSettled(jobResponses);
        await page.evaluate(() => {
          for (const url of globalThis.__stockBackupURLs.splice(0)) URL.revokeObjectURL(url);
        });
      }
    }
    await page.waitForFunction(async ({ base, key }) => {
      for (const path of ['/dashboard-events-export-jobs', '/usage/export-jobs']) {
        const response = await fetch(base + path, { headers: { Authorization: 'Bearer ' + key } });
        if ((await response.json()).jobs.length !== 0) return false;
      }
      return true;
    }, config);
    assert.ok(chunks > 2);
    assert.equal(opaqueVersions, chunks, 'browser did not use the opaque file version');
    assert.ok(backupChunks > 2);
    assert.equal(backupVersions, backupChunks);
    assert.equal(backupFailures, 1);
    assert.equal(missingEndpointInjected, 1);
    assert.ok(apiRequests > 10);
    assert.deepEqual(unauthenticated, []);
    assert.deepEqual(resourceDataRequests, []);
    assert.deepEqual(errors, []);
    assert.deepEqual(dialogs, []);
    process.stdout.write(JSON.stringify({ browser: browser.version(), records: config.records,
      filteredRecords: 5000, firstRenderMs, apiRequests, chunks, opaqueVersions, injectedFailures, downloads,
      backupChunks, backupFailures, missingEndpointInjected, fullBackups,
      heapSampling: { intervalMs: 25, collectGarbageBeforeEachBackup: true, scope: 'browser JS heap, not native Blob memory or RSS' },
      resourceDataRequests, pageErrors: errors, dialogs }) + '\n');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
