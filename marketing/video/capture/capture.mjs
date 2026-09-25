// Reproducible promo captures from Redline's isolated demo service.
//
//   node capture/capture.mjs            # both providers + overview
//   REDLINE_BIN=/tmp/redline node capture/capture.mjs
//
// Every frame comes from `redline demo serve`: synthetic providers and jobs,
// the production decision evaluator, and a no-op executor. The DEMO pill is
// hidden only in these marketing captures; the product always shows it.
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, '../../..');
const out = resolve(here, '../public/captures');
// Playwright is already a root devDependency for the dashboard tests.
const { chromium } = createRequire(join(repo, 'package.json'))('@playwright/test');

const VIEWPORT = { width: 1600, height: 900 }; // 16:9, rendered at 2x → 3200x1800
const SCALE = 2;

function buildBinary() {
  if (process.env.REDLINE_BIN) return process.env.REDLINE_BIN;
  const bin = join(tmpdir(), 'redline-promo-capture');
  execFileSync('go', ['build', '-o', bin, './cmd/redline'], { cwd: repo, stdio: 'inherit' });
  return bin;
}

async function assertPortFree(port) {
  try {
    await fetch(`http://127.0.0.1:${port}`);
  } catch {
    return;
  }
  throw new Error(`port ${port} is already serving something; set PROMO_PORT_BASE to a free range`);
}

async function serve(bin, args, port) {
  await assertPortFree(port);
  const state = mkdtempSync(join(tmpdir(), 'redline-promo-'));
  rmSync(state, { recursive: true }); // demo serve requires a new or empty dir
  const child = spawn(bin, ['demo', 'serve', ...args, '--listen', `127.0.0.1:${port}`, '--state-dir', state], { stdio: ['ignore', 'pipe', 'pipe'] });
  let log = '';
  child.stdout.on('data', d => { log += d; });
  child.stderr.on('data', d => { log += d; });
  const url = `http://127.0.0.1:${port}`;
  for (let i = 0; i < 100; i++) {
    if (child.exitCode !== null) throw new Error(`demo serve exited:\n${log}`);
    try { if ((await fetch(url)).ok) return { url, stop: () => { child.kill('SIGTERM'); rmSync(state, { recursive: true, force: true }); } }; } catch {}
    await new Promise(r => setTimeout(r, 100));
  }
  child.kill('SIGTERM');
  throw new Error(`demo serve did not start:\n${log}`);
}

// Marketing-only presentation: hide the DEMO pill/red rule and drop the
// "Demo ·" source label from the capacity popover.
const PRESENTATION_CSS = `
  #demo-pill{display:none!important}
  body.demo-mode .topbar{box-shadow:none!important}
  *{caret-color:transparent!important}
`;
const PRESENTATION_SCRIPT = () => {
  // The synthetic sample is stamped at render time, so clock skew can read
  // "sampled in 0 secs"; present it the way a fresh real sample reads.
  const scrub = () => document.querySelectorAll('.detail-head > span').forEach(span => {
    let text = span.textContent;
    if (text.startsWith('Demo · ')) text = text.slice('Demo · '.length);
    text = text.replace(/sampled in \d+ secs?/, 'sampled just now');
    if (text !== span.textContent) span.textContent = text;
  });
  new MutationObserver(scrub).observe(document, { subtree: true, childList: true, characterData: true });
};

async function openPage(browser, url) {
  const context = await browser.newContext({ viewport: VIEWPORT, deviceScaleFactor: SCALE, colorScheme: 'dark', reducedMotion: 'reduce', bypassCSP: true });
  await context.addInitScript(PRESENTATION_SCRIPT);
  const page = await context.newPage();
  await page.goto(url);
  await page.addStyleTag({ content: PRESENTATION_CSS });
  await page.locator('#live-pill:not(.connecting)').waitFor();
  await page.locator('#tasks-body [data-task-row]').first().waitFor();
  await page.waitForTimeout(400);
  return { context, page };
}

