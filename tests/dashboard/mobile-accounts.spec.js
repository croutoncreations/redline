// Mobile web multi-account carousel — mirrors the Android design.
// Synthetic accounts only; labels, plans and numbers are made up.
const { test, expect } = require('@playwright/test');
const { loadMobileDashboard, dashboardFixture } = require('./harness');

const NOW = Date.parse('2026-07-20T19:00:00Z');
const at = (hours) => new Date(NOW + hours * 3600000).toISOString();

function multiAccountDashboard({ extraClaude = 0 } = {}) {
  const dashboard = dashboardFixture();
  const base = dashboard.providers[0];
  const claude = (id, fields) => ({ ...base, id, provider: 'claude', usage_source: { active: 'openusage' }, ...fields });
  const accounts = [
    claude('claude-client', {
      label: 'Client — Northwind long-running engagement', account_status: 'ok',
      latest_decision: { decision: 'WAIT', reason: 'below reserve' },
      snapshot: { provider: 'claude', observed_at: at(0), plan: 'Team', short: { remaining: 0, resets_at: at(0.95) }, weekly: { remaining: 0.58, resets_at: at(104) }, allowances: [] },
    }),
    claude('claude-main', {
      label: 'Personal', account_status: 'ok',
      latest_decision: { decision: 'RUN', reason: 'surplus' },
      snapshot: {
        provider: 'claude', observed_at: at(0), plan: 'Max 20x',
        short: { remaining: 0.68, resets_at: at(2.2) }, weekly: { remaining: 0.41, resets_at: at(75) }, allowances: [],
        banked_resets: 2, banked_resets_expire_at: at(86), banked_resets_expiries: [at(230), at(86)],
      },
    }),
    claude('claude-side', {
      label: 'Side project', account_status: 'stale', snapshot_stale: true,
      error: "OpenUsage reports this account's usage as out of date.",
      latest_decision: { decision: 'UNKNOWN', reason: 'stale' },
      snapshot: {
        provider: 'claude', observed_at: at(-3), plan: 'Pro', source_stale: true,
        short: { remaining: 0.64, resets_at: at(-1) }, weekly: { remaining: 0.41, resets_at: at(66) }, allowances: [],
        banked_resets: 1, banked_resets_expiries: [at(-0.5)],
      },
    }),
  ];
  for (let i = 1; i <= extraClaude; i++) {
    accounts.push(claude(`claude-extra-${i}`, {
      label: `Extra ${i}`, account_status: 'ok',
      snapshot: { provider: 'claude', observed_at: at(0), plan: 'Max 5x', weekly: { remaining: 0.5, resets_at: at(50) }, allowances: [] },
    }));
  }
  dashboard.providers = [...accounts, dashboard.providers[1]];
  return dashboard;
}

const carousel = (page) => page.getByTestId('account-carousel');
const position = (page) => page.getByTestId('carousel-position');

test('several Claude accounts become one carousel that opens on the Overview', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard() });
  await expect(carousel(page)).toHaveCount(1);
  // Codex keeps its own card.
  await expect(page.locator('[data-provider-id="codex-main"]')).toBeVisible();
  await expect(position(page)).toHaveText(/^Overview/);
  await expect(carousel(page)).toContainText('Claude · 3 accounts · 1 dispatching · 1 needs attention');
  // Rows in the service's order, each with its own numbers; nothing blended.
  const rows = page.getByTestId('overview-row');
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(0)).toContainText('Client — Northwind');
  await expect(rows.nth(1)).toContainText('Personal');
  await expect(rows.nth(1)).toContainText('Max 20x');
  await expect(rows.nth(1)).toContainText('68%');
  await expect(rows.nth(1)).toContainText('41%');
  await expect(carousel(page)).not.toContainText('average');
});

test('an out-of-date account shows last-known values and no countdown', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard() });
  const side = page.getByTestId('overview-row').nth(2);
  await expect(side).toContainText('was 64%');
  await expect(side).toContainText('was 41%');
  await expect(side).toContainText('Out of date');
  // Fixed times only: 'due …' for a passed reset, a day name for the next.
  await expect(side).toContainText('due ');
  await expect(side).not.toContainText(/\bin \d/);
  // The full sentence is in the row's accessible name.
  await expect(side).toHaveAttribute('aria-label', /reset was due .*last reported reset/);
  await side.click();
  await expect(position(page)).toHaveText('Account 3 of 3');
  const pageEl = page.getByTestId('carousel-page-3');
  await expect(pageEl.getByTestId('stale-meters')).toContainText('Reset was due');
  await expect(pageEl.getByTestId('stale-meters')).toContainText('awaiting fresh numbers');
  await expect(pageEl.getByTestId('stale-meters')).toContainText('Last known: 1 available · 1 since expired');
  await expect(pageEl.getByTestId('stale-meters')).not.toContainText(/Resets in/);
});

