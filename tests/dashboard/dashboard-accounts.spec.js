// Desktop dashboard summary rail with several Claude accounts. Synthetic
// accounts only, shaped like OpenUsage Team seats: a 5-hour window and a
// Fable window, credits 0, and no account-wide weekly window reported.
const { test, expect } = require('@playwright/test');
const { loadDashboard, dashboardFixture } = require('./harness');

const NOW = Date.parse('2026-07-20T19:00:00Z');
const at = (hours) => new Date(NOW + hours * 3600000).toISOString();

function teamSeat(id, label, short) {
  return {
    id, label, provider: 'claude', policy: 'standard', policy_source: 'global', default_policy: 'standard',
    max_concurrent_runs: 1, active_runs: 0, usage_source: { active: 'openusage' }, account_status: 'ok',
    latest_decision: { decision: 'UNKNOWN', policy: 'standard', reason: 'weekly limit not reported; dispatch holds until a policy exists for this account type' },
    snapshot: {
      provider: 'claude', observed_at: at(0), plan: 'Team 5x', source: 'openusage', weekly_unavailable: true, banked_resets: 0,
      ...short,
      allowances: [{ key: 'model:fable:weekly', source_label: 'Fable', scope: 'model', role: 'weekly', remaining: 1, resets_at: at(78), period_duration_seconds: 604800 }],
    },
  };
}

/** claude-main (with a weekly window), `seats` Team seats, then Codex. */
function accountsDashboard(seats = 4) {
  const dashboard = dashboardFixture();
  const [main, codex] = dashboard.providers;
  const names = ['Delta', 'Alpha', 'Echo', 'Bravo', 'Charlie', 'Foxtrot', 'Golf'];
  const team = names.slice(0, seats).map((name, index) => teamSeat(
    `claude-seat-${index + 1}`, name,
    index === 2 ? { short: { remaining: 0.45, resets_at: at(2.5) } } : { short_window_not_started: true },
  ));
  dashboard.providers = [{ ...main, label: 'Personal', snapshot: { ...main.snapshot, plan: 'Max 5x', banked_resets: 1, banked_resets_expire_at: at(200) } }, ...team, codex];
  return dashboard;
}

const rail = (page) => page.locator('.summary-rail');
const tiles = (page) => page.locator('#usage-strip .provider-compact');

/** Every tile sits fully inside the rail, none overlaps another or the counters. */
async function expectTilesFit(page) {
  const box = await rail(page).boundingBox();
  const boxes = await tiles(page).evaluateAll(nodes => nodes.map(n => { const r = n.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height }; }));
  const meta = await page.locator('.rail-meta').evaluate(n => { const r = n.getBoundingClientRect(); const s = getComputedStyle(n); return s.display === 'none' ? null : { x: r.x, y: r.y, w: r.width, h: r.height }; });
  for (const b of boxes) {
    expect(b.w).toBeGreaterThan(0);
    expect(b.x).toBeGreaterThanOrEqual(box.x - 0.5);
    expect(b.x + b.w).toBeLessThanOrEqual(box.x + box.width + 0.5);
    expect(b.y + b.h).toBeLessThanOrEqual(box.y + box.height + 0.5);
    if (meta) {
      const overlapX = Math.min(b.x + b.w, meta.x + meta.w) - Math.max(b.x, meta.x);
      const overlapY = Math.min(b.y + b.h, meta.y + meta.h) - Math.max(b.y, meta.y);
      expect(overlapX > 0.5 && overlapY > 0.5, 'tile overlaps the NEXT CHECK / RUNNING counters').toBe(false);
    }
  }
  for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
    const a = boxes[i], b = boxes[j];
    const ox = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x);
    const oy = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
    expect(ox > 0.5 && oy > 0.5, `tiles ${i} and ${j} overlap`).toBe(false);
  }
  // No text inside a tile is cut off: every text line fits its own box.
  // The account name may end in an ellipsis on purpose (its full text is the
  // tooltip); everything else must fit.
  const clipped = await tiles(page).evaluateAll(nodes => nodes.flatMap(n => [...n.querySelectorAll('.provider-trigger *')]
    .filter(e => !e.closest('.provider-name-text'))
    .filter(e => e.children.length === 0 && e.textContent.trim() && getComputedStyle(e).display !== 'none')
    .filter(e => e.scrollWidth > e.clientWidth + 1 && getComputedStyle(e).overflow !== 'visible' || e.getBoundingClientRect().right > n.getBoundingClientRect().right + 0.5)
    .map(e => e.textContent.trim())));
  expect(clipped).toEqual([]);
}

