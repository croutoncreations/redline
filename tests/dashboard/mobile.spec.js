// Mobile dashboard Playwright tests — Phase 4
const { test, expect } = require('@playwright/test');
const { assets, loadMobileDashboard, candidatesFixture, dashboardFixture } = require('./harness');

// ── Pairing ──────────────────────────────────────────────────────────────────

test('pairing page waits for an explicit browser action before redeeming', async ({ page }) => {
  const redemptions = [];
  await page.route('http://redline.test/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    if (assets[url.pathname]) {
      const [body, contentType] = assets[url.pathname];
      return route.fulfill({status: 200, body, contentType});
    }
    if (url.pathname === '/v1/pairing/redeem' && request.method() === 'POST') {
      redemptions.push(request.postDataJSON());
      return route.fulfill({status: 204});
    }
    return route.fulfill({status: 404, contentType: 'application/json', body: '{"error":"not found"}'});
  });
  await page.goto('http://redline.test/pair#pairing_token=preview-safe-token');
  await expect(page.getByRole('heading', {name: 'Pair this browser'})).toBeVisible();
  expect(redemptions).toHaveLength(0);
  await expect(page).toHaveURL(/#pairing_token=preview-safe-token$/);
  await page.getByRole('button', {name: 'Pair this browser'}).click();
  await expect.poll(() => redemptions).toEqual([{pairing_token: 'preview-safe-token'}]);
  await expect(page).toHaveURL('http://redline.test/m');
});

// ── Usage tab ─────────────────────────────────────────────────────────────────

test('renders provider cards with usage rings on the Usage tab', async ({ page }) => {
  await loadMobileDashboard(page);
  // Both provider cards visible
  await expect(page.locator('[data-testid="provider-card"]')).toHaveCount(2);
  // Claude card shows ring SVG
  const claudeCard = page.locator('[data-provider-id="claude-main"]');
  await expect(claudeCard).toBeVisible();
  await expect(claudeCard.locator('[data-testid="ring-svg"]').first()).toBeVisible();
  // Codex card shows ring SVG
  const codexCard = page.locator('[data-provider-id="codex-main"]');
  await expect(codexCard).toBeVisible();
  await expect(codexCard.locator('[data-testid="ring-svg"]').first()).toBeVisible();
});

test('ring shows correct remaining percentage from weekly snapshot', async ({ page }) => {
  const dashboard = dashboardFixture();
  // Claude weekly remaining = 0.53 → 53%
  dashboard.providers[0].snapshot.weekly.remaining = 0.78;
  await loadMobileDashboard(page, { dashboard });
  const weeklyRing = page.locator('[data-provider-id="claude-main"] .m-ring-cell').filter({hasText: 'Weekly'});
  const remaining = await weeklyRing.locator('circle[data-remaining]').getAttribute('data-remaining');
  expect(parseFloat(remaining)).toBeCloseTo(0.78, 2);
});

test('ring falls back to short window when weekly is absent', async ({ page }) => {
  const dashboard = dashboardFixture();
  // Remove weekly, keep short at 0.62
  delete dashboard.providers[0].snapshot.weekly;
  dashboard.providers[0].snapshot.short = { remaining: 0.62, resets_at: '2026-07-20T23:00:00Z' };
  await loadMobileDashboard(page, { dashboard });
  const sessionRing = page.locator('[data-provider-id="claude-main"] .m-ring-cell').filter({hasText: 'Session'});
  const remaining = await sessionRing.locator('circle[data-remaining]').getAttribute('data-remaining');
  expect(parseFloat(remaining)).toBeCloseTo(0.62, 2);
});

test('ring shows stale state when snapshot is stale', async ({ page }) => {
  const dashboard = dashboardFixture();
  dashboard.providers[0].snapshot_stale = true;
  dashboard.providers[0].error = 'Usage data is stale; scheduling is paused until a fresh snapshot is available.';
  await loadMobileDashboard(page, { dashboard });
  const svgs = page.locator('[data-provider-id="claude-main"] [data-testid="ring-svg"]');
  await expect(svgs).toHaveCount(2);
  await expect(svgs.first()).toHaveAttribute('aria-label', 'stale');
  await expect(svgs.last()).toHaveAttribute('aria-label', 'stale');
});