test('a live account page lists every credit deadline apart from window resets', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard() });
  await page.getByTestId('overview-row').nth(1).click();
  await expect(position(page)).toHaveText('Account 2 of 3');
  const credits = page.getByTestId('carousel-page-2').getByTestId('banked-resets');
  await expect(credits).toContainText('2 available');
  await expect(credits).toContainText('Credit deadlines:');
});

test('previous, next, Overview, keyboard and swipe all move between pages', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard() });
  await page.getByRole('button', { name: 'Next account' }).click();
  await expect(position(page)).toHaveText('Account 1 of 3');
  await expect(page.getByTestId('carousel-announcement')).toHaveText('Claude account 1 of 3, Client — Northwind long-running engagement');
  await page.getByRole('button', { name: 'Next account' }).click();
  await expect(position(page)).toHaveText('Account 2 of 3');
  await page.getByRole('button', { name: 'Previous account' }).click();
  await expect(position(page)).toHaveText('Account 1 of 3');
  // Keyboard: arrows move, Escape returns to the Overview.
  await page.keyboard.press('ArrowRight');
  await expect(position(page)).toHaveText('Account 2 of 3');
  await page.keyboard.press('Escape');
  await expect(position(page)).toHaveText(/^Overview/);
  await page.getByRole('button', { name: 'Next account' }).click();
  await page.getByRole('button', { name: '‹ Overview' }).click();
  await expect(position(page)).toHaveText(/^Overview/);
  // › is disabled on the last page.
  for (let i = 0; i < 3; i++) await page.getByRole('button', { name: 'Next account' }).click();
  await expect(page.getByRole('button', { name: 'Next account' })).toBeDisabled();
});

test('the carousel keeps one height on every page so the card below does not move', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard() });
  const codex = page.locator('[data-provider-id="codex-main"]');
  const heights = [], tops = [];
  for (let p = 0; p <= 3; p++) {
    if (p > 0) await page.getByRole('button', { name: 'Next account' }).click();
    heights.push((await carousel(page).boundingBox()).height);
    tops.push((await codex.boundingBox()).y);
  }
  expect(new Set(heights.map(Math.round)).size, `heights ${heights}`).toBe(1);
  expect(new Set(tops.map(Math.round)).size, `codex tops ${tops}`).toBe(1);
});

test('the selected account survives a refresh and a reorder', async ({ page }) => {
  const dashboard = multiAccountDashboard();
  await loadMobileDashboard(page, { dashboard });
  await page.getByTestId('overview-row').nth(1).click();
  await expect(position(page)).toHaveText('Account 2 of 3');
  // A live update with the accounts in a different order keeps Personal.
  await page.evaluate((next) => {
    for (const source of window.__redlineEventSources) source.emit('dashboard', next);
  }, { ...dashboard, providers: [dashboard.providers[1], dashboard.providers[0], dashboard.providers[2], dashboard.providers[3]] });
  // Personal moved to the front: selection follows the id, not the index.
  await expect(position(page)).toHaveText('Account 1 of 3');
  await expect(page.getByTestId('carousel-page-1')).toContainText('Personal');
  // Removed from the configuration: back to the Overview, not a neighbour.
  await page.evaluate((next) => {
    for (const source of window.__redlineEventSources) source.emit('dashboard', next);
  }, { ...dashboard, providers: [dashboard.providers[0], dashboard.providers[2], dashboard.providers[3]] });
  await expect(position(page)).toHaveText(/^Overview/);
});

test('page changes keep focus on the control used and announce once', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard() });
  const next = page.getByRole('button', { name: 'Next account' });
  await next.focus();
  await page.keyboard.press('Enter');
  await page.keyboard.press('Enter');
  await expect(position(page)).toHaveText('Account 2 of 3');
  await expect(next).toBeFocused();
  await expect(page.getByTestId('carousel-announcement')).toHaveText('Claude account 2 of 3, Personal');
  // A data refresh re-renders without moving focus or re-announcing.
  await page.evaluate(() => document.getElementById('m-carousel-announcer').textContent = 'sentinel');
  await page.evaluate((next) => {
    for (const source of window.__redlineEventSources) source.emit('dashboard', next);
  }, multiAccountDashboard());
  await expect(next).toBeFocused();
  await expect(page.getByTestId('carousel-announcement')).toHaveText('sentinel');
  // Pages are labelled slides.
  await expect(page.getByRole('group', { name: 'Account 2 of 3' })).toBeVisible();
});