for (const width of [1440, 1280, 1024, 820, 390]) {
  test(`every account tile is visible and nothing overlaps at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await loadDashboard(page, { dashboard: accountsDashboard(4) });
    await expect(tiles(page)).toHaveCount(6);
    for (let i = 0; i < 6; i++) await expect(tiles(page).nth(i)).toBeVisible();
    await expectTilesFit(page);
  });
}

test('a Team seat tile shows its 5-hour usage and reset, and says weekly is not reported', async ({ page }) => {
  await loadDashboard(page, { dashboard: accountsDashboard(4) });
  const echo = page.locator('[data-provider-id="claude-seat-3"]');
  const trigger = echo.locator('.provider-trigger');
  // Headline: the 5-hour number, labelled as such; never a weekly figure.
  await expect(trigger.locator('.provider-copy-line b')).toHaveText('45% 5h');
  await expect(trigger).toContainText('5h 45% · resets');
  await expect(trigger).toContainText('Weekly not reported');
  await expect(trigger).not.toContainText(/\d+% weekly/);
  // The bar is the 5-hour window and says so.
  await expect(trigger.getByRole('progressbar', { name: '45% 5-hour remaining' })).toHaveCount(1);
  await expect(trigger.getByRole('progressbar', { name: /weekly/ })).toHaveCount(0);
  await expect(trigger.locator('.provider-pressure')).toHaveText('Dispatch held · weekly not reported');
  // Name and plan identify the account.
  await expect(trigger).toContainText('Echo');
  await expect(trigger).toContainText('Team 5x');
});

test('an unstarted Team seat says it starts on first use and still shows no weekly number', async ({ page }) => {
  await loadDashboard(page, { dashboard: accountsDashboard(4) });
  const delta = page.locator('[data-provider-id="claude-seat-1"] .provider-trigger');
  await expect(delta.locator('.provider-copy-line b')).toHaveText('100% 5h');
  await expect(delta).toContainText('starts on first use');
  await expect(delta).toContainText('Weekly not reported');
  await expect(delta.getByRole('progressbar', { name: '100% 5-hour remaining' })).toHaveCount(1);
});

test('the account with a weekly window keeps its weekly headline and bar', async ({ page }) => {
  await loadDashboard(page, { dashboard: accountsDashboard(4) });
  const main = page.locator('[data-provider-id="claude-main"] .provider-trigger');
  await expect(main.locator('.provider-copy-line b')).toHaveText('53% weekly');
  await expect(main.getByRole('progressbar', { name: '53% weekly remaining' })).toHaveCount(1);
  await expect(main).toContainText('Personal');
});

test('details keep the Fable reset, credits and the missing-weekly explanation', async ({ page }) => {
  await loadDashboard(page, { dashboard: accountsDashboard(4) });
  const echo = page.locator('[data-provider-id="claude-seat-3"]');
  await echo.locator('.provider-trigger').click();
  await expect(echo.locator('[data-testid="weekly-not-reported"]')).toContainText('Weekly limit not reported for this account');
  await expect(echo.locator('.provider-detail')).toContainText('Fable');
  await expect(echo.locator('.provider-detail')).toContainText('0 available');
  await expect(echo.locator('.provider-detail')).toContainText('5-hour window');
  await expect(echo.locator('.detail-head strong')).toHaveText('Claude Echo capacity');
});

test('a right-hand tile opens its details inside the window', async ({ page }) => {
  await loadDashboard(page, { dashboard: accountsDashboard(4) });
  for (const id of ['claude-seat-3', 'claude-main']) {
    const card = page.locator(`[data-provider-id="${id}"]`);
    await card.locator('.provider-trigger').click();
    const detail = card.locator('.provider-detail');
    await expect(detail).toBeVisible();
    await page.waitForTimeout(250);
    const box = await detail.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(1280);
  }
});

test('seven Claude accounts wrap onto more rows and stay readable', async ({ page }) => {
  await loadDashboard(page, { dashboard: accountsDashboard(7) });
  await expect(tiles(page)).toHaveCount(9);
  await expectTilesFit(page);
  const rows = new Set(await tiles(page).evaluateAll(nodes => nodes.map(n => Math.round(n.getBoundingClientRect().y))));
  expect(rows.size).toBeGreaterThan(1);
});

test('a long account label is cut with an ellipsis and kept whole in the tooltip', async ({ page }) => {
  const dashboard = accountsDashboard(4);
  dashboard.providers[2].label = 'Client — Northwind long-running engagement with a very long name';
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await loadDashboard(page, { dashboard });
    await expectTilesFit(page);
    const name = page.locator('[data-provider-id="claude-seat-2"] .provider-copy-line strong');
    await expect(page.locator('[data-provider-id="claude-seat-2"] .provider-plan')).toBeVisible();
    const pillWhole = await page.locator('[data-provider-id="claude-seat-2"] .provider-plan').evaluate(n => n.scrollWidth <= n.clientWidth + 1 && n.getBoundingClientRect().right <= n.closest('.provider-compact').getBoundingClientRect().right + 0.5);
    expect(pillWhole).toBe(true);
    await expect(name).toHaveAttribute('title', /Client — Northwind long-running engagement with a very long name · Team 5x/);
    await expect(page.locator('[data-provider-id="claude-seat-2"] .provider-copy-line b')).toBeVisible();
    // The name box itself stays inside its tile.
    const fits = await name.evaluate(n => n.getBoundingClientRect().right <= n.closest('.provider-compact').getBoundingClientRect().right + 0.5);
    expect(fits).toBe(true);
  }
});

for (const width of [1024, 1280]) {
  test(`three tiles sit in one row beside the counters at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await loadDashboard(page, { dashboard: accountsDashboard(1) });
    await expect(tiles(page)).toHaveCount(3);
    await expectTilesFit(page);
  });
}