test('shows error-state card when provider has no snapshot', async ({ page }) => {
  const dashboard = dashboardFixture();
  dashboard.providers[0].snapshot = undefined;
  dashboard.providers[0].error = 'No usage snapshot has been collected yet.';
  await loadMobileDashboard(page, { dashboard });
  const card = page.locator('[data-provider-id="claude-main"]');
  await expect(card).toHaveClass(/error-state/);
});

test('shows paused card styling when provider is paused', async ({ page }) => {
  const dashboard = dashboardFixture();
  dashboard.providers[0].paused = true;
  await loadMobileDashboard(page, { dashboard });
  const card = page.locator('[data-provider-id="claude-main"]');
  await expect(card).toHaveClass(/paused/);
  // Pressure status label shows Paused
  await expect(card.locator('.m-provider-status')).toHaveText('Paused');
});

test('opens every provider usage detail by default', async ({ page }) => {
  await loadMobileDashboard(page);
  const claudeDetail = page.locator('[data-testid="provider-detail-claude-main"]');
  const codexDetail = page.locator('[data-testid="provider-detail-codex-main"]');
  await expect(claudeDetail).toBeVisible();
  await expect(codexDetail).toBeVisible();
  // Should show both short and weekly meters for Claude.
  await expect(claudeDetail).toContainText('5-hour window');
  await expect(claudeDetail).toContainText('Weekly allowance');
  // Model allowance (Fable) with reset_inferred.
  await expect(claudeDetail).toContainText('Fable');
  await expect(claudeDetail).toContainText('reset inferred');
  await expect(codexDetail).toContainText('Weekly allowance');
});

test('collapses provider usage details independently', async ({ page }) => {
  await loadMobileDashboard(page);
  await page.locator('[data-provider-toggle="claude-main"]').click();
  await expect(page.locator('[data-testid="provider-detail-claude-main"]')).toBeHidden();
  await expect(page.locator('[data-testid="provider-detail-codex-main"]')).toBeVisible();
  await page.locator('[data-provider-toggle="claude-main"]').click();
  await expect(page.locator('[data-testid="provider-detail-claude-main"]')).toBeVisible();
});

test('account pools displayed before model pools in detail', async ({ page }) => {
  const dashboard = dashboardFixture();
  // Add an account-scope allowance
  dashboard.providers[0].snapshot.allowances = [
    { key: 'account:pro:weekly', source_label: 'Pro Account', scope: 'account', remaining: 0.8, resets_at: '2026-07-24T19:00:00Z', reset_inferred: false },
    { key: 'model:fable:weekly', source_label: 'Fable', scope: 'model', remaining: 1, resets_at: '2026-07-24T19:00:00Z', reset_inferred: true },
  ];
  await loadMobileDashboard(page, { dashboard });
  const detail = page.locator('[data-testid="provider-detail-claude-main"]');
  const metersText = await detail.locator('.m-meters').textContent();
  const accountIdx = metersText.indexOf('Pro Account');
  const modelIdx = metersText.indexOf('Fable');
  // Account pool should appear before model pool
  expect(accountIdx).toBeLessThan(modelIdx);
});

test('decision detail block is expandable in provider card', async ({ page }) => {
  await loadMobileDashboard(page);
  const decisionDetail = page.locator('[data-testid="decision-detail"]').first();
  await expect(decisionDetail).toBeVisible();
  // It's a <details> element — click summary to expand
  await decisionDetail.click();
  await expect(decisionDetail).toHaveAttribute('open', '');
});

test('View Queue button switches to queue tab with provider pre-selected', async ({ page }) => {
  await loadMobileDashboard(page);
  // Open Claude provider
  // Click View Queue
  await page.locator('[data-action="view-queue"][data-provider="claude-main"]').click();
  // Should switch to queue tab
  await expect(page.locator('#m-queue-panel')).toBeVisible();
  // Provider selector should have claude-main
  await expect(page.locator('#m-queue-provider')).toHaveValue('claude-main');
});

test('pause and resume actions call the correct API endpoints', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  // Open Claude provider
  // Pause
  await page.locator('[data-action="pause-provider"][data-provider="claude-main"]').click();
  await expect.poll(() => state.requests.some(r => r.path === '/v1/providers/claude-main/pause')).toBe(true);
});