// manifest.json records CSS-pixel boxes of the elements the video zooms to,
// so camera moves follow the real layout instead of hard-coded coordinates.
const manifest = { viewport: VIEWPORT, scale: SCALE, shots: {} };
const BOXES = {
  main: 'main',
  rail: '.summary-rail',
  queue: '.queue-panel',
  row: '#tasks-body [data-task-row]',
  status: '#tasks-body [data-task-row] .status',
  activity: 'section.panel:has(#runs-list)',
  firstRun: '#runs-list > :first-child',
  decisions: 'section.panel:has(#attempts-list)',
  popover: '.provider-compact.open .provider-detail',
  decisionDetail: '.provider-compact.open .decision-detail',
  meters: '.provider-compact.open .meters',
  dialog: '#logs-dialog[open]',
  runResult: '#logs-dialog[open] #run-result',
};

async function shot(page, name) {
  await page.screenshot({ path: join(out, `${name}.png`) });
  const boxes = {};
  for (const [key, selector] of Object.entries(BOXES)) {
    const locator = page.locator(selector).first();
    if (await locator.count() && await locator.isVisible()) {
      const box = await locator.boundingBox();
      if (box) boxes[key] = Object.fromEntries(Object.entries(box).map(([k, v]) => [k, Math.round(v)]));
    }
  }
  manifest.shots[name] = boxes;
}

async function captureProvider(browser, bin, provider, port) {
  const slug = provider.replace('-main', '');
  const demo = await serve(bin, ['--scenario', 'decision-run', '--provider', provider, '--run-duration', '5s'], port);
  try {
    const { context, page } = await openPage(browser, demo.url);
    await shot(page, `${slug}-01-queued`);

    const card = page.locator(`.provider-compact[data-provider-id="${provider}"]`);
    await card.locator('.provider-trigger').click();
    await card.locator('.provider-detail').waitFor({ state: 'visible' });
    await card.locator('[data-capacity-evidence][data-loaded="true"]').waitFor().catch(() => {});
    await page.waitForTimeout(300);
    await shot(page, `${slug}-02-capacity`);
    await page.mouse.click(1500, 860); // close popover

    const response = await page.evaluate(async id => {
      const r = await fetch('/v1/scheduler/execute', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ provider_account_id: id }) });
      return { status: r.status, body: await r.json() };
    }, provider);
    if (response.body?.result?.decision !== 'RUN') throw new Error(`${provider} was not admitted: ${JSON.stringify(response)}`);
    await page.locator('#tasks-body').getByText(/running/i).first().waitFor();
    await page.waitForTimeout(500);
    await shot(page, `${slug}-03-running`);

    await page.locator('#runs-list').getByText('Fixed a race in cache refresh').first().waitFor({ timeout: 15_000 });
    await page.waitForTimeout(500);
    await shot(page, `${slug}-04-completed`);

    await page.locator('#runs-list [data-run]').first().click();
    await page.locator('#logs-dialog[open] #log-content').filter({ hasNotText: 'Select a stream.' }).waitFor();
    await page.waitForTimeout(400);
    await shot(page, `${slug}-05-run-detail`);
    await context.close();
  } finally {
    demo.stop();
  }
}

async function captureOverview(browser, bin, port) {
  const demo = await serve(bin, ['--scenario', 'overview'], port);
  try {
    const { context, page } = await openPage(browser, demo.url);
    await shot(page, 'overview');
    await context.close();
  } finally {
    demo.stop();
  }
}

mkdirSync(out, { recursive: true });
const bin = buildBinary();
const browser = await chromium.launch();
try {
  const base = Number(process.env.PROMO_PORT_BASE || 7481);
  await captureProvider(browser, bin, 'claude-main', base);
  await captureProvider(browser, bin, 'codex-main', base + 1);
  await captureOverview(browser, bin, base + 2);
} finally {
  await browser.close();
}
writeFileSync(join(out, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
console.log(`Captures written to ${out}`);