test('a stale account page has no countdown anywhere, including the decision', async ({ page }) => {
  const dashboard = multiAccountDashboard();
  dashboard.providers[2].latest_decision = { decision: 'WAIT', reason: 'x', projected_trigger_at: at(5) };
  await loadMobileDashboard(page, { dashboard });
  await page.getByTestId('overview-row').nth(2).click();
  const stalePage = page.getByTestId('carousel-page-3');
  await expect(stalePage).not.toContainText(/Resets in|Likely eligible|in \d+ (min|hr|day)/);
  await expect(stalePage.getByRole('button', { name: 'Pause Side project' })).toBeVisible();
});

test('the height holds after switching tabs and resizing elsewhere', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard() });
  const before = (await carousel(page).boundingBox()).height;
  await page.getByRole('tab', { name: /Queue/ }).click();
  await page.setViewportSize({ width: 400, height: 900 });
  await page.waitForTimeout(250);
  await page.getByRole('tab', { name: /Usage/ }).click();
  const heights = [];
  for (let p = 0; p <= 3; p++) {
    if (p > 0) await page.getByRole('button', { name: 'Next account' }).click();
    heights.push(Math.round((await carousel(page).boundingBox()).height));
  }
  expect(new Set(heights).size, `heights ${heights} (before ${before})`).toBe(1);
});

test('a single Claude account keeps today\'s card', async ({ page }) => {
  await loadMobileDashboard(page);
  await expect(carousel(page)).toHaveCount(0);
  await expect(page.locator('[data-provider-id="claude-main"]')).toBeVisible();
});

test('large text wraps instead of clipping and the height stays fixed', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard() });
  // A 1.6x text size, applied once per element, like a
  // phone's font scale: every computed px font size grows by 1.6.
  await page.evaluate(() => {
    // Sizes are read for every new node first, then set, so a child never
    // scales its already-scaled parent's size (no compounding).
    const scale = (root) => {
      const fresh = [...root.querySelectorAll('*')].filter(el => !el.dataset.scaled);
      const sizes = fresh.map(el => parseFloat(getComputedStyle(el).fontSize));
      fresh.forEach((el, i) => { el.dataset.scaled = '1'; el.style.fontSize = `${sizes[i] * 1.6}px`; });
    };
    const list = document.querySelector('#m-usage-list');
    scale(list);
    // Simulated with inline styles, so re-rendered nodes are scaled just
    // after the render measured them; each step below re-measures on resize
    // (as the page does for a real font-scale or rotation change).
    new MutationObserver(() => scale(list)).observe(list, { childList: true, subtree: true });
    window.dispatchEvent(new Event('resize'));
  });
  await page.waitForTimeout(250);
  // Keyboard, not taps: the sticky header covers the strip at this size.
  await page.getByRole('button', { name: 'Next account' }).focus();
  await page.keyboard.press('Enter');
  await expect(position(page)).toHaveText('Account 1 of 3');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(250);
  // Every text node inside the carousel lies within its box.
  const overflow = await carousel(page).evaluate((section) => {
    const box = section.getBoundingClientRect();
    return [...section.querySelectorAll('*')].filter(el => !el.closest('[hidden]') && el.children.length === 0 && el.textContent.trim())
      .filter(el => { const r = el.getBoundingClientRect(); return r.width && (r.right > box.right + 1 || r.left < box.left - 1); })
      .map(el => el.textContent.trim()).slice(0, 5);
  });
  expect(overflow).toEqual([]);
  // And the height is still one fixed height across pages.
  const heights = [];
  for (let p = 0; p <= 3; p++) {
    if (p > 0) { await page.getByRole('button', { name: 'Next account' }).focus(); await page.keyboard.press('Enter'); }
    await page.evaluate(() => window.dispatchEvent(new Event('resize')));
    await page.waitForTimeout(200);
    heights.push(Math.round((await carousel(page).boundingBox()).height));
  }
  expect(new Set(heights).size, `heights at 1.6x ${heights}`).toBe(1);
  await page.screenshot({ path: process.env.REDLINE_WEB_SCREENSHOTS ? `${process.env.REDLINE_WEB_SCREENSHOTS}/web-05-large-text-account-3.png` : undefined, fullPage: false });
});

