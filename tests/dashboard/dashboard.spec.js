const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');
const { loadDashboard, dashboardFixture, profileFixture } = require('./harness');


test('renders operational state and applies live dashboard events', async ({ page }) => {
  const state = await loadDashboard(page);
  await expect(page.getByRole('button', { name: 'Recent errors' })).toBeVisible();
  await expect(page.locator('#health-explainer')).toContainText('temporary usage source timeout');
  await expect(page.getByRole('button', { name: 'Show Claude usage details' })).toContainText('53%');
  await expect(page.getByRole('button', { name: 'Show Claude usage details' })).toContainText('5h 62%');
  await expect(page.getByRole('button', { name: 'Show Claude usage details' })).toContainText('Likely eligible in 2 hrs');
  await expect(page.getByRole('button', { name: 'Show Codex usage details' })).toContainText('Likely eligible in 1 day');
  await page.getByRole('button', { name: 'Show Claude usage details' }).click();
  await expect(page.locator('[data-provider-id="claude-main"] .provider-detail')).toContainText('Native');
  await expect(page.locator('[data-provider-id="claude-main"] .provider-detail')).toContainText('OpenUsage unavailable');
  await expect(page.locator('[data-provider-id="claude-main"] .provider-detail')).toContainText('Fable · reset inferred');
  await page.evaluate(next => window.__redlineEventSources[0].emit('dashboard', next), { ...state.dashboard, tasks: [], runs: [], attempts: [], health: { ...state.dashboard.health, status: 'ok', dispatch_errors: 0 } });
  await expect(page.getByText('No jobs are queued yet.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'healthy' })).toBeVisible();
  await page.evaluate(() => window.__redlineEventSources[0].fail());
  await expect(page.getByText('reconnecting')).toBeVisible();
});

test('links the dashboard to product updates and the Crouton Creations tool family', async ({ page }) => {
  await loadDashboard(page);
  await expect(page.getByRole('link', { name: 'More tools from Crouton Creations' }))
    .toHaveAttribute('href', /utm_source=redline/);
  await expect(page.getByRole('link', { name: 'Get builder updates' }))
    .toHaveAttribute('href', /buttondown\.com\/croutoncreations\?utm_source=redline/);
});

test('describes high scheduling pressure without implying the subscription expires', async ({ page }) => {
  const dashboard = dashboardFixture();
  dashboard.providers[0].latest_decision = {
    decision: 'RUN',
    mode: 'window_slots',
    reason: 'weekly remaining is well behind pace',
    unlocked_tier: 'expiring',
  };
  await loadDashboard(page, { dashboard });
  const claude = page.getByRole('button', { name: 'Show Claude usage details' });
  await expect(claude).toContainText('Run now · high surplus');
  await expect(claude).not.toContainText('Redline · Expiring');
  await claude.click();
  await expect(page.locator('[data-provider-id="claude-main"] .provider-detail'))
    .toContainText('All job tiers are eligible because weekly allowance is at risk of expiring unused.');
});

test('does not present a stale usage percentage as current', async ({ page }) => {
  const dashboard = dashboardFixture();
  dashboard.providers[0].snapshot_stale = true;
  dashboard.providers[0].error = 'Usage data is stale; scheduling is paused until a fresh snapshot is available.';
  await loadDashboard(page, { dashboard });
  const claude = page.getByRole('button', { name: 'Show Claude usage details' });
  await expect(claude).toContainText('Usage unavailable');
  await expect(claude).toContainText('Last sample');
  await expect(claude).not.toContainText('53% weekly');
  await claude.click();
  await expect(page.locator('[data-provider-id="claude-main"] .provider-detail'))
    .toContainText('Last known usage');
  await expect(page.locator('[data-provider-id="claude-main"] .provider-detail'))
    .toContainText('scheduling is paused');
});

test('loads capacity attribution and model evidence on demand', async ({ page }) => {
	const state = await loadDashboard(page);
	const claude = page.getByRole('button', { name: 'Show Claude usage details' });
	const claudeCard = page.locator('[data-provider-id="claude-main"]');
	await claude.hover();
	await expect(claudeCard).toContainText('Empirical weekly capacity');
	await expect(claudeCard).toContainText('100M processed tokens');
	await expect(claudeCard).toContainText('50% attributed');
	await expect(claudeCard).toContainText('$120–$180 API-dollar equivalent');
	await expect(claudeCard).toContainText('gatepost-pi');
	await expect(claudeCard).toContainText('claude-opus-4-8');
	await claude.click();
	await page.evaluate(next => window.__redlineEventSources[0].emit('dashboard', next), state.dashboard);
	await expect(page.locator('[data-provider-id="claude-main"]')).toContainText('Empirical weekly capacity');
});

test('changes a provider policy and shows per-provider summary', async ({ page }) => {
  const state = await loadDashboard(page);
  const codex = page.getByRole('button', { name: 'Show Codex usage details' });
  await codex.click();
  const policy = page.getByLabel('Codex dispatch policy');
  await expect(policy).toHaveValue('');
  await expect(policy.locator('option[value=""]')).toContainText('Default · Standard');
  await expect(page.locator('[data-provider-id="codex-main"] .concurrency-status')).toContainText('1/2 active');
  await expect(page.locator('[data-provider-id="codex-main"] .concurrency-status')).toContainText('Weekly');
  await expect(page.locator('[data-provider-id="codex-main"] .concurrency-status')).toContainText('1/2');
  await policy.selectOption('late');
  await expect.poll(() => state.requests.some(item =>
    item.method === 'PATCH' && item.path === '/v1/providers/codex-main/policy' && item.body.policy === 'late'
  )).toBe(true);
  await expect(page.locator('#policy')).toHaveText('per provider');
  await expect(page.getByLabel('Codex dispatch policy')).toHaveValue('late');
  if (process.env.REDLINE_PROOF_DIR) {
    fs.mkdirSync(process.env.REDLINE_PROOF_DIR, { recursive: true });
    await page.screenshot({ path: path.join(process.env.REDLINE_PROOF_DIR, 'provider-policy-control.png'), fullPage: true });
  }
});

test('discovers harness versions and filters Pi models by provider', async ({ page }) => {
  await loadDashboard(page);
  await page.getByRole('button', { name: 'Profiles' }).click();
  const dialog = page.getByRole('dialog', { name: 'Harness & workspace setup' });
  await expect(dialog).toBeVisible();
  const harness = page.locator('#profile-harness'), provider = page.locator('#profile-provider'), model = page.locator('#profile-model-choice');
  await expect(harness.locator('option')).toContainText(['Codex CLI · v0.144.6', 'Claude Code · v2.1.211', 'Pi · v0.80.10']);
  await provider.selectOption('codex-main'); await harness.selectOption('pi');
  await expect(model.locator('option[value="openai-codex/gpt-5.6-sol"]')).toHaveCount(1);
  await provider.selectOption('claude-main');
  await expect(harness).toHaveValue('pi');
  await expect(model.locator('option[value="anthropic-cli/claude-opus-4-8"]')).toHaveCount(1);
  if (process.env.REDLINE_PROOF_DIR) {
    fs.mkdirSync(process.env.REDLINE_PROOF_DIR, { recursive: true });
    await page.screenshot({ path: path.join(process.env.REDLINE_PROOF_DIR, 'profile-discovery.png'), fullPage: true });
  }

  await page.locator('[data-profile="pi-custom"]').click();
  await expect(model).toHaveValue('anthropic-cli/private-preview');
  await expect(model.locator('option:checked')).toContainText('previously used');
  await model.selectOption('__other__');
  await expect(page.locator('#profile-model-custom')).toBeVisible();
  await harness.selectOption('command');
  await expect(page.locator('#profile-command-field')).toBeVisible();
  await expect(page.locator('#profile-model-field')).toBeHidden();
});

test('creates a scheduled job with tier and recurrence settings', async ({ page }) => {
  const state = await loadDashboard(page);
  await page.getByRole('button', { name: '+ New job' }).click();
  const dialog = page.getByRole('dialog', { name: 'New scheduled job' });
  await expect(dialog).toBeVisible();
  await page.locator('#task-name').fill('Review cache invalidation');
  await page.locator('#task-profile').selectOption('codex-devx');
  await page.locator('#task-priority').fill('82');
  await page.locator('#task-tier').selectOption('expiring');
  await expect(page.locator('#task-interval')).toBeDisabled();
  await expect(page.locator('#task-interval-note')).toBeVisible();
  await page.locator('#task-type').selectOption('recurring');
  await expect(page.locator('#task-interval')).toBeEnabled();
  await page.locator('#task-interval').fill('7d');
  await page.locator('#task-prompt').fill('Inspect one cache invalidation path and report findings.');
  await page.locator('#task-repo-change').check();
  await page.getByRole('button', { name: 'Save job' }).click();
  await expect(dialog).toBeHidden();
  await expect.poll(() => state.requests.filter(item => item.path === '/v1/tasks').length).toBe(1);
  expect(state.requests.find(item => item.path === '/v1/tasks').body).toMatchObject({ priority: 82, dispatch_tier: 'expiring', type: 'recurring', min_interval: '7d', require_repo_change: true });
});

test('starts a job from an editable prompt template', async ({ page }) => {
  const state = await loadDashboard(page);
  await page.getByRole('button', { name: '+ New job' }).click();
  await page.locator('#task-template').selectOption('bug-hunt');
  await expect(page.locator('#task-name')).toHaveValue('Find and fix one reproducible bug');
  await expect(page.locator('#task-prompt')).toHaveValue(/Find ONE real/);
  await expect(page.locator('#task-template-description')).toContainText('Requires: Repository access');
  await page.locator('#task-name').fill('Find one bug in parsing');
  await page.locator('#task-prompt').fill('Inspect parsing only. Reproduce one bug before fixing it.');
  await page.locator('#task-profile').selectOption('codex-devx');
  await page.getByRole('button', { name: 'Save job' }).click();
  await expect.poll(() => state.requests.some(item =>
    item.path === '/v1/tasks' && item.body.name === 'Find one bug in parsing' &&
    item.body.prompt === 'Inspect parsing only. Reproduce one bug before fixing it.'
  )).toBe(true);
});

test('creates a Pi profile and surfaces blocked deletion errors', async ({ page }) => {
  const state = await loadDashboard(page);
  await page.getByRole('button', { name: 'Profiles' }).click();
  await expect(page.getByRole('dialog', { name: 'Harness & workspace setup' })).toBeVisible();
  await page.locator('#profile-id').fill('pi-codex');
  await page.locator('#profile-provider').selectOption('codex-main');
  await page.locator('#profile-harness').selectOption('pi');
  await page.locator('#profile-model-choice').selectOption('openai-codex/gpt-5.6-sol');
  await page.locator('#profile-repository').fill('/repo/redline');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect.poll(() => state.requests.filter(item => item.path === '/v1/profiles').length).toBe(1);
  expect(state.requests.find(item => item.path === '/v1/profiles').body).toMatchObject({ id: 'pi-codex', provider_account_id: 'codex-main', harness_type: 'pi', model: 'openai-codex/gpt-5.6-sol' });
	await page.locator('#profile-base-branch').fill('main');
	await page.getByRole('button', { name: 'Save profile' }).click();
	await expect.poll(() => state.requests.some(item => item.method === 'PATCH' && item.path === '/v1/profiles/pi-codex')).toBe(true);
  // saveProfile() re-enables #save-profile only after its full chain (loadProfiles -> editProfile ->
  // refresh) resolves; editProfile() clears #profile-form-error as part of that chain, so waiting for
  // the PATCH request alone races the delete click below against that clear.
  await expect(page.getByRole('button', { name: 'Save profile' })).toBeEnabled();

  state.blockProfileDelete = true;
  page.once('dialog', confirmation => confirmation.accept());
  await page.getByRole('button', { name: 'Delete' }).click();
  await expect(page.locator('#profile-form-error')).toContainText('profile is assigned to a task');
});

test('imports Hermes Desktop and creates a remote profile from discovered context', async ({ page }) => {
  const state = await loadDashboard(page);
  await page.getByRole('button', { name: 'Profiles' }).click();
  await page.locator('#profile-id').fill('hermes-sample-app');
  await page.locator('#profile-provider').selectOption('codex-main');
  await page.locator('#profile-harness').selectOption('hermes');
  await expect(page.locator('#profile-hermes-fields')).toBeVisible();
  await page.getByRole('button', { name: 'Import Desktop' }).click();
  await expect(page.locator('#profile-hermes-status')).toContainText('1 profile discovered');
  await expect(page.locator('#profile-runtime-profile')).toHaveValue('default');
  await page.locator('#profile-runtime-project').selectOption('sample-app');
  await expect(page.locator('#profile-model-choice')).toHaveValue('default');
  await page.locator('#profile-model-choice').selectOption('openai-codex/gpt-5.6-sol');
  await page.getByRole('button', { name: 'Save profile' }).click();

  await expect.poll(() => state.requests.some(item => item.path === '/v1/agent-contexts')).toBe(true);
  await expect.poll(() => state.requests.some(item => item.path === '/v1/profiles')).toBe(true);
  expect(state.requests.find(item => item.path === '/v1/agent-contexts').body).toMatchObject({
    id: 'hermes-sample-app-context', runtime_connection_id: 'hermes-desktop',
    profile: 'default', project: 'sample-app', working_directory: '/home/tester/projects/sample-app',
    session_mode: 'isolated',
  });
  expect(state.requests.find(item => item.path === '/v1/profiles').body).toMatchObject({
    id: 'hermes-sample-app', provider_account_id: 'codex-main',
    agent_context_id: 'hermes-sample-app-context', harness_type: 'hermes',
    model: 'openai-codex/gpt-5.6-sol', workspace_provider: 'runtime-owned',
    repository: '/home/tester/projects/sample-app',
  });
});

test('selects a discovered existing Hermes job for a scheduled task', async ({ page }) => {
  const state = await loadDashboard(page, {
    profiles: [...profileFixture(), {
      id: 'hermes-content', provider_account_id: 'claude-main',
      agent_context_id: 'hermes-content-context', harness_type: 'hermes',
      model: 'custom:cliproxyapi-plus/claude-fable-5-medium',
      budget_model_group: 'fable', workspace_provider: 'runtime-owned',
      repository: '/home/tester/worktrees/content-site',
    }],
    runtimeConnections: [{
      id: 'hermes-desktop', runtime: 'hermes', transport: 'gateway',
      url: 'http://hermes.test:9119', max_concurrent_runs: 1,
    }],
    agentContexts: [{
      id: 'hermes-content-context', runtime_connection_id: 'hermes-desktop',
      profile: 'default', project: 'content-site', working_directory: '/home/tester/worktrees/content-site',
      session_mode: 'isolated', max_concurrent_runs: 1,
    }],
    runtimeJobs: {
      'hermes-desktop': [{
        id: 'job-seo-planner', name: 'Weekly SEO content planner',
        provider: 'custom:cliproxyapi-plus', model: 'claude-fable-5-medium', enabled: true,
      }],
    },
  });
  await page.getByRole('button', { name: 'New job' }).click();
  await page.locator('#task-name').fill('Content planner');
  await page.locator('#task-profile').selectOption('hermes-content');
  await expect(page.locator('#task-runtime-job-field')).toBeVisible();
  await expect(page.locator('#task-runtime-job')).toContainText('Weekly SEO content planner');
  await page.locator('#task-runtime-job').selectOption('job-seo-planner');
  await page.getByRole('button', { name: 'Save job' }).click();

  await expect.poll(() => state.requests.some(item => item.method === 'POST' && item.path === '/v1/tasks')).toBe(true);
  expect(state.requests.find(item => item.method === 'POST' && item.path === '/v1/tasks').body).toMatchObject({
    execution_profile_id: 'hermes-content', runtime_job_id: 'job-seo-planner',
  });
});

test('creates edits and deletes a standalone Hermes runtime connection', async ({ page }) => {
  const state = await loadDashboard(page);
  await page.getByRole('button', { name: 'Profiles' }).click();
  await page.locator('#profile-harness').selectOption('hermes');
  await page.getByRole('button', { name: '+ Connection' }).click();
  await expect(page.getByRole('dialog', { name: 'New Hermes connection' })).toBeVisible();
  await page.locator('#runtime-id').fill('hermes-remote');
  await page.locator('#runtime-url').fill('https://hermes.example');
  await page.locator('#runtime-credential-source').selectOption('environment');
  await page.locator('#runtime-credential-ref').fill('HERMES_GATEWAY_CREDENTIAL');
  await page.getByRole('button', { name: 'Save connection' }).click();

  await expect.poll(() => state.requests.some(item => item.method === 'POST' && item.path === '/v1/runtime-connections')).toBe(true);
  expect(state.requests.find(item => item.method === 'POST' && item.path === '/v1/runtime-connections').body).toMatchObject({
    id:'hermes-remote',runtime:'hermes',transport:'gateway',url:'https://hermes.example',
    credential_source:'environment',credential_ref:'HERMES_GATEWAY_CREDENTIAL',
  });
  await expect(page.locator('#profile-runtime-connection')).toHaveValue('hermes-remote');

  await page.getByRole('button', { name: 'Edit', exact: true }).click();
  await page.locator('#runtime-concurrency').fill('3');
  await page.getByRole('button', { name: 'Save connection' }).click();
  await expect.poll(() => state.requests.some(item => item.method === 'PATCH' && item.path === '/v1/runtime-connections/hermes-remote')).toBe(true);

  await page.getByRole('button', { name: 'Edit', exact: true }).click();
  page.once('dialog', confirmation => confirmation.accept());
  await page.getByRole('button', { name: 'Delete connection' }).click();
  await expect.poll(() => state.requests.some(item => item.method === 'DELETE' && item.path === '/v1/runtime-connections/hermes-remote')).toBe(true);
});

test('loads both run log streams and controls an existing task', async ({ page }) => {
  const state = await loadDashboard(page);
  await page.getByRole('button', { name: 'View details →' }).click();
  await expect(page.locator('#log-content')).toContainText('RESULT');
  await expect(page.locator('#log-content')).toContainText('ok');
  await expect(page.locator('#log-content')).toContainText('9,346 input · 7,040 cached · 28 output');
  await page.getByRole('button', { name: 'Raw' }).click();
  await expect(page.locator('#log-content')).toContainText('"result":"ok"');
  await page.getByRole('button', { name: 'Formatted' }).click();
  await page.getByRole('button', { name: 'stderr' }).click();
  await expect(page.locator('#log-content')).toContainText('warning from stderr');
  await page.getByRole('button', { name: 'Close' }).click();

  await page.getByRole('row').filter({ hasText: 'Audit authentication' }).click();
  await expect(page.getByRole('dialog', { name: 'Manage scheduled job' })).toBeVisible();
	await page.locator('#task-priority').fill('75');
	await page.getByRole('button', { name: 'Save job' }).click();
	await expect.poll(() => state.requests.some(item => item.method === 'PATCH' && item.path === '/v1/tasks/audit-auth')).toBe(true);
	await page.getByRole('button', { name: 'Manage' }).click();
	page.once('dialog', confirmation => confirmation.accept());
	await page.getByRole('button', { name: 'Delete' }).click();
	await expect(page.locator('#task-form-error')).toContainText('task has run history');
  await page.getByRole('button', { name: 'Disable' }).click();
  await expect.poll(() => state.requests.some(item => item.path === '/v1/tasks/audit-auth/disable')).toBe(true);
});

test('summarizes queue states, previews long run history, and keeps log context', async ({ page }) => {
  const dashboard = dashboardFixture();
  dashboard.tasks = [
    dashboard.tasks[0],
    { ...dashboard.tasks[0], id: 'flaky-tests', name: 'Fix flaky tests', state: 'failed' },
  ];
  dashboard.runs = Array.from({ length: 12 }, (_, index) => ({
    id: `run-${index + 1}`, task_id: index % 2 ? 'flaky-tests' : 'audit-auth',
    provider_account_id: 'codex-main', state: 'completed', started_at: dashboard.generated_at,
  }));
  await loadDashboard(page, { dashboard, waitForReady: false });
  await expect(page.locator('#task-count')).toHaveText('2 jobs · 1 failed');
  await expect(page.locator('#runs-list .activity')).toHaveCount(8);
  await page.getByRole('button', { name: 'Show all 12 runs' }).click();
  await expect(page.locator('#runs-list .activity')).toHaveCount(12);
  await page.getByRole('button', { name: 'Show latest 8' }).click();
  await expect(page.locator('#runs-list .activity')).toHaveCount(8);
  await page.locator('[data-run="run-1"]').click();
  await expect(page.locator('#log-title')).toHaveText('Audit authentication');
  await expect(page.locator('#log-context')).toContainText('run-1 · Completed · codex-main');
});

test('shows durable activity results, artifacts, and marks opened work read', async ({ page }) => {
  const state = await loadDashboard(page);
  await expect(page.getByRole('heading', { name: 'Activity' })).toBeVisible();
  await expect(page.locator('#run-count')).toHaveText('1 new · 1 total');
  await expect(page.locator('#runs-list')).toContainText('Opened a focused authentication fix.');
  await expect(page.locator('#runs-list')).toContainText('openai-codex · gpt-5.6-sol');
  await page.locator('[data-run="run-1"]').click();
  await expect(page.locator('#run-result')).toContainText('Opened a focused authentication fix.');
  await expect(page.locator('#run-result').getByRole('link', { name: /PR #42/ })).toHaveAttribute('href', 'https://github.com/acme/redline/pull/42');
  await expect.poll(() => state.requests.some(item => item.path === '/v1/runs/run-1/read')).toBe(true);
});

test('shows dashboard API errors and preserves the responsive queue layout', async ({ page }) => {
  await page.setViewportSize({ width: 680, height: 800 });
  const state = await loadDashboard(page);
  await expect(page.getByRole('columnheader', { name: 'Route' })).toBeHidden();
  state.dashboardError = true;
  await page.getByRole('button', { name: 'Refresh dashboard' }).click();
  await expect(page.locator('#error-banner')).toContainText('dashboard unavailable');
});

test('shows loading state and task save errors without closing the form', async ({ page }) => {
	const state = await loadDashboard(page, { pauseDashboard: true, waitForReady: false, taskCreateError: 'minimum interval is invalid' });
	await expect(page.getByText('Loading queue…')).toBeVisible();
	state.releaseDashboard();
	await expect(page.locator('#tasks-body').getByText('Audit authentication')).toBeVisible();
	await page.getByRole('button', { name: '+ New job' }).click();
	await page.locator('#task-name').fill('Invalid scheduled job');
	await page.locator('#task-profile').selectOption('codex-devx');
	await page.locator('#task-prompt').fill('Do a small thing.');
	await page.getByRole('button', { name: 'Save job' }).click();
	await expect(page.locator('#task-form-error')).toContainText('minimum interval is invalid');
	await expect(page.getByRole('dialog', { name: 'New scheduled job' })).toBeVisible();
});