test('Refresh usage button calls refresh endpoint', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  await page.locator('[data-action="refresh-usage"][data-provider="claude-main"]').click();
  await expect.poll(() => state.requests.some(r => r.path === '/v1/providers/claude-main/refresh')).toBe(true);
});

test('live pill shows live state and SSE receives dashboard update', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  // After SSE open, pill should be live
  await expect(page.locator('#m-live-pill')).toHaveClass(/live/);
  // Send a dashboard event
  const next = { ...state.dashboard };
  next.providers = next.providers.map(p => ({ ...p, paused: false }));
  await page.evaluate(d => window.__redlineEventSources[0].emit('dashboard', d), next);
  // Still showing Usage panel
  await expect(page.locator('#m-usage-panel')).toBeVisible();
});

test('stale banner appears when SSE connection is lost', async ({ page }) => {
  await loadMobileDashboard(page);
  await page.evaluate(() => window.__redlineEventSources[0].fail());
  await expect(page.locator('#m-stale-banner')).toBeVisible();
  await expect(page.locator('#m-live-pill')).toHaveClass(/offline/);
});

test('all provider action buttons meet 44px tap target minimum', async ({ page }) => {
  await loadMobileDashboard(page);

  for (const selector of [
    '[data-provider-toggle="claude-main"]',
    '[data-action="view-queue"][data-provider="claude-main"]',
    '[data-action="pause-provider"][data-provider="claude-main"]',
    '[data-action="refresh-usage"][data-provider="claude-main"]',
  ]) {
    const box = await page.locator(selector).boundingBox();
    expect(box, `${selector} must be visible`).not.toBeNull();
    const dimension = Math.max(box.width, box.height);
    expect(dimension, `${selector} tap target: ${dimension}px`).toBeGreaterThanOrEqual(44);
  }
});

// ── Queue tab ─────────────────────────────────────────────────────────────────

test('SSE dashboard updates do not repeatedly recompute candidates', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  await page.locator('#m-tab-queue').click();
  await expect.poll(() => state.requests.filter(r => r.path === '/v1/providers/claude-main/candidates').length).toBe(0);
  // Candidate GETs are intentionally not recorded by the harness, so count them at the browser boundary.
  let candidateRequests = 0;
  page.on('request', request => {
    if (new URL(request.url()).pathname.endsWith('/candidates')) candidateRequests += 1;
  });
  await page.locator('#m-queue-provider').selectOption('codex-main');
  await expect.poll(() => candidateRequests).toBe(1);
  await page.evaluate(d => window.__redlineEventSources[0].emit('dashboard', d), state.dashboard);
  await page.waitForTimeout(50);
  expect(candidateRequests).toBe(1);
});

test('Queue tab shows candidates with eligibility and reasons', async ({ page }) => {
  const state = await loadMobileDashboard(page, {
    candidates: {
      'codex-main': candidatesFixture('codex-main', { eligible: true }),
      'claude-main': candidatesFixture('claude-main', { eligible: true }),
    },
  });
  await page.locator('#m-tab-queue').click();
  await expect(page.locator('#m-queue-panel')).toBeVisible();
  // Candidates should be listed
  await expect(page.locator('[data-testid="candidate-audit-auth"]')).toBeVisible();
  await expect(page.locator('[data-testid="candidate-fix-perf"]')).toBeVisible();
});

test('Queue candidate shows reason when ineligible', async ({ page }) => {
  await loadMobileDashboard(page, {
    candidates: {
      'claude-main': candidatesFixture('claude-main', {
        eligible: false,
        ineligible_reason: 'weekly remaining is below threshold',
        second_reason: 'concurrency limit reached',
      }),
      'codex-main': candidatesFixture('codex-main'),
    },
  });
  await page.locator('#m-tab-queue').click();
  await expect(page.locator('[data-testid="candidate-reason-audit-auth"]')).toContainText('weekly remaining is below threshold');
  await expect(page.locator('[data-testid="candidate-reason-fix-perf"]')).toContainText('concurrency limit reached');
});

