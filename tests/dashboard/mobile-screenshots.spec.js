// Pixel 9-sized proof captures. Runs only when REDLINE_MOBILE_PROOF_DIR is set.
const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');
const { loadMobileDashboard } = require('./harness');

const proofDir = process.env.REDLINE_MOBILE_PROOF_DIR;
test.skip(!proofDir, 'REDLINE_MOBILE_PROOF_DIR is not set');

async function shoot(page, name) {
  fs.mkdirSync(proofDir, { recursive: true });
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'dark' });
  await page.screenshot({ path: path.join(proofDir, `${name}.png`) });
}

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
