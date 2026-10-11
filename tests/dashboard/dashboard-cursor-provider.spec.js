const { test, expect } = require('@playwright/test');
const { loadDashboard, loadMobileDashboard, dashboardFixture, cursorProviderFixture, cursorUsageFixture } = require('./harness');

// A provider: cursor account is a real queue: scheduled from its monthly
// subscription pools and run through the Cursor CLI. Synthetic fixtures.

function withCursorProvider(mutate) {
  const dashboard = dashboardFixture();
  const cursor = cursorProviderFixture();
  if (mutate) mutate(cursor);
  dashboard.providers.push(cursor);
  return dashboard;
}

test.describe('Cursor as a scheduled provider (desktop)', () => {
  test.skip(({ isMobile }) => isMobile, 'desktop dashboard');
  test.use({ timezoneId: 'America/Los_Angeles', locale: 'en-US' });

  test('shows the monthly subscription, never a week, and both pools', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCursorProvider() });
    const card = page.locator('[data-provider-id="cursor-main"]');
    await expect(card.locator('.provider-copy-line')).toContainText('Cursor');
    await expect(card.locator('.provider-copy-line')).toContainText('95% monthly');
    await expect(card.locator('.provider-window-line')).toContainText('Month resets in 12 days');
    await expect(card.locator('.provider-window-line')).not.toContainText('Week');
    await page.getByRole('button', { name: 'Show Cursor usage details' }).click();
    const detail = card.locator('.provider-detail');
    await expect(detail).toContainText('monthly subscription');
    await expect(detail).toContainText('Cursor Models');
    await expect(detail).toContainText('Other Models');
    await expect(detail).not.toContainText('weekly');
  });

  test('says Cursor CLI is signed out and names agent login', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCursorProvider(cursor => {
      cursor.snapshot_stale = true;
      cursor.usage_source = { active: 'native', reason: 'signed_out', last_error: 'no Cursor session' };
    }) });
    const card = page.locator('[data-provider-id="cursor-main"]');
    await expect(card.locator('.provider-window-line')).toContainText('Signed out · run agent login');
  });

  test('says the Cursor CLI and app are on different accounts and that scheduling waits', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCursorProvider(cursor => {
      cursor.snapshot_stale = true;
      cursor.usage_source = { active: 'openusage', reason: 'account_mismatch',
        last_error: 'the Cursor CLI and the Cursor app are signed in to different accounts; sign both in to the account whose usage Redline should schedule against (`agent login`)' };
    }) });
    const card = page.locator('[data-provider-id="cursor-main"]');
    await expect(card.locator('.provider-window-line')).toContainText('CLI and app accounts differ');
    await expect(card.locator('.provider-pressure')).toContainText('Accounts differ');
  });

  test('the usage card stops claiming Cursor is unscheduled once it is an account', async ({ page }) => {
    const dashboard = withCursorProvider();
    dashboard.cursor_usage = cursorUsageFixture();
    await loadDashboard(page, { dashboard });
    const card = page.getByTestId('cursor-card');
    await expect(card.locator('.usage-only-badge')).toHaveText('Usage detail');
    await card.locator('.provider-trigger').click();
    await expect(page.getByTestId('cursor-usage-only')).toContainText('gate jobs on the Cursor account');
    await expect(page.getByTestId('cursor-usage-only')).not.toContainText('does not schedule');
  });

  test('offers the Cursor CLI and its discovered models for a Cursor profile', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCursorProvider() });
    await page.getByRole('button', { name: 'Profiles' }).click();
    await page.locator('#profile-provider').selectOption('cursor-main');
    await page.locator('#profile-harness').selectOption('cursor-agent');
    const models = await page.locator('#profile-model-choice option').evaluateAll(options => options.map(option => option.value));
    expect(models).toEqual(expect.arrayContaining(['auto', 'composer-2.5', 'claude-opus-5-5-high']));
  });
});

test.describe('Cursor as a scheduled provider (mobile)', () => {
  test.skip(({ isMobile }) => !isMobile, 'mobile page');

  test('labels the ring Monthly and shows the tightest pool instead of a session', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCursorProvider() });
    const card = page.locator('[data-provider-id="cursor-main"]');
    await expect(card.locator('.m-provider-name')).toHaveText('Cursor');
    await expect(card.locator('.m-rings')).toContainText('Monthly');
    await expect(card.locator('.m-rings')).toContainText('Other');
    await expect(card.locator('.m-rings')).toContainText('lowest pool');
    await expect(card.locator('.m-rings')).not.toContainText('Session');
    await expect(card.locator('.m-rings')).not.toContainText('Weekly');
  });

  test('says the Cursor CLI and app are on different accounts', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCursorProvider(cursor => {
      cursor.snapshot_stale = true;
      cursor.usage_source = { active: 'openusage', reason: 'account_mismatch', last_error: 'different accounts' };
    }) });
    await expect(page.getByTestId('account-mismatch')).toContainText('different accounts');
  });

  test('the usage card says the monthly pools schedule Cursor jobs', async ({ page }) => {
    const dashboard = withCursorProvider();
    dashboard.cursor_usage = cursorUsageFixture();
    await loadMobileDashboard(page, { dashboard });
    await expect(page.getByTestId('cursor-meta')).toContainText('monthly pools schedule Cursor jobs');
    await expect(page.getByTestId('cursor-meta')).not.toContainText('not used for scheduling');
  });
});