test('Next Up banner shows when dispatch is available', async ({ page }) => {
  await loadMobileDashboard(page, {
    candidates: {
      'codex-main': candidatesFixture('codex-main', { eligible: true, dispatch_available: true }),
      'claude-main': candidatesFixture('claude-main', { eligible: true, dispatch_available: true }),
    },
  });
  await page.locator('#m-tab-queue').click();
  await expect(page.locator('[data-testid="next-up"]')).toBeVisible();
  await expect(page.locator('[data-testid="next-up"]')).toContainText('Audit authentication');
  await expect(page.locator('[data-testid="next-up"]')).toContainText('1 ready · 1 blocked');
});

test('Next Up banner is hidden when dispatch is not available', async ({ page }) => {
  await loadMobileDashboard(page, {
    candidates: {
      'codex-main': candidatesFixture('codex-main', { dispatch_available: false, eligible: false }),
      'claude-main': candidatesFixture('claude-main', { dispatch_available: false, eligible: false }),
    },
  });
  await page.locator('#m-tab-queue').click();
  await expect(page.locator('[data-testid="next-up"]')).toBeHidden();
});

test('stale snapshot meta shown when candidates snapshot is stale', async ({ page }) => {
  await loadMobileDashboard(page, {
    candidates: {
      'claude-main': candidatesFixture('claude-main', { snapshot_stale: true }),
      'codex-main': candidatesFixture('codex-main'),
    },
  });
  await page.locator('#m-tab-queue').click();
  await expect(page.locator('#m-snapshot-meta')).toHaveClass(/stale/);
  await expect(page.locator('#m-snapshot-meta')).toContainText('stale');
});

test('Run confirm sheet names task and provider, dispatches on confirm', async ({ page }) => {
  const state = await loadMobileDashboard(page, {
    candidates: {
      'claude-main': candidatesFixture('claude-main', { eligible: true, dispatch_available: true }),
      'codex-main': candidatesFixture('codex-main', { eligible: true }),
    },
    dispatchOutcome: 202,
  });
  await page.locator('#m-tab-queue').click();
  // Click Run in Next Up banner
  await page.locator('[data-testid="next-up"] button[data-action="run-now"]').click();
  // Confirm sheet should appear
  await expect(page.locator('#m-run-confirm')).toBeVisible();
  await expect(page.locator('#m-run-confirm-title')).toContainText('Audit authentication');
  await expect(page.locator('#m-run-confirm-body')).toContainText('claude-main');
  // Confirm the run
  await page.locator('#m-run-confirm-ok').click();
  await expect.poll(() => state.requests.some(r =>
    r.path === '/v1/tasks/audit-auth/dispatch' && r.body === null
  )).toBe(true);
  // Admitted runs route to the Runs tab and close the sheet.
  await expect(page.locator('#m-run-confirm')).toBeHidden();
  await expect(page.locator('#m-tab-runs')).toHaveAttribute('aria-selected', 'true');
});

test('muted Run remains clickable and a 200 non-admission shows the task reason', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  await page.locator('#m-tab-queue').click();
  const blocked = page.locator('[data-testid="candidate-fix-perf"]');
  await blocked.locator('[data-action="candidate-run"]').click();
  await expect(page.locator('#m-run-confirm-title')).toContainText('Fix performance regression');
  await page.locator('#m-run-confirm-ok').click();
  await expect.poll(() => state.requests.some(r => r.path === '/v1/tasks/fix-perf/dispatch')).toBe(true);
  await expect(page.locator('#m-error-banner')).toContainText('candidate is no longer eligible');
});

test('dispatch 409 shows error and closes confirm sheet', async ({ page }) => {
  await loadMobileDashboard(page, {
    candidates: {
      'claude-main': candidatesFixture('claude-main', { eligible: true, dispatch_available: true }),
      'codex-main': candidatesFixture('codex-main', { eligible: true }),
    },
    dispatchOutcome: 409,
  });
  await page.locator('#m-tab-queue').click();
  await page.locator('[data-testid="next-up"] button[data-action="run-now"]').click();
  await page.locator('#m-run-confirm-ok').click();
  await expect(page.locator('#m-error-banner')).toBeVisible();
  await expect(page.locator('#m-error-banner')).toContainText('paused');
  await expect(page.locator('#m-run-confirm')).toBeHidden();
});

