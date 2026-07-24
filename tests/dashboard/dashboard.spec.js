const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');

const dashboardRoot = path.join(__dirname, '..', '..', 'internal', 'api', 'dashboard');
const assets = {
  '/': [fs.readFileSync(path.join(dashboardRoot, 'index.html')), 'text/html'],
  '/assets/dashboard.js': [fs.readFileSync(path.join(dashboardRoot, 'dashboard.js')), 'text/javascript'],
  '/assets/dashboard.css': [fs.readFileSync(path.join(dashboardRoot, 'dashboard.css')), 'text/css'],
  '/assets/claude.svg': [fs.readFileSync(path.join(dashboardRoot, 'claude.svg')), 'image/svg+xml'],
  '/assets/codex.svg': [fs.readFileSync(path.join(dashboardRoot, 'codex.svg')), 'image/svg+xml'],
};

function dashboardFixture() {
  const observed = '2026-07-20T19:00:00Z';
  return {
    generated_at: observed,
    active_policy: 'standard',
    policies: {
      late: { trigger_margin: .04, rolling_reserve: .40, pace_thresholds: [{ time_remaining: '24h', min_weekly_remaining: .80 }] },
      standard: { trigger_margin: .02, rolling_reserve: .25, pace_thresholds: [{ time_remaining: '72h', min_weekly_remaining: .50 }] },
    },
    health: { status: 'degraded', window: '24h', active_runs: 0, dispatch_attempts: 8, dispatch_errors: 1 },
    scheduler: { enabled: true, next_cycle_at: '2026-07-20T19:05:00Z' },
    usage_monitor: { enabled: true, next_cycle_at: '2026-07-20T19:05:00Z' },
    providers: [
      { id: 'claude-main', provider: 'claude', policy: 'standard', policy_source: 'global', default_policy: 'standard', max_concurrent_runs: 1, active_runs: 0, usage_source: { active: 'native', last_error: 'OpenUsage unavailable; using native collection' }, snapshot: { provider: 'claude', observed_at: observed, short: { remaining: 0.62, resets_at: '2026-07-20T23:00:00Z' }, weekly: { remaining: 0.53, resets_at: '2026-07-24T19:00:00Z' }, allowances: [{ key: 'model:fable:weekly', source_label: 'Fable', scope: 'model', role: 'weekly', remaining: 1, resets_at: '2026-07-24T19:00:00Z', period_duration_seconds: 604800, reset_inferred: true }] } },
      { id: 'codex-main', provider: 'codex', policy: 'standard', policy_source: 'global', default_policy: 'standard', max_concurrent_runs: 2, active_runs: 1, pool_concurrency: { weekly: 2 }, active_pool_claims: { weekly: 1 }, usage_source: { active: 'openusage' }, snapshot: { provider: 'codex', observed_at: observed, weekly: { remaining: 0.47, resets_at: '2026-07-25T19:00:00Z' }, allowances: [] } },
    ],
    tasks: [{ id: 'audit-auth', name: 'Audit authentication', priority: 70, type: 'recurring', state: 'queued', enabled: true, execution_profile_id: 'codex-devx', provider_account_id: 'codex-main', harness_type: 'codex-cli', model: 'gpt-5.5', workspace_provider: 'devx', min_interval: 86400000000000, require_repo_change: true, dispatch_tier: 'well_behind' }],
    runs: [{ id: 'run-1', task_id: 'audit-auth', provider_account_id: 'codex-main', state: 'completed', started_at: observed }],
    attempts: [{ id: 1, provider_account_id: 'codex-main', outcome: 'error', error: 'temporary usage source timeout', completed_at: observed }],
  };
}

function profileFixture() {
  return [
    { id: 'codex-devx', provider_account_id: 'codex-main', harness_type: 'codex-cli', model: 'gpt-5.5', workspace_provider: 'devx', repository: '/repo/redline' },
    { id: 'pi-custom', provider_account_id: 'claude-main', harness_type: 'pi', model: 'anthropic-cli/private-preview', workspace_provider: 'devx', repository: '/repo/redline' },
  ];
}