test('stale and signed-out seats among several accounts still fit and say why', async ({ page }) => {
  const dashboard = accountsDashboard(4);
  const stale = dashboard.providers[1];
  stale.snapshot_stale = true; stale.account_status = 'stale';
  const signedOut = dashboard.providers[2];
  signedOut.account_status = 'signed_out';
  signedOut.usage_source = { active: 'openusage', last_error: 'claude CLI is signed out' };
  signedOut.stale_reason = 'Claude CLI signed out';
  for (const width of [1280, 1024, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await loadDashboard(page, { dashboard });
    await expectTilesFit(page);
    await expect(page.locator('[data-provider-id="claude-seat-1"] .provider-copy-line b')).toHaveText('Usage unavailable');
  }
});

test('details stay a full-width panel on narrow windows, for every column', async ({ page }) => {
  await page.setViewportSize({ width: 600, height: 900 });
  await loadDashboard(page, { dashboard: accountsDashboard(4) });
  for (const id of ['claude-main', 'claude-seat-1']) {
    const card = page.locator(`[data-provider-id="${id}"]`);
    await card.locator('.provider-trigger').click();
    await page.waitForTimeout(250);
    const box = await card.locator('.provider-detail').boundingBox();
    expect(Math.round(box.x)).toBe(16);
    expect(Math.round(box.x + box.width)).toBe(600 - 16 - (await page.evaluate(() => window.innerWidth - document.documentElement.clientWidth)));
    await page.keyboard.press('Escape');
    await page.mouse.move(0, 0);
    await expect(card.locator('.provider-detail')).toBeHidden();
  }
});

test('one Claude account keeps the single-row rail', async ({ page }) => {
  await loadDashboard(page);
  await expect(tiles(page)).toHaveCount(2);
  await expectTilesFit(page);
  const rows = new Set(await tiles(page).evaluateAll(nodes => nodes.map(n => Math.round(n.getBoundingClientRect().y))));
  expect(rows.size).toBe(1);
});

test.describe('screenshots', () => {
  test.skip(!process.env.REDLINE_RAIL_SCREENSHOTS, 'set REDLINE_RAIL_SCREENSHOTS to a directory');
  for (const width of [1440, 1280, 1024, 820, 390]) {
    test(`rail at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await loadDashboard(page, { dashboard: accountsDashboard(4) });
      await rail(page).screenshot({ path: `${process.env.REDLINE_RAIL_SCREENSHOTS}/rail-${width}.png` });
    });
  }
  test('default two providers at 1280px', async ({ page }) => {
    await loadDashboard(page);
    await rail(page).screenshot({ path: `${process.env.REDLINE_RAIL_SCREENSHOTS}/rail-1280-default-two.png` });
  });
  test('Echo details open', async ({ page }) => {
    await loadDashboard(page, { dashboard: accountsDashboard(4) });
    await page.locator('[data-provider-id="claude-seat-3"] .provider-trigger').click();
    await page.waitForTimeout(300);
    await page.screenshot({ path: `${process.env.REDLINE_RAIL_SCREENSHOTS}/rail-1280-echo-details.png`, clip: { x: 0, y: 0, width: 1280, height: 720 } });
  });
});