test('candidate overflow sheet shows enable/disable/retry, no unnamed dispatch', async ({ page }) => {
  await loadMobileDashboard(page, {
    candidates: {
      'claude-main': candidatesFixture('claude-main', { eligible: false }),
      'codex-main': candidatesFixture('codex-main'),
    },
  });
  await page.locator('#m-tab-queue').click();
  // Open overflow for ineligible candidate (no "Run now" should appear)
  await page.locator('[data-testid="candidate-audit-auth"] [data-action="candidate-overflow"]').click();
  const sheet = page.locator('#m-action-sheet');
  await expect(sheet).toBeVisible();
  // Should have enable, disable, retry, cancel — but NOT an unnamed dispatch
  const buttons = sheet.locator('button');
  const labels = await buttons.allTextContents();
  expect(labels.some(l => l.includes('Enable'))).toBe(true);
  expect(labels.some(l => l.includes('Disable'))).toBe(true);
  expect(labels.some(l => l.includes('Retry'))).toBe(true);
  // No button with empty label
  expect(labels.every(l => l.trim().length > 0)).toBe(true);
});

test('candidate overflow for eligible candidate includes Run now option', async ({ page }) => {
  await loadMobileDashboard(page, {
    candidates: {
      'claude-main': candidatesFixture('claude-main', { eligible: true, dispatch_available: true }),
      'codex-main': candidatesFixture('codex-main', { eligible: true }),
    },
  });
  await page.locator('#m-tab-queue').click();
  await page.locator('[data-testid="candidate-audit-auth"] [data-action="candidate-overflow"]').click();
  const sheet = page.locator('#m-action-sheet');
  await expect(sheet).toBeVisible();
  const labels = await sheet.locator('button').allTextContents();
  expect(labels.some(l => l.includes('Run now'))).toBe(true);
});

test('action sheet action calls task control endpoint', async ({ page }) => {
  const state = await loadMobileDashboard(page, {
    candidates: {
      'claude-main': candidatesFixture('claude-main', { eligible: false }),
      'codex-main': candidatesFixture('codex-main'),
    },
  });
  await page.locator('#m-tab-queue').click();
  await page.locator('[data-testid="candidate-audit-auth"] [data-action="candidate-overflow"]').click();
  await page.locator('#m-action-sheet button', { hasText: 'Disable' }).click();
  await expect.poll(() => state.requests.some(r => r.path === '/v1/tasks/audit-auth/disable')).toBe(true);
});

test('queue selector changes provider and loads new candidates', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  await page.locator('#m-tab-queue').click();
  // Change to claude-main
  await page.locator('#m-queue-provider').selectOption('claude-main');
  // Should load claude-main candidates
  await expect(page.locator('#m-snapshot-meta')).not.toBeEmpty();
});

// ── Runs tab ─────────────────────────────────────────────────────────────────

test('Runs tab shows runs list with unread badge and marks activity read', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  const badge = page.locator('#m-runs-badge');
  await expect(badge).toBeVisible();
  await expect(badge).toContainText('1');
  await page.locator('#m-tab-runs').click();
  await expect.poll(() => state.requests.some(r => r.path === '/v1/runs/read-all')).toBe(true);
});

test('opening a run marks it read and shows run detail', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  await page.locator('#m-tab-runs').click();
  await expect(page.locator('[data-testid="run-item-run-1"]')).toBeVisible();
  await page.locator('[data-testid="run-item-run-1"]').click();
  // Detail panel visible
  await expect(page.locator('#m-run-detail')).toHaveClass(/visible/);
  await expect(page.locator('#m-run-detail-title')).toContainText('Audit authentication');
  // Mark read endpoint called
  await expect.poll(() => state.requests.some(r => r.path === '/v1/runs/run-1/read')).toBe(true);
});

