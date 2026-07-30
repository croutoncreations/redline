// Captures dashboard screenshots for visual review. Runs only when
// REDLINE_PROOF_DIR is set, e.g.:
//   REDLINE_PROOF_DIR=.artifacts/shots npx playwright test screenshots
const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');
const { loadDashboard, dashboardFixture } = require('./harness');

const proofDir = process.env.REDLINE_PROOF_DIR;
test.skip(!proofDir, 'REDLINE_PROOF_DIR is not set');

async function shoot(page, name, options = {}) {
  fs.mkdirSync(proofDir, { recursive: true });
  await page.screenshot({ path: path.join(proofDir, `${name}.png`), fullPage: true, ...options });
}

function scaleFixture() {
  const base = dashboardFixture();
  const tiers = ['behind', 'well_behind', 'expiring'];
  const states = ['queued', 'queued', 'queued', 'running', 'disabled', 'failed'];
  base.tasks = Array.from({ length: 24 }, (_, i) => ({
    id: `job-${String(i + 1).padStart(2, '0')}`,
    name: [
      'Audit authentication', 'Refresh dependency lockfiles', 'Find one reproducible bug',
      'Summarize open pull requests', 'Tighten flaky integration tests', 'Profile slow API endpoints',
      'Review stale feature flags', 'Update onboarding documentation',
    ][i % 8] + (i >= 8 ? ` #${Math.floor(i / 8) + 1}` : ''),
    priority: 90 - (i * 3) % 70,
    type: i % 3 === 0 ? 'one_off' : 'recurring',
    state: states[i % states.length],
    enabled: states[i % states.length] !== 'disabled',
    execution_profile_id: i % 2 ? 'codex-devx' : 'claude-devx',
    provider_account_id: i % 2 ? 'codex-main' : 'claude-main',
    harness_type: i % 2 ? 'codex-cli' : 'claude-code',
    model: i % 2 ? 'gpt-5.5' : 'claude-opus-4-8',
    workspace_provider: 'devx',
    min_interval: (i % 4) * 86400000000000,
    require_repo_change: i % 2 === 0,
    dispatch_tier: tiers[i % tiers.length],
  }));
  const runStates = ['completed', 'completed', 'failed', 'running', 'completed'];
  base.runs = Array.from({ length: 30 }, (_, i) => ({
    id: `run-${i + 1}`,
    task_id: base.tasks[i % base.tasks.length].id,
    provider_account_id: i % 2 ? 'codex-main' : 'claude-main',
    state: runStates[i % runStates.length],
    error: runStates[i % runStates.length] === 'failed' ? 'harness exited with code 1' : undefined,
    started_at: new Date(Date.now() - (i + 1) * 47 * 60 * 1000).toISOString(),
    completed_at: new Date(Date.now() - i * 45 * 60 * 1000).toISOString(),
  }));
  base.attempts = Array.from({ length: 40 }, (_, i) => ({
    id: i + 1,
    provider_account_id: i % 2 ? 'codex-main' : 'claude-main',
    outcome: i % 9 === 4 ? 'error' : 'wait',
    reason: i % 9 === 4 ? undefined : 'no actionable weekly overflow',
    error: i % 9 === 4 ? 'temporary usage source timeout' : undefined,
    completed_at: new Date(Date.now() - (i + 1) * 5 * 60 * 1000).toISOString(),
  }));
  base.health.active_runs = 2;
  return base;
}

test('captures default, dialog, and detail views', async ({ page }) => {
  await loadDashboard(page);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await shoot(page, 'dashboard-default');
  await page.getByRole('button', { name: 'Show Claude usage details' }).click();
  await expect(page.locator('[data-provider-id="claude-main"]')).toContainText('Empirical weekly capacity');
  await shoot(page, 'dashboard-provider-detail');
  await page.keyboard.press('Escape');
  await page.mouse.move(640, 600);
  await expect(page.locator('[data-provider-id="claude-main"] .provider-detail')).toBeHidden();
  await page.getByRole('button', { name: 'New job' }).click();
  await expect(page.getByRole('dialog', { name: 'New scheduled job' })).toBeVisible();
  await shoot(page, 'dialog-new-job');
  await page.getByRole('button', { name: 'Cancel' }).click();
  await page.getByRole('button', { name: 'Profiles' }).click();
  await expect(page.getByRole('dialog', { name: 'Harness & workspace setup' })).toBeVisible();
  await expect(page.locator('#profile-discovery-status')).toContainText('found');
  await shoot(page, 'dialog-profiles');
  await page.getByRole('button', { name: 'Close', exact: true }).click();
  await page.getByRole('button', { name: /View logs/ }).first().click();
  await expect(page.locator('#log-content')).toContainText('RESULT');
  await shoot(page, 'dialog-logs');
});

test('captures scale and failure views', async ({ page }) => {
  await loadDashboard(page, { dashboard: scaleFixture(), waitForReady: false });
  await expect(page.locator('#tasks-body').getByText('Audit authentication', { exact: true })).toBeVisible();
  await shoot(page, 'dashboard-scale');
});

test('captures empty state', async ({ page }) => {
  const empty = dashboardFixture();
  empty.tasks = []; empty.runs = []; empty.attempts = [];
  empty.health = { ...empty.health, status: 'ok', dispatch_errors: 0 };
  await loadDashboard(page, { dashboard: empty, waitForReady: false });
  await expect(page.getByText('No jobs are queued yet', { exact: false })).toBeVisible();
  await shoot(page, 'dashboard-empty');
});

test('captures narrow viewport', async ({ page }) => {
  await page.setViewportSize({ width: 680, height: 900 });
  await loadDashboard(page);
  await shoot(page, 'dashboard-narrow');
});
