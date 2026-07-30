const { expect } = require("@playwright/test");
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
  const claudeTrigger = new Date(Date.now() + 2 * 60 * 60 * 1000).toISOString();
  const codexTrigger = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString();
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
      { id: 'claude-main', provider: 'claude', policy: 'standard', policy_source: 'global', default_policy: 'standard', max_concurrent_runs: 1, active_runs: 0, usage_source: { active: 'native', last_error: 'OpenUsage unavailable; using native collection' }, latest_decision: { decision: 'WAIT', policy: 'standard', mode: 'window_slots', reason: 'no actionable weekly overflow', overflow: .01, rolling_dispatchable: .37, pace_gap: .08, projected_trigger_at: claudeTrigger, projection_basis: 'Assumes weekly usage stays unchanged.' }, snapshot: { provider: 'claude', observed_at: observed, short: { remaining: 0.62, resets_at: '2026-07-20T23:00:00Z' }, weekly: { remaining: 0.53, resets_at: '2026-07-24T19:00:00Z' }, allowances: [{ key: 'model:fable:weekly', source_label: 'Fable', scope: 'model', role: 'weekly', remaining: 1, resets_at: '2026-07-24T19:00:00Z', period_duration_seconds: 604800, reset_inferred: true }] } },
      { id: 'codex-main', provider: 'codex', policy: 'standard', policy_source: 'global', default_policy: 'standard', max_concurrent_runs: 2, active_runs: 1, pool_concurrency: { weekly: 2 }, active_pool_claims: { weekly: 1 }, usage_source: { active: 'openusage' }, latest_decision: { decision: 'WAIT', policy: 'standard', mode: 'pace_threshold', reason: 'no pace threshold matched', pace_gap: .08, projected_trigger_at: codexTrigger, projection_basis: 'Assumes weekly usage stays unchanged.' }, snapshot: { provider: 'codex', observed_at: observed, weekly: { remaining: 0.47, resets_at: '2026-07-25T19:00:00Z' }, allowances: [] } },
    ],
    tasks: [{ id: 'audit-auth', name: 'Audit authentication', priority: 70, type: 'recurring', state: 'queued', enabled: true, execution_profile_id: 'codex-devx', provider_account_id: 'codex-main', harness_type: 'codex-cli', model: 'gpt-5.5', workspace_provider: 'devx', min_interval: 86400000000000, require_repo_change: true, dispatch_tier: 'well_behind' }],
    unread_runs: 1,
    runs: [{ id: 'run-1', task_id: 'audit-auth', provider_account_id: 'codex-main', state: 'completed', started_at: observed, completed_at: observed, summary: 'Opened a focused authentication fix.', outcome: 'changes_proposed', actual_provider: 'openai-codex', actual_model: 'gpt-5.6-sol', artifacts: [{ type: 'pull_request', label: 'PR #42', url: 'https://github.com/acme/redline/pull/42' }] }],
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

function taskTemplatesFixture() {
  return [{
    id: 'bug-hunt', name: 'Find and fix one reproducible bug',
    description: 'Reproduce one real defect, fix its root cause, and verify the affected suite.',
    prompt: 'Find ONE real, demonstrable bug. Reproduce it, fix it, and verify the affected suite.',
    priority: 80, type: 'recurring', dispatch_tier: 'behind',
    min_interval: 259200000000000, require_repo_change: true,
    requirements: ['Repository access', 'Test and static-analysis tools'],
  }];
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
    taskTemplates: taskTemplatesFixture(),
    runtimeConnections: [], agentContexts: [],
    runtimeJobs: {},
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
    if (url.pathname === '/v1/task-templates') return json(200, state.taskTemplates);
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
    const runtimeJobsMatch = url.pathname.match(/^\/v1\/runtime-connections\/([^/]+)\/jobs$/);
    if (runtimeJobsMatch && method === 'GET') {
      return json(200, state.runtimeJobs[decodeURIComponent(runtimeJobsMatch[1])] || []);
    }
    if (/^\/v1\/runtime-connections\/[^/]+\/discover$/.test(url.pathname) && method === 'POST') return json(200, {
      version: '0.17.0', profiles: [{ name: 'default', path: '/home/tester/.hermes', model: 'gpt-5.5', provider: 'openai-codex' }],
      profile_options: [{
        profile: { name: 'default', path: '/home/tester/.hermes', model: 'gpt-5.5', provider: 'openai-codex' },
        projects: [{ id: 'sample-app', name: 'Sample App', primary_path: '/home/tester/projects/sample-app' }],
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
    if (logMatch) return json(200, { run_id: logMatch[1], stream: url.searchParams.get('stream'), content: url.searchParams.get('stream') === 'stderr' ? 'warning from stderr' : '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}\n{"type":"turn.completed","usage":{"input_tokens":9346,"cached_input_tokens":7040,"output_tokens":28}}\n{"type":"result","result":"ok"}' });
    const readMatch = url.pathname.match(/^\/v1\/runs\/([^/]+)\/read$/);
    if (readMatch && method === 'POST') { state.requests.push({ method, path: url.pathname }); return json(200, { read: true }); }
    if (url.pathname === '/v1/runs/read-all' && method === 'POST') { state.requests.push({ method, path: url.pathname }); return json(200, { read: true }); }
    return json(404, { error: `unhandled ${method} ${url.pathname}` });
  });

  await page.goto('http://redline.test/');
	if (state.waitForReady) {
		await expect(page.getByRole('heading', { name: 'Scheduled jobs' })).toBeVisible();
		await expect(page.locator('#tasks-body').getByText('Audit authentication')).toBeVisible();
	}
  return state;
}

module.exports = { loadDashboard, dashboardFixture, profileFixture, profileOptionsFixture, taskTemplatesFixture, capacityFixture };