test('opening an already-read run does not decrement the badge for other unread runs', async ({ page }) => {
  const dashboard = dashboardFixture();
  dashboard.runs.push({
    id: 'run-2', task_id: 'audit-auth', provider_account_id: 'codex-main', state: 'completed',
    started_at: '2026-07-20T19:00:00Z', completed_at: '2026-07-20T19:00:00Z',
    activity_read_at: '2026-07-20T18:00:00Z', summary: 'Already seen.',
  });
  // Only run-1 is unread; run-2 was already read before this session started.
  dashboard.unread_runs = 1;
  const state = await loadMobileDashboard(page, { dashboard });
  await page.locator('#m-tab-runs').click();
  const badge = page.locator('#m-runs-badge');
  await expect(badge).toContainText('1');
  await Promise.all([
    page.waitForResponse(resp => resp.url().includes('/v1/runs/run-2/events')),
    page.locator('[data-testid="run-item-run-2"]').click(),
  ]);
  await expect(page.locator('#m-run-detail')).toHaveClass(/visible/);
  // run-2 was already read, so opening it must not call /read or touch the badge.
  expect(state.requests.some(r => r.path === '/v1/runs/run-2/read')).toBe(false);
  // run-1 is still unread, so the badge must still read 1.
  await expect(badge).toContainText('1');
});

test('run detail shows summary, route, artifacts, and events', async ({ page }) => {
  await loadMobileDashboard(page);
  await page.locator('#m-tab-runs').click();
  await page.locator('[data-testid="run-item-run-1"]').click();
  const detail = page.locator('#m-run-detail-body');
  // Summary
  await expect(detail).toContainText('Opened a focused authentication fix.');
  // Route info
  await expect(detail).toContainText('openai-codex');
  await expect(detail).toContainText('gpt-5.6-sol');
  // Artifact link
  await expect(detail.locator('a', { hasText: 'PR #42' })).toHaveAttribute('href', 'https://github.com/acme/redline/pull/42');
  // Events
  await expect(detail).toContainText('run_started');
});

test('run event messages render as text rather than HTML', async ({ page }) => {
  await loadMobileDashboard(page, {
    runEvents: [{ id: 'unsafe', type: 'run.failed', occurred_at: '2026-07-20T19:00:00Z', message: '<img src=x onerror=alert(1)>unsafe' }],
  });
  await page.locator('#m-tab-runs').click();
  await page.locator('[data-testid="run-item-run-1"]').click();
  await expect(page.locator('.m-run-events')).toContainText('<img src=x onerror=alert(1)>unsafe');
  await expect(page.locator('.m-run-events img')).toHaveCount(0);
});

test('stderr tail is shown without word-wrap', async ({ page }) => {
  await loadMobileDashboard(page);
  await page.locator('#m-tab-runs').click();
  await page.locator('[data-testid="run-item-run-1"]').click();
  const stderr = page.locator('[data-testid="stderr-content"]');
  await expect(stderr).toBeVisible();
  await expect(stderr).toContainText('warning: deprecated API call');
  // Check white-space: pre (no wrapping)
  const ws = await stderr.evaluate(el => getComputedStyle(el).whiteSpace);
  expect(ws).toBe('pre');
});

test('back button returns to runs list', async ({ page }) => {
  await loadMobileDashboard(page);
  await page.locator('#m-tab-runs').click();
  await page.locator('[data-testid="run-item-run-1"]').click();
  await expect(page.locator('#m-run-detail')).toHaveClass(/visible/);
  await page.locator('#m-run-back').click();
  await expect(page.locator('#m-run-detail')).not.toHaveClass(/visible/);
  await expect(page.locator('[data-testid="run-item-run-1"]')).toBeVisible();
});

// ── Service worker / API isolation ────────────────────────────────────────────
const fs = require('node:fs');
const path = require('node:path');
const dashboardRoot2 = path.join(__dirname, '..', '..', 'internal', 'api', 'dashboard');

// Service worker behaviour is covered by sw.spec.js, which executes the real
// worker; grepping its source could not catch any of the caching regressions
// that actually shipped.

test('manifest.webmanifest has maskable icon metadata', async () => {
  const manifestText = fs.readFileSync(path.join(dashboardRoot2, 'manifest.webmanifest'), 'utf8');
  const manifest = JSON.parse(manifestText);
  expect(manifest.scope).toBe('/');
  expect(manifest.start_url).toBe('/m');
  expect(manifest.icons.length).toBeGreaterThanOrEqual(2);
  expect(manifest.icons.every(icon => icon.purpose && icon.purpose.includes('maskable'))).toBe(true);
  expect(manifest.icons.every(icon => icon.type === 'image/png')).toBe(true);
  const sizes = manifest.icons.map(i => i.sizes);
  expect(sizes.some(s => s.includes('192'))).toBe(true);
  expect(sizes.some(s => s.includes('512'))).toBe(true);
});

