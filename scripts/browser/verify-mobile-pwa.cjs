#!/usr/bin/env node
const { chromium } = require('@playwright/test');
const { execFileSync, spawn } = require('node:child_process');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');

async function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      server.close(error => error ? reject(error) : resolve(port));
    });
  });
}

async function waitFor(url, process) {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    if (process.exitCode !== null) throw new Error(`demo server exited with ${process.exitCode}`);
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch (_) {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error(`timed out waiting for ${url}`);
}

async function main() {
  const outputDir = path.resolve(process.argv[2] || '.artifacts/mobile-pwa-runtime');
  fs.mkdirSync(outputDir, { recursive: true });
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), 'redline-mobile-pwa-state-'));
  const buildDir = fs.mkdtempSync(path.join(os.tmpdir(), 'redline-mobile-pwa-build-'));
  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const projectRoot = path.resolve(__dirname, '..', '..');
  const binary = path.join(buildDir, 'redline');
  execFileSync('go', ['build', '-o', binary, './cmd/redline'], { cwd: projectRoot, stdio: 'inherit' });
  const server = spawn(binary, ['demo', 'serve', '--listen', `127.0.0.1:${port}`, '--state-dir', stateDir, '--keep'], {
    cwd: projectRoot,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let serverLog = '';
  server.stdout.on('data', chunk => { serverLog += chunk; });
  server.stderr.on('data', chunk => { serverLog += chunk; });

  let browser;
  try {
    await waitFor(`${baseURL}/v1/health`, server);
    browser = await chromium.launch({ headless: true });
    const context = await browser.newContext({ viewport: { width: 412, height: 915 }, deviceScaleFactor: 2.625 });
    const page = await context.newPage();
    const consoleErrors = [];
    page.on('console', message => {
      if (message.type() === 'error') consoleErrors.push(message.text());
    });
    await page.goto(`${baseURL}/m`, { waitUntil: 'domcontentloaded' });
    await page.locator('#m-tab-usage').waitFor({ state: 'visible' });
    const serviceWorker = await page.evaluate(async () => {
      const registration = await navigator.serviceWorker.ready;
      return { scope: registration.scope, controlled: Boolean(navigator.serviceWorker.controller) };
    });
    if (serviceWorker.scope !== `${baseURL}/`) throw new Error(`unexpected service worker scope ${serviceWorker.scope}`);
    const onlineConsoleErrors = consoleErrors.slice();
    if (onlineConsoleErrors.length) throw new Error(`online console errors: ${onlineConsoleErrors.join(' | ')}`);

    // A newly claimed worker may require one controlled navigation before offline reload.
    await page.reload({ waitUntil: 'domcontentloaded' });
    await context.setOffline(true);
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.locator('#m-tab-usage').waitFor({ state: 'visible' });
    await page.screenshot({ path: path.join(outputDir, 'offline-shell-pixel9.png') });
    await context.setOffline(false);

    const result = {
      base_url: baseURL,
      service_worker_scope: serviceWorker.scope,
      service_worker_controlled_initially: serviceWorker.controlled,
      offline_shell_visible: true,
      viewport: { width: 412, height: 915, device_scale_factor: 2.625 },
      online_console_errors: onlineConsoleErrors,
      offline_console_errors: consoleErrors.slice(onlineConsoleErrors.length),
    };
    fs.writeFileSync(path.join(outputDir, 'pwa-runtime.json'), JSON.stringify(result, null, 2) + '\n');
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
  } finally {
    if (browser) await browser.close();
    if (server.exitCode === null) {
      server.kill('SIGTERM');
      await Promise.race([
        new Promise(resolve => server.once('exit', resolve)),
        new Promise(resolve => setTimeout(resolve, 3000)),
      ]);
    }
    server.stdout.destroy();
    server.stderr.destroy();
    fs.rmSync(stateDir, { recursive: true, force: true });
    fs.rmSync(buildDir, { recursive: true, force: true });
    if (server.exitCode && server.exitCode !== 0 && server.signalCode !== 'SIGTERM') process.stderr.write(serverLog);
  }
}

main().catch(error => {
  console.error(error.stack || error);
  process.exitCode = 1;
});
