#!/usr/bin/env node
// Requires Playwright (or CPA_PLAYWRIGHT pointing to playwright-core) and a
// Chromium installation. Run from any directory. Only a temporary browser
// profile and a loopback Go test server are created.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const fs = require('node:fs/promises');
const path = require('node:path');
const { chromium } = require(process.env.CPA_PLAYWRIGHT || 'playwright');
const root = path.resolve(__dirname, '..');

(async () => {
  const host = spawn('go', ['test', '-run', '^TestDashboardBrowserServer$', '-count=1', '-v'], {
    cwd: path.join(root, 'go'), env: { ...process.env, CPA_BROWSER_TEST: '1' }, stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '', browser, base;
  const exited = new Promise(resolve => host.on('exit', code => resolve(code)));
  host.stderr.on('data', chunk => { output += chunk; });
  try {
    base = await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('browser server startup timed out')), 60000);
      host.stdout.on('data', chunk => {
        output += chunk;
        const match = output.match(/CPA_BROWSER_URL=(\S+)/);
        if (match) { clearTimeout(timeout); resolve(match[1]); }
      });
      host.on('exit', code => { clearTimeout(timeout); reject(new Error(`browser server exited ${code}: ${output}`)); });
      host.on('error', reject);
    });
    browser = await chromium.launch({ headless: true, ...(process.env.CPA_CHROME ? { executablePath: process.env.CPA_CHROME } : {}) });
    const context = await browser.newContext({ acceptDownloads: true, viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20000);
    page.setDefaultNavigationTimeout(20000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const cdp = await context.newCDPSession(page);
    await cdp.send('Performance.enable');
    const heap = async () => (await cdp.send('Performance.getMetrics')).metrics.find(x => x.name === 'JSHeapUsedSize').value;
    const started = performance.now();
    await page.goto(base);
    await page.waitForFunction(() => document.querySelector('#totalRequests').textContent.replace(/\D/g, '') === '12000');
    await page.waitForFunction(() => document.querySelectorAll('#events tbody tr').length > 0);
    const firstRenderMs = performance.now() - started;
    await page.selectOption('#range', 'all');
    await page.selectOption('#filterModel', 'browser-b');
    await page.selectOption('#filterModel', '中文🙂');
    await page.selectOption('#filterModel', 'browser-a');
    await page.waitForFunction(() => document.querySelector('#eventsCount').textContent.includes('4,000') && document.querySelector('#events tbody').textContent.includes('browser-a'));
    assert.equal(await page.inputValue('#filterModel'), 'browser-a');
    await page.click('#eventsNext');
    await page.waitForFunction(() => document.querySelector('#eventsPage').textContent.match(/\d+/)?.[0] === '2');
    assert.equal(await page.inputValue('#filterModel'), 'browser-a');

    // Inject one transport failure, then require the real chunk retry to
    // finish a byte-accurate file. API and encoding responses stay production.
    let retries = 0, chunkRequests = 0;
    await page.route('**/dashboard-events-export-download?*', async route => {
      const url = new URL(route.request().url());
      if (url.searchParams.get('chunk') === '1') {
        chunkRequests++;
        if (!retries++) { await route.fulfill({ status: 503, body: 'temporary test fault' }); return; }
      }
      await route.continue();
    });
    const initialHeap = await heap();
    let sampledPeakHeap = initialHeap;
    const timer = setInterval(() => { heap().then(value => { sampledPeakHeap = Math.max(sampledPeakHeap, value); }).catch(() => {}); }, 50);
    const downloads = [];
    try {
      for (const [button, format] of [['#exportRowsJson', 'json'], ['#exportRowsCsv', 'csv']]) {
        const start = performance.now();
        const pending = page.waitForEvent('download');
        await page.click(button);
        const download = await pending;
        assert.equal(await download.failure(), null);
        const bytes = await fs.readFile(await download.path());
        const text = bytes.toString('utf8');
        assert.ok(download.suggestedFilename().endsWith('.' + format));
        assert.ok(text.includes('9007199254740993'), 'int64 token precision lost');
        if (format === 'json') {
          const rows = JSON.parse(text);
          assert.equal(rows.length, 4000);
          assert.ok(rows.every(row => row.model === 'browser-a' && Object.hasOwn(row, 'cost_usd')));
        } else {
          assert.equal(text.trim().split('\n').length, 4001);
          assert.ok(text.split('\n')[0].includes('cost_usd'));
        }
        downloads.push({ format, bytes: bytes.length, durationMs: performance.now() - start });
      }
    } finally { clearInterval(timer); }
    assert.ok(chunkRequests > 2, 'browser did not use chunk transport');
    await page.waitForFunction(async () => {
      const response = await fetch('dashboard-events-export-jobs');
      return (await response.json()).jobs.length === 0;
    });
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ browser: browser.version(), records: 12000, filteredRecords: 4000, firstRenderMs, initialHeap, sampledPeakHeap, finalHeap: await heap(), chunkRequests, injectedFailures: 1, downloads, pageErrors: errors }, null, 2));
  } finally {
    if (browser) await browser.close();
    if (base) await fetch(new URL('/__stop', base), { method: 'POST' }).catch(() => {});
    else host.kill();
    const code = await exited;
    if (code !== 0) throw new Error(`browser server exit ${code}: ${output}`);
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