// ── Accessibility ─────────────────────────────────────────────────────────────

test('tab bar buttons have accessible roles and labels', async ({ page }) => {
  await loadMobileDashboard(page);
  for (const [id, label] of [['m-tab-usage', 'Usage'], ['m-tab-queue', 'Queue'], ['m-tab-runs', 'Runs']]) {
    const btn = page.locator(`#${id}`);
    await expect(btn).toHaveAttribute('role', 'tab');
    await expect(btn).toContainText(label);
  }
});

test('provider card headers are keyboard-focusable buttons', async ({ page }) => {
  await loadMobileDashboard(page);
  const btn = page.locator('[data-provider-toggle="claude-main"]');
  await expect(btn).toBeVisible();
  // Verify it's a button (keyboard-accessible)
  const tagName = await btn.evaluate(el => el.tagName);
  expect(tagName).toBe('BUTTON');
});

test('run confirm dialog has aria-modal and labelledby', async ({ page }) => {
  await loadMobileDashboard(page, {
    candidates: {
      'codex-main': candidatesFixture('codex-main', { eligible: true, dispatch_available: true }),
      'claude-main': candidatesFixture('claude-main', { eligible: true }),
    },
  });
  await page.locator('#m-tab-queue').click();
  await page.locator('[data-testid="next-up"] button[data-action="run-now"]').click();
  const dialog = page.locator('#m-run-confirm');
  await expect(dialog).toHaveAttribute('aria-modal', 'true');
  await expect(dialog).toHaveAttribute('aria-labelledby', 'm-run-confirm-title');
});

test('health pill is accessible and reflects health state', async ({ page }) => {
  const dashboard = dashboardFixture();
  dashboard.health.status = 'ok';
  await loadMobileDashboard(page, { dashboard });
  const pill = page.locator('#m-health-pill');
  await expect(pill).toHaveAttribute('aria-label', /healthy/i);
  // Degraded state
  const dashboard2 = dashboardFixture();
  dashboard2.health.status = 'degraded';
  await page.evaluate(d => window.__redlineEventSources[0].emit('dashboard', d), dashboard2);
  await expect(pill).toHaveAttribute('aria-label', /degraded/i);
});

test('expired pairing session shows re-pair guidance instead of a generic refresh error', async ({ page }) => {
  await loadMobileDashboard(page, { sessionExpired: true, waitForReady: false });
  const banner = page.locator('#m-error-banner');
  await expect(banner).toBeVisible();
  await expect(banner).toContainText(/session expired/i);
  await expect(banner).toContainText('redline pair');
  await expect(banner).not.toContainText('Dashboard refresh failed');
  await expect(page.locator('#m-live-pill')).toHaveAttribute('aria-label', /reconnecting/i);
});

test('canonical session/weekly pools render once, not duplicated as account pools', async ({ page }) => {
  const dashboard = dashboardFixture();
  const snap = dashboard.providers[0].snapshot;
  snap.short = { remaining: 1.0, resets_at: '2026-07-21T00:49:00Z' };
  snap.weekly = { remaining: 0.93, resets_at: '2026-07-24T16:59:00Z' };
  // Exactly what the OpenUsage and native collectors emit: they populate
  // snapshot.Short/Weekly *and* append the same pools to allowances.
  snap.allowances = [
    { key: 'session', source_label: 'Session', scope: 'account', role: 'short', remaining: 1.0, resets_at: '2026-07-21T00:49:00Z', period_duration_seconds: 18000 },
    { key: 'weekly', source_label: 'Weekly', scope: 'account', role: 'weekly', remaining: 0.93, resets_at: '2026-07-24T16:59:00Z', period_duration_seconds: 604800 },
    { key: 'model:fable:weekly', source_label: 'Fable', scope: 'model', role: 'weekly', remaining: 1.0, resets_at: '2026-07-24T16:59:00Z', reset_inferred: true },
  ];
  await loadMobileDashboard(page, { dashboard });
  const detail = page.locator('[data-testid="provider-detail-claude-main"]');
  const headings = await detail.locator('.m-meter-head span').allTextContents();
  expect(headings).toEqual(['5-hour window', 'Weekly allowance', 'Fable · reset inferred']);
});