test('opening details on a page keeps keyboard focus there', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard() });
  await page.getByTestId('overview-row').nth(1).click();
  const summary = page.getByTestId('carousel-page-2').locator('summary');
  await summary.focus();
  await page.keyboard.press('Enter');
  await expect(summary).toBeFocused();
  // And the height still matches the other pages after it opened.
  await page.getByRole('button', { name: 'Next account' }).click();
  await page.getByRole('button', { name: 'Previous account' }).click();
  const a = (await carousel(page).boundingBox()).height;
  await page.getByRole('button', { name: '‹ Overview' }).click();
  const b = (await carousel(page).boundingBox()).height;
  expect(Math.round(a)).toBe(Math.round(b));
});

test('the single-account card shows the same lines as before', async ({ page }) => {
  await loadMobileDashboard(page);
  const card = page.locator('[data-provider-id="claude-main"]');
  await expect(card).toContainText(/Resets in \d+ hrs? · /);
  await expect(card).not.toContainText('was ');
  await expect(card.locator('progress')).not.toHaveCount(0);
});

test('many accounts: the overview is the tallest page and the height still holds', async ({ page }) => {
  await loadMobileDashboard(page, { dashboard: multiAccountDashboard({ extraClaude: 8 }) });
  const codex = page.locator('[data-provider-id="codex-main"]');
  const heights = [], tops = [];
  for (let p = 0; p <= 2; p++) {
    if (p > 0) await page.getByRole('button', { name: 'Next account' }).click();
    heights.push(Math.round((await carousel(page).boundingBox()).height));
    tops.push(Math.round((await codex.boundingBox()).y));
  }
  expect(new Set(heights).size, `heights ${heights}`).toBe(1);
  expect(new Set(tops).size, `codex tops ${tops}`).toBe(1);
});


// Synthetic Team seats: OpenUsage reports a fresh 5-hour
// window, a Fable model window and 0 credits, but no account-wide weekly
// window. The page must say "not reported" -- never 0%, and never the 5-hour
// number standing in -- and keep the fresh numbers it does have.
test('a Team seat with no weekly limit shows its usage and "not reported"', async ({ page }) => {
  const dashboard = dashboardFixture();
  const base = dashboard.providers[0];
  const seat = (id, label, short, extra = {}) => ({
    ...base, id, label, provider: 'claude', usage_source: { active: 'openusage' }, account_status: 'ok',
    latest_decision: { decision: 'UNKNOWN', reason: 'weekly limit not reported; dispatch holds until a policy exists for this account type' },
    snapshot: {
      provider: 'claude', observed_at: at(0), plan: 'Team 5x', weekly_unavailable: true, banked_resets: 0,
      allowances: [{ key: 'model:fable:weekly', source_label: 'Fable', scope: 'model', role: 'weekly', remaining: 1, resets_at: at(120) }],
      ...short, ...extra,
    },
  });
  dashboard.providers = [
    seat('claude-5ea70001', 'Delta', { short_window_not_started: true }),
    seat('claude-5ea70003', 'Echo', { short: { remaining: 0.76, resets_at: at(2.5) } }),
    { ...base, id: 'claude-main', provider: 'claude', label: 'claude-main', account_status: 'ok',
      latest_decision: { decision: 'RUN', reason: 'surplus' },
      snapshot: { provider: 'claude', observed_at: at(0), plan: 'Max 5x', short: { remaining: 0.5, resets_at: at(3) }, weekly: { remaining: 0.45, resets_at: at(70) }, allowances: [] } },
    dashboard.providers[1],
  ];
  await loadMobileDashboard(page, { dashboard });
  await expect(carousel(page)).toContainText('Claude · 3 accounts · 1 dispatching');
  await expect(carousel(page)).not.toContainText('need');
  const rows = page.getByTestId('overview-row');
  await expect(rows.nth(1)).toContainText('76%');
  await expect(rows.nth(1)).toContainText('n/r');
  await expect(rows.nth(1)).toContainText('Dispatch held · weekly not reported');
  await expect(rows.nth(1)).toHaveAttribute('aria-label', /Weekly, not reported/);
  await expect(rows.nth(1)).not.toHaveAttribute('aria-label', /Weekly, 76/);
  await expect(rows.nth(2)).toContainText('45%');
  await rows.nth(1).click();
  const echo = page.getByTestId('carousel-page-2');
  await expect(echo.getByTestId('weekly-not-reported')).toContainText('Not reported');
  await expect(echo).toContainText('Fable');
  await expect(echo.getByTestId('banked-resets')).toContainText('0 available');
  // Exactly one 76% on the page: the 5-hour window.
  await expect(echo.getByText('76%', { exact: false })).toHaveCount(1);
});