function profileOptionsFixture() {
  return {
    generated_at: '2026-07-20T19:00:00Z',
    harnesses: [
      { id: 'codex-cli', label: 'Codex CLI', installed: true, version: '0.144.6', models: { codex: [{ id: 'gpt-5.5', label: 'GPT-5.5', source: 'codex_cache' }] } },
      { id: 'claude-code', label: 'Claude Code', installed: true, version: '2.1.211', models: { claude: [{ id: 'claude-opus-4-8', label: 'Claude Opus 4.8', source: 'pi_config', context_window: '200K', max_output: '32K' }] } },
      { id: 'pi', label: 'Pi', installed: true, version: '0.80.10', models: {
        codex: [{ id: 'openai-codex/gpt-5.6-sol', label: 'GPT-5.6 Sol', source: 'pi_config', context_window: '1M', max_output: '128K' }],
        claude: [{ id: 'anthropic-cli/claude-fable-5', label: 'Claude Fable 5', source: 'pi_config', context_window: '200K', max_output: '32K' }, { id: 'anthropic-cli/claude-opus-4-8', label: 'Claude Opus 4.8', source: 'pi_config', context_window: '200K', max_output: '32K' }],
      } },
      { id: 'hermes', label: 'Hermes', installed: true, version: '0.18.2', models: {} },
      { id: 'command', label: 'Custom command', installed: true },
    ],
  };
}

function capacityFixture(provider) {
  return {
    provider,
    confidence: 'low',
    snapshot_count: 120,
    token_observation_count: 42,
    weekly: {
      window: 'weekly', confidence: 'low', estimated_tokens: { input: 25000000, output: 2000000, cache_read: 73000000, cache_creation: 0, total: 100000000 },
      measured_tokens: { input: 250000, output: 20000, cache_read: 730000, cache_creation: 0, total: 1000000 },
      observed_usage: .01, total_observed_usage: .02, unattributed_usage: .01, attribution_coverage: .5,
      closed_spans: 1, observed_spans: 2, unattributed_spans: 1,
      sources: [{ key: 'gatepost-pi', observations: 30, tokens: { total: 800000 }, fraction_of_measured_tokens: .8 }, { key: 'gatepost', observations: 12, tokens: { total: 200000 }, fraction_of_measured_tokens: .2 }],
      models: [{ key: provider === 'claude' ? 'claude-opus-4-8' : 'gpt-5.6-sol', observations: 42, tokens: { total: 1000000 }, fraction_of_measured_tokens: 1 }],
      accounting: { unit: provider === 'claude' ? 'usd_api_equivalent' : 'codex_credits', estimated_capacity_low: 120, estimated_capacity_high: 180, pricing_coverage: 1 },
    },
  };
}