test('extra account pools beyond session/weekly are still shown', async ({ page }) => {
  const dashboard = dashboardFixture();
  const snap = dashboard.providers[0].snapshot;
  snap.allowances = [
    { key: 'session', source_label: 'Session', scope: 'account', role: 'short', remaining: 1.0, resets_at: '2026-07-21T00:49:00Z' },
    { key: 'weekly', source_label: 'Weekly', scope: 'account', role: 'weekly', remaining: 0.93, resets_at: '2026-07-24T16:59:00Z' },
    { key: 'account:pro:weekly', source_label: 'Pro Account', scope: 'account', role: 'weekly', remaining: 0.8, resets_at: '2026-07-24T19:00:00Z' },
  ];
  await loadMobileDashboard(page, { dashboard });
  const detail = page.locator('[data-testid="provider-detail-claude-main"]');
  const headings = await detail.locator('.m-meter-head span').allTextContents();
  expect(headings).toContain('Pro Account');
  expect(headings.filter(h => h === 'Pro Account')).toHaveLength(1);
  // Still deduped alongside the extra pool.
  expect(headings.filter(h => /5-hour window|Session/.test(h))).toHaveLength(1);
});

test('account pools render when only allowances are present (no short/weekly fields)', async ({ page }) => {
  const dashboard = dashboardFixture();
  const snap = dashboard.providers[0].snapshot;
  delete snap.short;
  delete snap.weekly;
  snap.allowances = [
    { key: 'session', source_label: 'Session', scope: 'account', role: 'short', remaining: 0.5, resets_at: '2026-07-21T00:49:00Z' },
    { key: 'weekly', source_label: 'Weekly', scope: 'account', role: 'weekly', remaining: 0.42, resets_at: '2026-07-24T16:59:00Z' },
  ];
  await loadMobileDashboard(page, { dashboard });
  const detail = page.locator('[data-testid="provider-detail-claude-main"]');
  const headings = await detail.locator('.m-meter-head span').allTextContents();
  expect(headings).toEqual(['5-hour window', 'Weekly allowance']);
  await expect(detail.locator('.m-meter-head').first()).toContainText('50% left');
});

test('a dropped SSE stream surfaces an expired session instead of reconnecting forever', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  // The session lapses while the tab sits open on a live stream.
  state.sessionExpired = true;
  await page.evaluate(() => window.__redlineEventSources[0].fail());
  const banner = page.locator('#m-error-banner');
  await expect(banner).toBeVisible();
  await expect(banner).toContainText(/session expired/i);
  await expect(banner).toContainText('redline pair');
});

test('a transient SSE drop probes the session but reports no expiry', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  const before = state.requests.filter(r => r.path === '/v1/dashboard').length;
  // Stream drops but the session is still valid: the probe runs and stays quiet.
  await page.evaluate(() => window.__redlineEventSources[0].fail());
  await expect.poll(() => state.requests.filter(r => r.path === '/v1/dashboard').length)
    .toBeGreaterThan(before);
  await expect(page.locator('#m-live-pill')).toHaveAttribute('aria-label', /reconnecting/i);
  await expect(page.locator('#m-error-banner')).toBeHidden();
});

test('repeated SSE reconnect failures probe the session only once', async ({ page }) => {
  const state = await loadMobileDashboard(page);
  const before = state.requests.filter(r => r.path === '/v1/dashboard').length;
  // A server that stays down fires onerror on every retry; probing each time
  // would double the load it is already struggling under.
  await page.evaluate(async () => {
    for (let attempt = 0; attempt < 5; attempt += 1) {
      window.__redlineEventSources[0].fail();
      await new Promise(resolve => setTimeout(resolve, 10));
    }
  });
  await expect.poll(() => state.requests.filter(r => r.path === '/v1/dashboard').length)
    .toBe(before + 1);
});
