const { test, expect } = require('@playwright/test');
const { loadDashboard, dashboardFixture, cursorUsageFixture } = require('./harness');

// Desktop dashboard: Cursor usage sits in the capacity rail beside the
// configured queues, labelled usage-only. Synthetic fixtures only.

function withCursor(mutate) {
  const dashboard = dashboardFixture();
  dashboard.cursor_usage = cursorUsageFixture();
  if (mutate) mutate(dashboard.cursor_usage);
  return dashboard;
}

test.describe('Desktop Cursor usage card', () => {
  test.use({ timezoneId: 'America/Los_Angeles', locale: 'en-US' });

  test('sits beside the configured queues, labelled usage only, with no queue controls', async ({ page }) => {
    const dashboard = withCursor();
    await loadDashboard(page, { dashboard });
    const strip = page.locator('#usage-strip');
    await expect(strip.locator('.provider-compact')).toHaveCount(dashboard.providers.length + 1);
    const card = page.getByTestId('cursor-card');
    await expect(card).toContainText('Cursor');
    await expect(card.locator('.cursor-plan')).toHaveText('Pro');
    await expect(card.locator('.usage-only-badge')).toHaveText('Usage only');
    await expect(page.getByTestId('cursor-state')).toHaveText('Up to date');
    await expect(card.locator('.provider-window-line')).toContainText('Grok Bot 42% left · resets in 2 days');
    await expect(card.locator('.provider-window-line')).toContainText('Cursor 100% left · Other used up');

    await page.getByRole('button', { name: /Show Cursor usage details/ }).click();
    const detail = card.locator('.provider-detail');
    await expect(detail).toBeVisible();
    await expect(page.getByTestId('cursor-usage-only')).toContainText('does not schedule, pause or submit jobs to Cursor or Grok Bot');
    await expect(detail).toContainText('OpenUsage · read 2 mins ago');
    await expect(detail.locator('select, [data-provider-policy], [data-action]')).toHaveCount(0);
    await expect(detail.locator('button')).toHaveCount(0);
    // Cursor is never a job target: profile accounts come from providers only.
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Profiles' }).click();
    const accounts = await page.locator('#profile-provider option').evaluateAll(options => options.map(option => option.value));
    expect(accounts.length).toBeGreaterThan(0);
    expect(accounts.some(value => /cursor|grok/i.test(value))).toBe(false);
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: /Show Cursor usage details/ }).click();

    const monthly = page.getByTestId('cursor-group-monthly');
    await expect(monthly).toContainText('Monthly · billing cycle');
    await expect(monthly.getByTestId('cursor-meter-autoUsage')).toContainText('100% left');
    await expect(monthly.getByTestId('cursor-meter-autoUsage')).toContainText('Sat, Aug 1, 9:16 PM PDT');
    await expect(monthly.getByTestId('cursor-meter-apiUsage')).toContainText('Used up');
    await expect(monthly.getByTestId('cursor-meter-apiUsage')).toContainText('Available again in 12 days');
    const weekly = page.getByTestId('cursor-group-weekly');
    await expect(weekly.getByTestId('cursor-meter-grokBot')).toContainText('42% left');
    await expect(weekly.getByTestId('cursor-meter-grokBot')).toContainText('Wed, Jul 22, 9:22 PM PDT');
    await expect(page.getByTestId('cursor-balance-credits')).toContainText('$22.23 available');
    await expect(page.getByTestId('cursor-group-on_demand')).toHaveCount(0);
  });

  test('keeps on-demand spend separate and without an invented limit', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCursor(report => {
      report.meters.push({ key: 'onDemand', label: 'On-demand', group: 'on_demand', unit: 'usd', used: 4.5, resets_at: '2026-08-02T04:16:45Z' });
    }) });
    await page.getByRole('button', { name: /Show Cursor usage details/ }).click();
    const onDemand = page.getByTestId('cursor-group-on_demand').getByTestId('cursor-meter-onDemand');
    await expect(onDemand).toContainText('$4.50 spent');
    await expect(onDemand.locator('progress')).toHaveCount(0);
  });

  test('turns out of date when its expiry passes on this clock, without new data', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCursor() });
    await expect(page.getByTestId('cursor-state')).toHaveText('Up to date');
    // Expired at 19:03Z, but within the grace OpenUsage is only rotating.
    await page.clock.fastForward('04:00');
    await expect(page.getByTestId('cursor-state')).toHaveText('Up to date');
    // Past the grace: the source missed its refresh.
    await page.clock.fastForward('02:00');
    await expect(page.getByTestId('cursor-state')).toHaveText('Out of date');
    await expect(page.getByTestId('cursor-card')).toHaveAttribute('data-stale', 'true');
    await expect(page.getByTestId('cursor-card').locator('.provider-window-line')).toContainText('Fresh usage required');
    await page.getByRole('button', { name: /Show Cursor usage details/ }).click();
    await expect(page.getByTestId('cursor-meter-grokBot')).toContainText('Last known Grok Bot');
  });

  test('shows partial failures escaped and reports missing meters as not reported', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCursor(report => {
      report.meters = report.meters.filter(meter => meter.key !== 'grokBot');
      report.not_reported = ['Grok Bot'];
      report.errors = ['<img src=x onerror=alert(1)> Grok Bot unavailable'];
    }) });
    await expect(page.getByTestId('cursor-state')).toHaveText('Partial');
    await expect(page.getByTestId('cursor-card').locator('.provider-window-line')).toContainText('Grok Bot not reported');
    await page.getByRole('button', { name: /Show Cursor usage details/ }).click();
    await expect(page.getByTestId('cursor-card').locator('.source-error')).toContainText('<img src=x onerror=alert(1)>');
    await expect(page.getByTestId('cursor-card').locator('img[src=x]')).toHaveCount(0);
    await expect(page.getByTestId('cursor-not-reported')).toHaveText('Not reported: Grok Bot');
  });

  test('explains when nothing has been read yet', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCursor(report => {
      delete report.fetched_at; delete report.expires_at; delete report.source; delete report.plan;
      report.meters = []; report.balances = []; report.refresh_error = 'OpenUsage is not running';
    }) });
    await expect(page.getByTestId('cursor-state')).toHaveText('No data');
    await page.getByRole('button', { name: /Show Cursor usage details/ }).click();
    await expect(page.getByTestId('cursor-card')).toContainText('Waiting for the first Cursor usage read.');
    await expect(page.getByTestId('cursor-card')).toContainText('OpenUsage is not running');
  });

  test('is absent when Cursor tracking is off and survives live updates', async ({ page }) => {
    const state = await loadDashboard(page);
    await expect(page.getByTestId('cursor-card')).toHaveCount(0);
    await page.evaluate(next => window.__redlineEventSources[0].emit('dashboard', next), withCursor());
    await expect(page.getByTestId('cursor-card')).toBeVisible();
    await page.getByRole('button', { name: /Show Cursor usage details/ }).click();
    await page.evaluate(next => window.__redlineEventSources[0].emit('dashboard', next), withCursor());
    await expect(page.getByTestId('cursor-card')).toHaveClass(/open/);
    await page.evaluate(next => window.__redlineEventSources[0].emit('dashboard', next), state.dashboard);
    await expect(page.getByTestId('cursor-card')).toHaveCount(0);
  });
});