async function loadDashboard(page, options = {}) {
  const state = {
    dashboard: dashboardFixture(), profiles: profileFixture(), profileOptions: profileOptionsFixture(),
    runtimeConnections: [], agentContexts: [],
    requests: [], dashboardError: false, blockProfileDelete: false, taskCreateError: '', waitForReady: true, ...options,
  };
	if (state.pauseDashboard) state.dashboardGate = new Promise(resolve => { state.releaseDashboard = resolve; });
  state.tasks = {
    'audit-auth': { id: 'audit-auth', name: 'Audit authentication', priority: 70, type: 'recurring', state: 'queued', enabled: true, execution_profile_id: 'codex-devx', min_interval: 86400000000000, prompt: 'Inspect one bounded area.', require_repo_change: true, dispatch_tier: 'well_behind' },
  };

  await page.addInitScript(() => {
    window.__redlineEventSources = [];
    window.EventSource = class {
      constructor(url) { this.url = url; this.listeners = {}; window.__redlineEventSources.push(this); queueMicrotask(() => this.onopen?.({})); }
      addEventListener(name, listener) { (this.listeners[name] ||= []).push(listener); }
      emit(name, data) { for (const listener of this.listeners[name] || []) listener({ data: JSON.stringify(data) }); }
      fail() { this.onerror?.({}); }
      close() {}
    };
  });

  await page.route('http://redline.test/**', async route => {
    const request = route.request(), url = new URL(request.url()), method = request.method();
    if (assets[url.pathname]) {
      const [body, contentType] = assets[url.pathname];
      return route.fulfill({ status: 200, body, contentType });
    }
    const json = (status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    if (url.pathname === '/v1/dashboard') {
		if (state.dashboardGate) { await state.dashboardGate; state.dashboardGate = null; }
      return state.dashboardError ? json(500, { error: 'dashboard unavailable' }) : json(200, state.dashboard);
    }
    if (url.pathname === '/v1/profile-options') return json(200, state.profileOptions);
    if (url.pathname === '/v1/runtime-connections' && method === 'GET') return json(200, state.runtimeConnections);
    if (url.pathname === '/v1/runtime-connections' && method === 'POST') {
      const body = request.postDataJSON(); state.requests.push({ method, path: url.pathname, body });
      state.runtimeConnections.push(body); return json(201, body);
    }
    const runtimeMatch = url.pathname.match(/^\/v1\/runtime-connections\/([^/]+)$/);
    if (runtimeMatch && runtimeMatch[1] !== 'imports') {
      const id = decodeURIComponent(runtimeMatch[1]), item = state.runtimeConnections.find(connection => connection.id === id);
      if (method === 'GET') return item ? json(200, item) : json(404, { error: 'runtime connection not found' });
      if (method === 'PATCH') {
        const body = request.postDataJSON(); state.requests.push({ method, path: url.pathname, body });
        Object.assign(item, body); return json(200, item);
      }
      if (method === 'DELETE') {
        state.requests.push({ method, path: url.pathname });
        if (state.agentContexts.some(context => context.runtime_connection_id === id)) return json(409, { error: 'runtime connection has agent contexts' });
        state.runtimeConnections = state.runtimeConnections.filter(connection => connection.id !== id);
        return route.fulfill({ status: 204 });
      }
    }
    if (url.pathname === '/v1/runtime-connections/imports') return json(200, [{
      id: 'hermes-desktop', runtime: 'hermes', transport: 'gateway',
      url: 'http://hermes.test:9119', credential_source: 'hermes_desktop', max_concurrent_runs: 1,
    }]);
    if (/^\/v1\/runtime-connections\/[^/]+\/discover$/.test(url.pathname) && method === 'POST') return json(200, {
      version: '0.17.0', profiles: [{ name: 'default', path: '/home/jon/.hermes', model: 'gpt-5.5', provider: 'openai-codex' }],
      profile_options: [{
        profile: { name: 'default', path: '/home/jon/.hermes', model: 'gpt-5.5', provider: 'openai-codex' },
        projects: [{ id: 'scout', name: 'Scout', primary_path: '/home/jon/projects/scout' }],
        providers: [{ slug: 'openai-codex', authenticated: true, models: ['gpt-5.5', 'gpt-5.6-sol'] }],
      }],
    });
    if (url.pathname === '/v1/agent-contexts' && method === 'GET') return json(200, state.agentContexts);
    if (url.pathname === '/v1/agent-contexts' && method === 'POST') {
      const body = request.postDataJSON(); state.requests.push({ method, path: url.pathname, body });
      state.agentContexts.push(body); return json(201, body);
    }
    const contextMatch = url.pathname.match(/^\/v1\/agent-contexts\/([^/]+)$/);
    if (contextMatch && method === 'PATCH') {
      const body = request.postDataJSON(); state.requests.push({ method, path: url.pathname, body });
      Object.assign(state.agentContexts.find(item => item.id === decodeURIComponent(contextMatch[1])), body);
      return json(200, body);
    }
    if (contextMatch && method === 'DELETE') {
      state.requests.push({ method, path: url.pathname });
      state.agentContexts = state.agentContexts.filter(item => item.id !== decodeURIComponent(contextMatch[1]));
      return route.fulfill({ status: 204 });
    }
		const capacityMatch = url.pathname.match(/^\/v1\/providers\/([^/]+)\/capacity$/);
		if (capacityMatch) {
			const provider = decodeURIComponent(capacityMatch[1]).startsWith('claude') ? 'claude' : 'codex';
			return json(200, capacityFixture(provider));
		}
    const providerPolicyMatch = url.pathname.match(/^\/v1\/providers\/([^/]+)\/policy$/);
    if (providerPolicyMatch && method === 'PATCH') {
      const id = decodeURIComponent(providerPolicyMatch[1]), body = request.postDataJSON();
      const provider = state.dashboard.providers.find(item => item.id === id);
      state.requests.push({ method, path: url.pathname, body });
      if (!provider) return json(404, { error: 'provider is not configured' });
      if (body.policy && !state.dashboard.policies[body.policy]) return json(400, { error: 'policy is not configured' });
      provider.policy = body.policy || provider.default_policy;
      provider.policy_source = body.policy ? 'override' : 'global';
      return json(200, { policy: provider.policy, source: provider.policy_source });
    }
    if (url.pathname === '/v1/profiles' && method === 'GET') return json(200, state.profiles);
    if (url.pathname === '/v1/profiles' && method === 'POST') {
      const body = request.postDataJSON(); state.requests.push({ method, path: url.pathname, body });
      const profile = { ...body, created_at: '2026-07-20T19:00:00Z' }; state.profiles.push(profile); return json(201, profile);
    }
    const profileMatch = url.pathname.match(/^\/v1\/profiles\/([^/]+)$/);
    if (profileMatch) {
      const id = decodeURIComponent(profileMatch[1]), profile = state.profiles.find(item => item.id === id);
      if (method === 'GET') return profile ? json(200, profile) : json(404, { error: 'profile not found' });
      if (method === 'DELETE') {
        state.requests.push({ method, path: url.pathname });
        if (state.blockProfileDelete) return json(409, { error: 'profile is assigned to a task' });
        state.profiles = state.profiles.filter(item => item.id !== id); return route.fulfill({ status: 204 });
      }
      if (method === 'PATCH') {
        const body = request.postDataJSON(); state.requests.push({ method, path: url.pathname, body }); Object.assign(profile, body); return json(200, profile);
      }
    }
    if (url.pathname === '/v1/tasks' && method === 'GET') return json(200, Object.values(state.tasks));
    if (url.pathname === '/v1/tasks' && method === 'POST') {
      const body = request.postDataJSON(); state.requests.push({ method, path: url.pathname, body });
		if (state.taskCreateError) return json(400, { error: state.taskCreateError });
      const task = { ...body, id: 'created-task', enabled: true, state: 'queued' }; state.tasks[task.id] = task; return json(201, task);
    }
    const taskMatch = url.pathname.match(/^\/v1\/tasks\/([^/]+)(?:\/(enable|disable|retry))?$/);
    if (taskMatch) {
      const id = decodeURIComponent(taskMatch[1]), control = taskMatch[2], task = state.tasks[id];
      if (method === 'GET') return task ? json(200, task) : json(404, { error: 'task not found' });
      if (method === 'POST' && control) { state.requests.push({ method, path: url.pathname }); task.enabled = control !== 'disable'; task.state = task.enabled ? 'queued' : 'disabled'; return json(200, task); }
      if (method === 'DELETE') return json(409, { error: 'task has run history' });
      if (method === 'PATCH') { const body = request.postDataJSON(); state.requests.push({ method, path: url.pathname, body }); Object.assign(task, body); return json(200, task); }
    }
    const logMatch = url.pathname.match(/^\/v1\/runs\/([^/]+)\/logs$/);
    if (logMatch) return json(200, { run_id: logMatch[1], stream: url.searchParams.get('stream'), content: url.searchParams.get('stream') === 'stderr' ? 'warning from stderr' : '{"type":"result","result":"ok"}' });
    return json(404, { error: `unhandled ${method} ${url.pathname}` });
  });

  await page.goto('http://redline.test/');
	if (state.waitForReady) {
		await expect(page.getByRole('heading', { name: 'Scheduled jobs' })).toBeVisible();
		await expect(page.getByText('Audit authentication')).toBeVisible();
	}
  return state;
}

test('renders operational state and applies live dashboard events', async ({ page }) => {
  const state = await loadDashboard(page);
  await expect(page.getByRole('button', { name: 'Recent errors' })).toBeVisible();
  await expect(page.locator('#health-explainer')).toContainText('temporary usage source timeout');
  await expect(page.getByRole('button', { name: 'Show Claude usage details' })).toContainText('53%');
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
  await page.locator('#task-type').selectOption('recurring');
  await page.locator('#task-interval').fill('7d');
  await page.locator('#task-prompt').fill('Inspect one cache invalidation path and report findings.');
  await page.locator('#task-repo-change').check();
  await page.getByRole('button', { name: 'Save job' }).click();
  await expect(dialog).toBeHidden();
  await expect.poll(() => state.requests.filter(item => item.path === '/v1/tasks').length).toBe(1);
  expect(state.requests.find(item => item.path === '/v1/tasks').body).toMatchObject({ priority: 82, dispatch_tier: 'expiring', type: 'recurring', min_interval: '7d', require_repo_change: true });
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

  state.blockProfileDelete = true;
  page.once('dialog', confirmation => confirmation.accept());
  await page.getByRole('button', { name: 'Delete' }).click();
  await expect(page.locator('#profile-form-error')).toContainText('profile is assigned to a task');
});

test('imports Hermes Desktop and creates a remote profile from discovered context', async ({ page }) => {
  const state = await loadDashboard(page);
  await page.getByRole('button', { name: 'Profiles' }).click();
  await page.locator('#profile-id').fill('hermes-scout');
  await page.locator('#profile-provider').selectOption('codex-main');
  await page.locator('#profile-harness').selectOption('hermes');
  await expect(page.locator('#profile-hermes-fields')).toBeVisible();
  await page.getByRole('button', { name: 'Import Desktop' }).click();
  await expect(page.locator('#profile-hermes-status')).toContainText('1 profile discovered');
  await expect(page.locator('#profile-runtime-profile')).toHaveValue('default');
  await page.locator('#profile-runtime-project').selectOption('scout');
  await expect(page.locator('#profile-model-choice')).toHaveValue('default');
  await page.locator('#profile-model-choice').selectOption('openai-codex/gpt-5.6-sol');
  await page.getByRole('button', { name: 'Save profile' }).click();

  await expect.poll(() => state.requests.some(item => item.path === '/v1/agent-contexts')).toBe(true);
  await expect.poll(() => state.requests.some(item => item.path === '/v1/profiles')).toBe(true);
  expect(state.requests.find(item => item.path === '/v1/agent-contexts').body).toMatchObject({
    id: 'hermes-scout-context', runtime_connection_id: 'hermes-desktop',
    profile: 'default', project: 'scout', working_directory: '/home/jon/projects/scout',
    session_mode: 'isolated',
  });
  expect(state.requests.find(item => item.path === '/v1/profiles').body).toMatchObject({
    id: 'hermes-scout', provider_account_id: 'codex-main',
    agent_context_id: 'hermes-scout-context', harness_type: 'hermes',
    model: 'openai-codex/gpt-5.6-sol', workspace_provider: 'runtime-owned',
    repository: '/home/jon/projects/scout',
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
  await page.getByRole('button', { name: 'View logs →' }).click();
  await expect(page.locator('#log-content')).toContainText('"result":"ok"');
  await page.getByRole('button', { name: 'stderr' }).click();
  await expect(page.locator('#log-content')).toContainText('warning from stderr');
  await page.getByRole('button', { name: 'Close' }).click();

  await page.getByRole('button', { name: 'Manage' }).click();
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
	await expect(page.getByText('Audit authentication')).toBeVisible();
	await page.getByRole('button', { name: '+ New job' }).click();
	await page.locator('#task-name').fill('Invalid scheduled job');
	await page.locator('#task-profile').selectOption('codex-devx');
	await page.locator('#task-prompt').fill('Do a small thing.');
	await page.getByRole('button', { name: 'Save job' }).click();
	await expect(page.locator('#task-form-error')).toContainText('minimum interval is invalid');
	await expect(page.getByRole('dialog', { name: 'New scheduled job' })).toBeVisible();
});
