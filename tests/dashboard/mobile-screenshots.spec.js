// Pixel 9-sized proof captures. Runs only when REDLINE_MOBILE_PROOF_DIR is set.
const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');
const { loadMobileDashboard, dashboardFixture, cursorUsageFixture } = require('./harness');

const proofDir = process.env.REDLINE_MOBILE_PROOF_DIR;
test.skip(!proofDir, 'REDLINE_MOBILE_PROOF_DIR is not set');
test.use({ timezoneId: 'America/Los_Angeles', locale: 'en-US' });

async function shoot(page, name) {
  fs.mkdirSync(proofDir, { recursive: true });
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'dark' });
  await page.screenshot({ path: path.join(proofDir, `${name}.png`) });
}

// Synthetic fixture data only, not live quota.
test('captures Pixel 9 Cursor usage card states', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile', 'mobile project only');
  const dashboard = dashboardFixture();
  dashboard.cursor_usage = cursorUsageFixture();
  dashboard.cursor_usage.meters.push({ key: 'onDemand', label: 'On-demand', group: 'on_demand', unit: 'usd', used: 12.4 });
  await loadMobileDashboard(page, { dashboard });
  const card = page.getByTestId('cursor-card');
  await card.scrollIntoViewIfNeeded();
  await expect(card).toBeVisible();
  await shoot(page, 'mobile-cursor-usage');
  fs.mkdirSync(proofDir, { recursive: true });
  await card.screenshot({ path: path.join(proofDir, 'mobile-cursor-card.png') });

  // Match the live UI's existing refresh grace: four minutes remains fresh;
  // six minutes is beyond the expiry plus grace, as covered by mobile.spec.js.
  await page.clock.fastForward('06:00');
  await expect(page.getByTestId('cursor-state')).toHaveText('Out of date');
  await card.screenshot({ path: path.join(proofDir, 'mobile-cursor-card-out-of-date.png') });
});

test('captures Pixel 9 Cursor partial failure', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile', 'mobile project only');
  const dashboard = dashboardFixture();
  dashboard.cursor_usage = cursorUsageFixture();
  dashboard.cursor_usage.meters = dashboard.cursor_usage.meters.filter(meter => meter.key === 'grokBot');
  dashboard.cursor_usage.balances = [];
  dashboard.cursor_usage.not_reported = ['Cursor Models', 'Other Models'];
  dashboard.cursor_usage.errors = ['Cursor plan usage unavailable: HTTP 503'];
  await loadMobileDashboard(page, { dashboard });
  const card = page.getByTestId('cursor-card');
  await card.scrollIntoViewIfNeeded();
  fs.mkdirSync(proofDir, { recursive: true });
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'dark' });
  await card.screenshot({ path: path.join(proofDir, 'mobile-cursor-card-partial.png') });
});

test('captures Pixel 9 usage queue and runs views', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile', 'mobile project only');
  await loadMobileDashboard(page);
  await expect(page.locator('[data-testid="provider-card"]')).toHaveCount(2);
  await expect(page.locator('[data-testid="provider-detail-claude-main"]')).toBeVisible();
  await expect(page.locator('[data-testid="provider-detail-codex-main"]')).toBeVisible();
  await shoot(page, 'mobile-usage');

  await page.locator('[data-testid="provider-detail-codex-main"]').scrollIntoViewIfNeeded();
  await shoot(page, 'mobile-usage-codex-detail');

  await page.locator('#m-tab-queue').click();
  await expect(page.locator('[data-testid="next-up"]')).toBeVisible();
  await shoot(page, 'mobile-queue');

  await page.locator('#m-tab-runs').click();
  await expect(page.locator('[data-testid="run-item-run-1"]')).toBeVisible();
  await expect(page.locator('#m-tab-runs')).toHaveClass(/active/);
  await expect(page.locator('#m-tab-queue')).not.toHaveClass(/active/);
  await shoot(page, 'mobile-runs');

  await page.locator('[data-testid="run-item-run-1"]').click();
  await expect(page.locator('#m-run-detail')).toHaveClass(/visible/);
  await shoot(page, 'mobile-run-detail');
});
