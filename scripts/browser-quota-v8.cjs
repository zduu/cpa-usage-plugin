#!/usr/bin/env node
// The fixture supplies a loopback-only host and disposable management key.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { chromium } = require(process.env.CPA_PLAYWRIGHT || 'playwright');

(async () => {
  const config = JSON.parse(await fs.readFile(process.argv[2], 'utf8'));
  const output = process.argv[3];
  const base = new URL(config.base);
  assert.equal(base.hostname, '127.0.0.1');
  assert.equal(base.protocol, 'http:');
  const browser = await chromium.launch({ headless: true,
    ...(process.env.CPA_CHROME ? { executablePath: process.env.CPA_CHROME } : {}) });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    await context.addInitScript(key => {
      localStorage.setItem('managementKey', key);
      localStorage.setItem('cli-proxy-language', 'en');
      localStorage.setItem('cpa-usage-range-v1', 'all');
    }, config.key);
    await context.route('**/*', route => new URL(route.request().url()).origin === base.origin ? route.continue() : route.abort());
    const page = await context.newPage();
    page.setDefaultTimeout(20000);
    const errors = [], unauthenticated = [], submissions = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('request', request => {
      const url = new URL(request.url());
      if (url.pathname.includes('/management/') && request.headers().authorization !== 'Bearer ' + config.key) unauthenticated.push(url.pathname);
      if (url.pathname.endsWith('/dashboard-quota-observations')) submissions.push(request.postDataJSON());
    });
    const response = await page.goto(base.origin + '/v0/resource/plugins/usage-dashboard-zduu/dashboard');
    assert.equal(response.status(), 200);
    await page.waitForFunction(() => document.querySelector('#totalRequests').textContent.trim() === '5');
    await page.selectOption('#apiSelect', config.apis.claude);
    const quota = page.locator('.quotaCycles');
    await quota.getByText('current-a', { exact: true }).waitFor();
    await quota.getByText('current-b', { exact: true }).waitFor();
    assert.match(await quota.textContent(), /25\.00/);
    assert.equal(await quota.getByText('old-model', { exact: true }).count(), 0);
    await quota.locator('[data-quota-period="previous"]').click();
    await quota.getByText('old-model', { exact: true }).waitFor();
    assert.match(await quota.textContent(), /30\.00/);
    assert.equal(await quota.locator('[data-quota-reset]').count(), 0);
    assert.equal(await quota.getByText('current-a', { exact: true }).count(), 0);
    await page.evaluate(() => load({ forceDetails: true }));
    assert.equal(await quota.locator('[data-quota-period="previous"]').getAttribute('aria-pressed'), 'true');
    await quota.screenshot({ path: path.join(output, 'previous-period.png') });
    await quota.locator('[data-quota-period="current"]').click();
    const countdown = await quota.locator('[data-quota-reset]').textContent();
    await page.waitForFunction(previous => document.querySelector('[data-quota-reset]')?.textContent !== previous, countdown);
    await quota.screenshot({ path: path.join(output, 'current-period.png') });

    await page.selectOption('#apiSelect', config.apis.codex);
    await quota.getByText('Weekly quota', { exact: true }).waitFor();
    assert.doesNotMatch(await quota.textContent(), /5-hour|5h/);
    assert.equal(await quota.locator('.quotaWindow').count(), 1);
    await page.selectOption('#apiSelect', config.apis.devin);
    await quota.getByText('Weekly quota', { exact: true }).waitFor();
    assert.match(await quota.textContent(), /40/);
    assert.ok(submissions.some(batch => batch.observations.some(row => row.provider === 'devin')), 'browser never submitted Devin observations');

    await page.selectOption('#apiSelect', config.apis.claude);
    await quota.getByText('current-a', { exact: true }).waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    // Cached content appears before the asynchronous selection refresh ends.
    // Await the final render so it cannot detach the screenshot's element.
    await page.evaluate(() => renderApiDetail());
    await quota.screenshot({ path: path.join(output, 'mobile-period.png') });
    assert.ok(await quota.locator('.tableWrap').evaluate(el => el.scrollWidth > el.clientWidth), 'wide model table should scroll on mobile');
    assert.deepEqual(errors, []);
    assert.deepEqual(unauthenticated, []);
    const report = { passed: true, checks: ['native usage', 'current and previous models and costs', 'selection survives refresh', 'live countdown', 'weekly-only Codex', 'Devin browser collection', 'management authentication', 'mobile table'], submissions: submissions.length };
    await fs.writeFile(path.join(output, 'browser-result.json'), JSON.stringify(report, null, 2) + '\n');
    console.log('PASS: quota browser, current/previous models, authenticated collection and mobile view');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
