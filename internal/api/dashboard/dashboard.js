const $ = (selector) => document.querySelector(selector);
const escapeHTML = (value = "") => String(value).replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const relative = (value) => {
  if (!value) return "—";
  const seconds = Math.round((new Date(value) - Date.now()) / 1000), abs = Math.abs(seconds);
  const [amount, unit] = abs < 60 ? [abs,"sec"] : abs < 3600 ? [Math.round(abs/60),"min"] : abs < 86400 ? [Math.round(abs/3600),"hr"] : [Math.round(abs/86400),"day"];
  return seconds >= 0 ? `in ${amount} ${unit}${amount === 1 ? "" : "s"}` : `${amount} ${unit}${amount === 1 ? "" : "s"} ago`;
};
const shortTime = (value) => value ? new Intl.DateTimeFormat(undefined,{month:'short',day:'numeric',hour:'numeric',minute:'2-digit'}).format(new Date(value)) : "—";
const duration = (nanos) => {
  if (!nanos) return "Always eligible";
  const hours = nanos / 3.6e12;
  if (hours >= 24 && hours % 24 === 0) return `${hours/24}d minimum`;
  return hours >= 1 ? `${Math.round(hours)}h minimum` : `${Math.round(nanos/6e10)}m minimum`;
};
const title = (value) => String(value || "").replace(/[_-]/g," ").replace(/\b\w/g,c=>c.toUpperCase());
const percent = (remaining) => Math.max(0,Math.min(100,Math.round((remaining || 0)*100)));
const compactNumber = (value) => new Intl.NumberFormat(undefined,{notation:'compact',maximumFractionDigits:1}).format(value || 0);
const tokenNumber = (value) => new Intl.NumberFormat().format(value || 0);

let currentRun = null, currentLogContent = '', currentLogView = 'formatted';
let profiles = [], taskTemplates = [], providerAccounts = [], providerCatalog = [], harnessCatalog = [], editingProfile = '', policyCatalog = {};
let runtimeConnections = [], agentContexts = [], hermesDiscovery = null, editingRuntimeConnection = '';
const capacityCache = new Map();
function meter(label, remaining, reset) {
  const value = percent(remaining), tone = value < 15 ? "danger" : value < 35 ? "warn" : "";
  return `<div><div class="meter-head"><span>${escapeHTML(label)}</span><b>${value}% left</b></div><div class="meter-track"><div class="meter-fill ${tone}" style="width:${value}%"></div></div><div class="reset">Resets ${escapeHTML(relative(reset))} · ${escapeHTML(shortTime(reset))}</div></div>`;
}
function policyControl(item, provider) {
  const defaultPolicy = item.default_policy || item.policy || 'default';
  const selected = item.policy_source === 'override' ? item.policy : '';
  const options = Object.keys(policyCatalog).sort().map(name =>
    `<option value="${escapeHTML(name)}"${selected === name ? ' selected' : ''}>${escapeHTML(title(name))}</option>`
  ).join('');
  const configured = policyCatalog[item.policy] || {};
  const reserve = configured.rolling_reserve == null ? '' : ` · preserves ${percent(configured.rolling_reserve)}% of the current window`;
  return `<span class="policy-control"><label><span>Dispatch policy <i class="help" tabindex="0" data-help="Policies control how early Redline admits background work and how much short-window capacity it protects. Choose Default to follow YAML configuration.">?</i></span><select data-provider-policy="${escapeHTML(item.id)}" aria-label="${escapeHTML(title(provider))} dispatch policy"><option value=""${selected === '' ? ' selected' : ''}>Default · ${escapeHTML(title(defaultPolicy))}</option>${options}</select></label><small>Using ${escapeHTML(title(item.policy))} via ${escapeHTML(item.policy_source)}${escapeHTML(reserve)}</small></span>`;
}
function concurrencyStatus(item) {
  const active = item.active_runs || 0, maximum = item.max_concurrent_runs || 1;
  const source = item.concurrency_source === 'override' ? 'custom override' : `config default ${item.default_max_concurrent_runs || maximum}`;
  const reset = item.concurrency_source === 'override' ? `<button type="button" data-provider-concurrency-reset="${escapeHTML(item.id)}">Use default</button>` : '';
  const pools = Object.entries(item.pool_concurrency || {}).sort(([left],[right]) => left.localeCompare(right)).map(([pool,limit]) =>
    `<span><b>${escapeHTML(title(pool))}</b><i>${item.active_pool_claims?.[pool] || 0}/${limit}</i></span>`
  ).join('');
  return `<span class="concurrency-status"><span><b>Parallel runs <i class="help" tabindex="0" data-help="The provider limit caps all simultaneous runs. Hermes connection and context limits, plus optional allowance-pool limits, apply independently.">?</i></b><span class="concurrency-control"><input data-provider-concurrency="${escapeHTML(item.id)}" type="number" min="1" max="32" value="${maximum}" aria-label="${escapeHTML(title(item.provider))} parallel run limit"><i>${active}/${maximum} active · ${escapeHTML(source)}</i>${reset}</span></span>${pools ? `<small>${pools}</small>` : ''}</span>`;
}
function providerPressure(item) {
  if (item.paused) return {label:'Paused',detail:'Scheduling is paused for this provider.',tone:'paused'};
  const current = item.latest_decision;
  if (!current) return {label:'Evaluating',detail:'Waiting for the first scheduler decision.',tone:'neutral'};
  if (current.decision === 'RUN') {
    return {label:`Redline · ${title(current.unlocked_tier || 'ready')}`,detail:current.reason || 'Background work is eligible to run.',tone:'triggered'};
  }
  if (current.mode === 'active_run') {
    return {label:'Running · limit reached',detail:current.reason || 'Redline is waiting for an active run to finish.',tone:'triggered'};
  }
  if (current.mode === 'window_slots') {
    if (current.reason === 'current 5-hour reserve protected') {
      return {label:'Protected · 5h reserve',detail:current.reason,tone:'protected'};
    }
    const margin = policyCatalog[item.policy]?.trigger_margin || 0;
    const distance = Math.max(0,margin - (current.overflow || 0));
    const points = Math.max(0,Math.ceil((distance * 100) - .000001));
    const projected = current.projected_trigger_at && new Date(current.projected_trigger_at) > new Date()
      ? `Likely eligible ${relative(current.projected_trigger_at)}`
      : '';
    return {
      label:projected || `Watching · ${points}% to trigger`,
      detail:`${current.projection_basis || 'Assumes weekly usage stays unchanged.'} ${points} percentage point${points === 1 ? '' : 's'} of additional projected weekly overflow needed.${current.reason ? ` ${current.reason}.` : ''}`,
      tone:points <= 2 ? 'near' : 'neutral',
    };
  }
  const pacePoints = Math.round((current.pace_gap || 0) * 100);
  if (current.projected_trigger_at && new Date(current.projected_trigger_at) > new Date()) {
    return {
      label:`Likely eligible ${relative(current.projected_trigger_at)}`,
      detail:`${current.projection_basis || 'Assumes weekly usage stays unchanged.'}${current.reason ? ` ${current.reason}.` : ''}`,
      tone:'neutral',
    };
  }
  if (pacePoints > 0) {
    return {label:`${pacePoints}% behind pace`,detail:current.reason || 'Waiting for a configured pace threshold.',tone:pacePoints >= 15 ? 'near' : 'neutral'};
  }
  return {label:'On pace',detail:current.reason || 'No dispatch threshold is currently active.',tone:'healthy'};
}
function providerCompact(item) {
  const snap = item.snapshot, provider = String(item.provider || item.id).toLowerCase();
  const source = item.usage_source?.active || snap?.source || 'unknown';
  const icon = provider === 'claude' ? 'claude.svg' : 'codex.svg';
  const weekly = snap?.weekly, value = weekly ? percent(weekly.remaining) : 0, tone = value < 35 ? 'warn' : '';
  const pressure = providerPressure(item);
  const weeklyReset = weekly ? `Week resets ${relative(weekly.resets_at)}` : 'Weekly reset unavailable';
  const shortWindow = snap?.short
    ? `5h ${percent(snap.short.remaining)}% · resets ${relative(snap.short.resets_at)}`
    : 'No 5h limit';
  let details = `<p class="no-data">${escapeHTML(item.error || 'Waiting for usage data.')}</p>`;
  if (snap) {
    const windows = [];
    if (snap.short) windows.push(meter('5-hour window',snap.short.remaining,snap.short.resets_at));
    windows.push(meter('Weekly allowance',snap.weekly.remaining,snap.weekly.resets_at));
    (snap.allowances || []).filter(window => window.scope === 'model').forEach(window => {
      const label = `${window.source_label || title(window.key)}${window.reset_inferred ? ' · reset inferred' : ''}`;
      windows.push(meter(label,window.remaining,window.resets_at));
    });
    const decisionDetail = `<span class="decision-detail ${escapeHTML(pressure.tone)}"><b>${escapeHTML(pressure.label)}</b><span>${escapeHTML(pressure.detail)}${item.latest_decision_at ? ` · checked ${escapeHTML(relative(item.latest_decision_at))}` : ''}</span></span>`;
    details = `${decisionDetail}${policyControl(item, provider)}${concurrencyStatus(item)}<div class="meters">${windows.join('')}</div>`;
  }
  const cached = capacityCache.get(item.id);
  const evidence = cached ? renderCapacityEvidence(cached) : '<span class="capacity-loading">Open to load empirical capacity evidence.</span>';
  const sourceError = item.usage_source?.last_error ? `<span class="source-error">${escapeHTML(item.usage_source.last_error)}</span>` : '';
  return `<div class="provider-compact" data-provider-id="${escapeHTML(item.id)}"><button class="provider-trigger" type="button" aria-label="Show ${escapeHTML(title(provider))} usage details"><span class="provider-logo ${escapeHTML(provider)}"><img src="/assets/${icon}" alt=""></span><span class="provider-summary"><span class="provider-copy-line"><strong>${escapeHTML(title(provider))}</strong><b>${weekly ? `${value}% weekly` : '—'}</b></span><span class="provider-window-line"><span>${escapeHTML(shortWindow)}</span><span>${escapeHTML(weeklyReset)}</span></span><span class="provider-pressure ${escapeHTML(pressure.tone)}">${escapeHTML(pressure.label)}</span><span class="compact-track"><span class="compact-fill ${tone}" style="width:${value}%"></span></span></span></button><span class="provider-detail"><span class="detail-head"><strong>${escapeHTML(title(provider))} capacity</strong><span>${escapeHTML(title(source))} · ${snap ? `sampled ${escapeHTML(relative(snap.observed_at))}` : 'offline'}</span></span>${sourceError}${details}<span class="capacity-evidence" data-capacity-evidence${cached ? ' data-loaded="true"' : ''}>${evidence}</span></span></div>`;
}
function wireProviderDetails() {
  document.querySelectorAll('.provider-compact').forEach(card => {
    const trigger = card.querySelector('.provider-trigger');
    card.addEventListener('mouseenter', () => loadCapacityEvidence(card));
    trigger.addEventListener('focus', () => loadCapacityEvidence(card));
    trigger.addEventListener('click', event => {
      event.stopPropagation();
      document.querySelectorAll('.provider-compact.open').forEach(open => { if (open !== card) open.classList.remove('open'); });
      card.classList.toggle('open');
      if (card.classList.contains('open')) loadCapacityEvidence(card);
    });
  });
  document.querySelectorAll('[data-provider-policy]').forEach(select => {
    select.addEventListener('click', event => event.stopPropagation());
    select.addEventListener('change', async event => {
      event.stopPropagation();
      const control = event.currentTarget, providerID = control.dataset.providerPolicy;
      control.disabled = true;
      try {
        await apiRequest(`/v1/providers/${encodeURIComponent(providerID)}/policy`, {
          method: 'PATCH', body: JSON.stringify({policy: control.value})
        });
        await refresh();
      } catch (error) {
        $('#error-banner').hidden = false;
        $('#error-banner').textContent = `Could not update provider policy: ${error.message}`;
        control.disabled = false;
      }
    });
  });
  document.querySelectorAll('[data-provider-concurrency]').forEach(input => {
    input.addEventListener('click', event => event.stopPropagation());
    input.addEventListener('change', async event => {
      event.stopPropagation();
      const control = event.currentTarget, providerID = control.dataset.providerConcurrency;
      const limit = Number(control.value);
      if (!Number.isInteger(limit) || limit < 1 || limit > 32) {
        $('#error-banner').hidden = false;
        $('#error-banner').textContent = 'Provider parallel runs must be between 1 and 32.';
        return;
      }
      control.disabled = true;
      try {
        await apiRequest(`/v1/providers/${encodeURIComponent(providerID)}/concurrency`, {
          method:'PATCH', body:JSON.stringify({max_concurrent_runs:limit})
        });
        await refresh();
      } catch (error) {
        $('#error-banner').hidden = false;
        $('#error-banner').textContent = `Could not update provider concurrency: ${error.message}`;
        control.disabled = false;
      }
    });
  });
  document.querySelectorAll('[data-provider-concurrency-reset]').forEach(button => {
    button.addEventListener('click', async event => {
      event.stopPropagation();
      const control = event.currentTarget, providerID = control.dataset.providerConcurrencyReset;
      control.disabled = true;
      try {
        await apiRequest(`/v1/providers/${encodeURIComponent(providerID)}/concurrency`, {
          method:'PATCH', body:JSON.stringify({max_concurrent_runs:0})
        });
        await refresh();
      } catch (error) {
        $('#error-banner').hidden = false;
        $('#error-banner').textContent = `Could not reset provider concurrency: ${error.message}`;
        control.disabled = false;
      }
    });
  });
}
function evidenceRows(items = []) {
  return items.slice(0,4).map(item => `<span><b>${escapeHTML(item.key)}</b><i>${percent(item.fraction_of_measured_tokens)}%</i></span>`).join('') || '<em>No classified evidence</em>';
}
function accountingSummary(accounting) {
  if (!accounting) return 'No weighted accounting estimate';
  const low = compactNumber(accounting.estimated_capacity_low), high = compactNumber(accounting.estimated_capacity_high);
  const amount = accounting.unit === 'usd_api_equivalent' ? `$${low}–$${high}` : `${low}–${high}`;
  const unit = accounting.unit === 'usd_api_equivalent' ? 'API-dollar equivalent' : title(accounting.unit);
  return `${amount} ${unit} · ${percent(accounting.pricing_coverage)}% priced`;
}
function renderCapacityEvidence(data) {
  const estimate = data.weekly || data.short;
  if (!estimate) return '<span class="capacity-loading">Not enough correlated usage evidence yet.</span>';
  const measured = estimate.measured_tokens || {}, total = measured.total || 0;
  const mix = [['uncached',measured.input],['cached',measured.cache_read],['output',measured.output],['cache write',measured.cache_creation]]
    .filter(([,tokens]) => tokens > 0)
    .map(([label,tokens]) => `<span>${escapeHTML(label)} <b>${total ? Math.round(tokens/total*100) : 0}%</b></span>`).join('');
  const crossCheck = data.ratio_derived_weekly_tokens ? ` · 5h cross-check ${compactNumber(data.ratio_derived_weekly_tokens)}${data.ratio_derived_difference != null ? ` (${Math.round(data.ratio_derived_difference*100)}% apart)` : ''}` : '';
  return `<span class="capacity-card"><span class="capacity-title"><strong>Empirical ${escapeHTML(estimate.window)} capacity</strong><i class="confidence ${escapeHTML(estimate.confidence)}">${escapeHTML(estimate.confidence)}</i></span><b class="capacity-total">${escapeHTML(compactNumber(estimate.estimated_tokens?.total))} processed tokens</b><span class="capacity-meta">${percent(estimate.attribution_coverage)}% attributed · ${estimate.closed_spans || 0}/${estimate.observed_spans || 0} drain spans${escapeHTML(crossCheck)} · ${escapeHTML(accountingSummary(estimate.accounting))}</span><span class="token-mix">${mix}</span><span class="evidence-label">Sources</span><span class="evidence-rows">${evidenceRows(estimate.sources)}</span><span class="evidence-label">Models</span><span class="evidence-rows">${evidenceRows(estimate.models)}</span></span>`;
}
async function loadCapacityEvidence(card) {
  const target = card.querySelector('[data-capacity-evidence]');
  if (!target || target.dataset.loaded === 'true' || target.dataset.loading === 'true') return;
  const providerID = card.dataset.providerId;
  if (capacityCache.has(providerID)) {
    target.innerHTML = renderCapacityEvidence(capacityCache.get(providerID)); target.dataset.loaded = 'true';
    return;
  }
  target.dataset.loading = 'true';
  target.innerHTML = '<span class="capacity-loading">Loading empirical capacity evidence…</span>';
  try {
    const response = await fetch(`/v1/providers/${encodeURIComponent(providerID)}/capacity`), data = await response.json();
    if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
    capacityCache.set(providerID, data);
    target.innerHTML = renderCapacityEvidence(data); target.dataset.loaded = 'true';
  } catch (error) {
    target.innerHTML = `<span class="capacity-loading error">Capacity evidence unavailable: ${escapeHTML(error.message)}</span>`;
  } finally { target.dataset.loading = 'false'; }
}
function renderTasks(tasks) {
  $('#task-count').textContent = `${tasks.length} task${tasks.length === 1 ? '' : 's'}`;
  $('#tasks-body').innerHTML = tasks.length ? tasks.map(task => `<tr class="task-row" data-task-row="${escapeHTML(task.id)}" tabindex="0"><td><span class="priority">P${task.priority}</span></td><td><span class="job-name">${escapeHTML(task.name)}</span><span class="subtle">${escapeHTML(task.id)}</span></td><td><span class="tier tier-${escapeHTML(task.dispatch_tier || 'behind')}">${escapeHTML(title(task.dispatch_tier || 'behind'))}</span></td><td><span class="tag">${escapeHTML(task.provider_account_id)}</span><span class="tag">${escapeHTML(task.model || task.harness_type)}</span></td><td><span class="job-name">${escapeHTML(title(task.type))}</span><span class="subtle">${escapeHTML(duration(task.min_interval))}${task.require_repo_change ? ' · repo change required' : ''}</span></td><td><span class="status ${escapeHTML(task.state)}">${escapeHTML(task.state)}</span></td><td><button class="manage-button" type="button" data-task="${escapeHTML(task.id)}">Manage</button></td></tr>`).join('') : '<tr><td colspan="7" class="empty">No jobs are queued yet. Create one to start using spare capacity.</td></tr>';
  document.querySelectorAll('.manage-button').forEach(button => button.addEventListener('click',event => { event.stopPropagation(); openTask(button.dataset.task); }));
  document.querySelectorAll('[data-task-row]').forEach(row => {
    row.addEventListener('click',() => openTask(row.dataset.taskRow));
    row.addEventListener('keydown',event => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        openTask(row.dataset.taskRow);
      }
    });
  });
}
function renderRuns(runs) {
  $('#run-count').textContent = `${runs.length} run${runs.length === 1 ? '' : 's'}`;
  $('#runs-list').innerHTML = runs.length ? runs.map(run => `<div class="activity"><i class="activity-dot ${escapeHTML(run.state)}"></i><div><strong>${escapeHTML(run.task_id)}</strong><p>${escapeHTML(run.provider_account_id)} · ${escapeHTML(title(run.state))}${run.error ? ` · ${escapeHTML(run.error)}` : ''}</p><button class="log-link" data-run="${escapeHTML(run.id)}">View logs →</button></div><time>${escapeHTML(relative(run.started_at))}</time></div>`).join('') : '<p class="empty">No runs recorded yet.</p>';
  document.querySelectorAll('[data-run]').forEach(button => button.addEventListener('click', () => openLogs(button.dataset.run)));
}
function renderAttempts(attempts) {
  $('#attempt-count').textContent = `${attempts.length} event${attempts.length === 1 ? '' : 's'}`;
  $('#attempts-list').innerHTML = attempts.length ? attempts.slice(0,12).map(a => `<div class="activity"><i class="activity-dot ${escapeHTML(a.outcome)}"></i><div><strong>${escapeHTML(title(a.outcome))} · ${escapeHTML(a.provider_account_id)}</strong><p>${escapeHTML(a.reason || a.error || a.mode || 'Scheduler evaluated capacity')}</p></div><time>${escapeHTML(relative(a.completed_at))}</time></div>`).join('') : '<p class="empty">No scheduler decisions recorded yet.</p>';
}
function renderHealth(health, attempts) {
  const healthy = health.status === 'ok' || health.status === 'healthy';
  $('#health-pill').className = `health-pill ${healthy ? '' : 'bad'}`;
  $('#health-pill').innerHTML = `<i></i><span>${healthy ? 'healthy' : 'Recent errors'}</span>`;
  const latest = attempts[0], recentError = attempts.find(attempt => attempt.outcome === 'error');
  const latestState = latest
    ? `The newest scheduler check was ${latest.outcome}${latest.completed_at ? ` ${relative(latest.completed_at)}` : ''}.`
    : 'No recent scheduler check is available.';
  const cause = recentError ? ` Latest sampled cause: ${recentError.error || recentError.reason}.` : '';
  const detail = healthy
    ? `No run, dispatch, or notification failures were recorded during the last ${health.window}.`
    : `${health.dispatch_errors} of ${health.dispatch_attempts} dispatch checks failed during the rolling ${health.window}; this does not mean the service is currently offline. ${latestState}${cause}`;
  $('#health-explainer').innerHTML = `<strong>${healthy ? 'Operational health' : 'Recent scheduler errors'}</strong><p>${escapeHTML(detail)}</p>`;
}
function render(data) {
	 document.body.dataset.updatedAt = data.generated_at;
  policyCatalog = data.policies || {};
  providerCatalog = data.providers.map(provider => ({id:provider.id,provider:provider.provider}));
  providerAccounts = providerCatalog.map(provider => provider.id);
  const openProviderID = document.querySelector('.provider-compact.open')?.dataset.providerId;
  $('#usage-strip').innerHTML = data.providers.map(providerCompact).join(''); wireProviderDetails();
  if (openProviderID) {
    document.querySelectorAll('.provider-compact').forEach(card => {
      if (card.dataset.providerId === openProviderID) { card.classList.add('open'); loadCapacityEvidence(card); }
    });
  }
  const policies = new Set(data.providers.map(provider => provider.policy).filter(Boolean));
  $('#policy').textContent = policies.size > 1 ? 'per provider' : ([...policies][0] || data.active_policy || '—');
  $('#next-check').textContent = data.scheduler.next_cycle_at ? relative(data.scheduler.next_cycle_at) : data.scheduler.enabled ? 'starting' : 'disabled';
  $('#active-runs').textContent = data.health.active_runs;
  $('#updated-at').textContent = `Updated ${shortTime(data.generated_at)}`;
  renderHealth(data.health,data.attempts); renderTasks(data.tasks); renderRuns(data.runs); renderAttempts(data.attempts);
  $('#error-banner').hidden = true;
}
async function openLogs(run) {
  currentRun = run; currentLogView = 'formatted'; $('#log-title').textContent = run; $('#logs-dialog').showModal();
  document.querySelectorAll('.log-tabs button[data-stream]').forEach(b => b.classList.toggle('active',b.dataset.stream === 'stdout'));
  document.querySelectorAll('[data-log-view]').forEach(b => b.classList.toggle('active',b.dataset.logView === currentLogView));
  await loadLog('stdout');
}
function logUsage(usage = {}) {
  const parts = [];
  if (usage.input_tokens != null || usage.input != null) parts.push(`${tokenNumber(usage.input_tokens ?? usage.input)} input`);
  if (usage.cached_input_tokens != null || usage.cache_read_input_tokens != null) parts.push(`${tokenNumber(usage.cached_input_tokens ?? usage.cache_read_input_tokens)} cached`);
  if (usage.output_tokens != null || usage.output != null) parts.push(`${tokenNumber(usage.output_tokens ?? usage.output)} output`);
  if (usage.reasoning_output_tokens != null || usage.reasoning != null) parts.push(`${tokenNumber(usage.reasoning_output_tokens ?? usage.reasoning)} reasoning`);
  return parts.join(' · ');
}
function contentText(content) {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  return content.filter(item => item?.type === 'text' && item.text).map(item => item.text).join('\n');
}
function formattedLogLine(line) {
  let event;
  try { event = JSON.parse(line); } catch (_) { return line; }
  const type = event.type || 'event';
  if (type === 'hermes.result') {
    const usage = logUsage(event.usage);
    return `RESULT · ${event.provider || 'Hermes'} / ${event.model || 'default'}\n${event.output || '(no response)'}${usage ? `\n${usage}` : ''}`;
  }
  if (type === 'item.completed' && event.item?.type === 'agent_message') {
    return `ASSISTANT\n${event.item.text || '(empty response)'}`;
  }
  if (type === 'turn.completed') {
    const usage = logUsage(event.usage);
    return `COMPLETED${usage ? `\n${usage}` : ''}`;
  }
  if (type === 'assistant') {
    const text = contentText(event.message?.content);
    const tools = (event.message?.content || []).filter(item => item?.type === 'tool_use').map(item => item.name).filter(Boolean);
    return text ? `ASSISTANT\n${text}` : tools.length ? `TOOLS\n${tools.join(' · ')}` : 'ASSISTANT · response metadata';
  }
  if (type === 'user') {
    const text = contentText(event.message?.content);
    const toolResults = (event.message?.content || []).filter(item => item?.type === 'tool_result').length;
    return text ? `USER\n${text}` : toolResults ? `TOOL RESULTS · ${toolResults}` : 'USER · message metadata';
  }
  if (type === 'result') {
    const usage = logUsage(event.usage);
    const result = typeof event.result === 'string' ? event.result : event.subtype || 'complete';
    const cost = event.total_cost_usd != null ? ` · $${Number(event.total_cost_usd).toFixed(4)}` : '';
    return `RESULT · ${title(event.subtype || event.terminal_reason || 'complete')}\n${result}${usage ? `\n${usage}${cost}` : cost}`;
  }
  if (type === 'rate_limit_event') {
    const info = event.rate_limit_info || {};
    return `RATE LIMIT · ${title(info.status || 'update')}\n${title(info.rateLimitType || 'allowance')}${info.resetsAt ? ` · resets ${relative(new Date(info.resetsAt * 1000))}` : ''}`;
  }
  if (type === 'thread.started') return `SESSION STARTED\n${event.thread_id || ''}`.trim();
  if (type === 'turn.started') return 'TURN STARTED';
  if (type === 'system') return `SYSTEM · ${title(event.subtype || 'event')}`;
  return `${title(type)}${event.subtype ? ` · ${title(event.subtype)}` : ''}`;
}
function formatLogContent(content) {
  if (!content) return '(empty log)';
  return content.split(/\r?\n/).filter(line => line.trim()).map(formattedLogLine).join('\n\n');
}
function renderLogContent() {
  $('#log-content').textContent = currentLogView === 'raw'
    ? (currentLogContent || '(empty log)')
    : formatLogContent(currentLogContent);
}
async function loadLog(stream) {
  const output = $('#log-content'); output.textContent = 'Loading…';
  try {
    const response = await fetch(`/v1/runs/${encodeURIComponent(currentRun)}/logs?stream=${stream}&tail_bytes=100000`), data = await response.json();
    if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
    currentLogContent = data.content || '';
    renderLogContent();
  } catch (error) { output.textContent = `Log unavailable: ${error.message}`; }
}
function durationInput(nanos) {
  if (!nanos) return '';
  const hours = nanos / 3.6e12;
  if (hours >= 24 && hours % 24 === 0) return `${hours/24}d`;
  if (hours >= 1) return `${hours}h`;
  return `${nanos/6e10}m`;
}
async function apiRequest(path,options={}) {
  const response = await fetch(path,{...options,headers:{Accept:'application/json','Content-Type':'application/json',...(options.headers || {})}});
  if (response.status === 204) return null;
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}
async function loadTaskTemplates() {
  if (!taskTemplates.length) taskTemplates = await apiRequest('/v1/task-templates');
  return taskTemplates;
}
async function loadProfiles(force=false) {
  if (force || !profiles.length) profiles = await apiRequest('/v1/profiles');
  $('#task-profile').innerHTML = profiles.map(profile => `<option value="${escapeHTML(profile.id)}">${escapeHTML(profile.id)} · ${escapeHTML(profile.provider_account_id)}${profile.model ? ` · ${escapeHTML(profile.model)}` : ''}</option>`).join('');
}
async function loadProfileOptions(force=false) {
  const catalog = await apiRequest(`/v1/profile-options${force ? '?refresh=true' : ''}`);
  harnessCatalog = catalog.harnesses || [];
  const installed = harnessCatalog.filter(harness => harness.installed && harness.id !== 'command').length;
  $('#profile-discovery-status').textContent = `${installed} agent CLI${installed === 1 ? '' : 's'} found · checked ${relative(catalog.generated_at)}`;
}
async function loadRuntimeConfiguration() {
  [runtimeConnections,agentContexts] = await Promise.all([
    apiRequest('/v1/runtime-connections'), apiRequest('/v1/agent-contexts')
  ]);
  runtimeConnections ||= []; agentContexts ||= [];
  $('#profile-runtime-connection').innerHTML = runtimeConnections.length
    ? runtimeConnections.filter(item => item.runtime === 'hermes').map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.id)} · ${escapeHTML(item.url || item.transport)}</option>`).join('')
    : '<option value="">No Hermes connections configured</option>';
  $('#edit-runtime-connection').disabled = !$('#profile-runtime-connection').value;
}
function selectedHermesProfileOptions() {
  const profile = $('#profile-runtime-profile').value;
  return (hermesDiscovery?.profile_options || []).find(item => item.profile?.name === profile);
}
function renderHermesProjects(selected='') {
  const options = selectedHermesProfileOptions(), profile = options?.profile;
  const projects = options?.projects || [];
  const choices = [{id:'',name:'Profile default',path:profile?.path || ''}, ...projects.map(project => ({
    id:project.id || project.name,name:project.name || project.id,
    path:project.primary_path || project.folders?.[0] || ''
  }))];
  $('#profile-runtime-project').innerHTML = choices.map(item => `<option value="${escapeHTML(item.id)}" data-path="${escapeHTML(item.path)}">${escapeHTML(item.name)}${item.path ? ` · ${escapeHTML(item.path)}` : ''}</option>`).join('');
  $('#profile-runtime-project').value = choices.some(item => item.id === selected) ? selected : '';
}
function installHermesModels() {
  const options = selectedHermesProfileOptions(), provider = providerKind();
  const matching = (options?.providers || []).filter(item => item.authenticated && (
    (provider === 'codex' && item.slug === 'openai-codex') ||
    (provider === 'claude' && item.slug === 'anthropic')
  ));
  const models = matching.flatMap(item => (item.models || []).map(model => ({
    id:`${item.slug}/${model}`,label:model,source:'hermes_gateway',thinking:!!item.capabilities?.[model]?.reasoning
  })));
  const harness = harnessCatalog.find(item => item.id === 'hermes');
  if (harness) harness.models = {...(harness.models || {}),[provider]:models};
}
async function discoverSelectedHermes(selectedProfile='',selectedProject='',selectedModel='default') {
  const connection = $('#profile-runtime-connection').value;
  if (!connection) {
    $('#profile-hermes-status').textContent = 'Import Hermes Desktop or configure a connection first.';
    return;
  }
  $('#profile-hermes-status').textContent = 'Connecting to Hermes Gateway…';
  const provider = providerKind(), providerSlug = provider === 'codex' ? 'openai-codex' : provider === 'claude' ? 'anthropic' : '';
  hermesDiscovery = await apiRequest(`/v1/runtime-connections/${encodeURIComponent(connection)}/discover`,{
    method:'POST',
    body:JSON.stringify({provider:providerSlug,include_models:true,model_limit:200})
  });
  $('#profile-runtime-profile').innerHTML = (hermesDiscovery.profiles || []).map(item =>
    `<option value="${escapeHTML(item.name)}">${escapeHTML(item.name)} · ${escapeHTML(item.provider || 'provider')} / ${escapeHTML(item.model || 'default')}</option>`
  ).join('');
  $('#profile-runtime-profile').value = (hermesDiscovery.profiles || []).some(item => item.name === selectedProfile) ? selectedProfile : hermesDiscovery.profiles?.[0]?.name || '';
  renderHermesProjects(selectedProject);
  installHermesModels();
  setModelControl('hermes',selectedModel);
  $('#profile-hermes-status').textContent = `Hermes ${hermesDiscovery.version || ''} · ${(hermesDiscovery.profiles || []).length} profile${(hermesDiscovery.profiles || []).length === 1 ? '' : 's'} discovered`;
}
async function importHermesDesktop() {
  const imports = (await apiRequest('/v1/runtime-connections/imports')) || [];
  if (!imports.length) throw new Error('Hermes Desktop does not have a remote Gateway configured.');
  const candidate = imports[0];
  if (!runtimeConnections.some(item => item.id === candidate.id)) {
    await apiRequest('/v1/runtime-connections',{method:'POST',body:JSON.stringify(candidate)});
  }
  await loadRuntimeConfiguration();
  $('#profile-runtime-connection').value = candidate.id;
  await discoverSelectedHermes();
}
function showRuntimeError(message) {
  $('#runtime-form-error').hidden = !message; $('#runtime-form-error').textContent = message || '';
}
function openRuntimeConnection(id='') {
  const item = runtimeConnections.find(connection => connection.id === id);
  editingRuntimeConnection = item?.id || '';
  $('#runtime-form').reset(); showRuntimeError('');
  $('#runtime-id').value = item?.id || ''; $('#runtime-id').disabled = !!item;
  $('#runtime-url').value = item?.url || '';
  $('#runtime-credential-source').value = item?.credential_source || '';
  $('#runtime-credential-ref').value = item?.credential_ref || '';
  $('#runtime-concurrency').value = item?.max_concurrent_runs || 1;
  $('#delete-runtime').hidden = !item;
  $('#runtime-dialog-title').textContent = item ? `Edit ${item.id}` : 'New Hermes connection';
  $('#runtime-dialog').showModal();
}
async function saveRuntimeConnection(event) {
  event.preventDefault(); showRuntimeError('');
  const payload = {
    id:$('#runtime-id').value.trim(),runtime:'hermes',transport:'gateway',
    url:$('#runtime-url').value.trim(),credential_source:$('#runtime-credential-source').value,
    credential_ref:$('#runtime-credential-ref').value.trim(),
    max_concurrent_runs:Number($('#runtime-concurrency').value || 1)
  };
  if (editingRuntimeConnection) delete payload.id;
  $('#save-runtime').disabled = true;
  try {
    const path=editingRuntimeConnection ? `/v1/runtime-connections/${encodeURIComponent(editingRuntimeConnection)}` : '/v1/runtime-connections';
    const saved=await apiRequest(path,{method:editingRuntimeConnection ? 'PATCH' : 'POST',body:JSON.stringify(payload)});
    await loadRuntimeConfiguration(); $('#profile-runtime-connection').value=saved.id;
    $('#runtime-dialog').close(); await discoverSelectedHermes();
  } catch(error) { showRuntimeError(error.message); } finally { $('#save-runtime').disabled=false; }
}
async function deleteRuntimeConnection() {
  if (!editingRuntimeConnection || !confirm(`Delete connection “${editingRuntimeConnection}”? Remove its execution profiles first.`)) return;
  try {
    await apiRequest(`/v1/runtime-connections/${encodeURIComponent(editingRuntimeConnection)}`,{method:'DELETE'});
    $('#runtime-dialog').close(); await loadRuntimeConfiguration(); hermesDiscovery=null;
    $('#profile-runtime-profile').innerHTML=''; $('#profile-runtime-project').innerHTML='';
    $('#profile-hermes-status').textContent='Choose or import a connection to discover profiles, projects, and models.';
  } catch(error) { showRuntimeError(error.message); }
}
function showTaskError(message) {
  $('#task-form-error').hidden = !message; $('#task-form-error').textContent = message || '';
}
async function updateTaskRuntimeJobs(selected='') {
  const profile=profiles.find(item => item.id === $('#task-profile').value);
  const field=$('#task-runtime-job-field'), select=$('#task-runtime-job'), status=$('#task-runtime-job-status');
  if (profile?.harness_type !== 'hermes') {
    field.hidden=true; select.innerHTML='<option value="">Not a Hermes profile</option>'; return;
  }
  field.hidden=false;
  const context=agentContexts.find(item => item.id === profile.agent_context_id);
  if (!context?.runtime_connection_id) {
    select.innerHTML='<option value="">New Hermes session from prompt</option>';
    status.textContent='This profile has no Hermes connection.';
    return;
  }
  status.textContent='Discovering existing Hermes jobs…';
  try {
    const jobs=await apiRequest(`/v1/runtime-connections/${encodeURIComponent(context.runtime_connection_id)}/jobs`);
    select.innerHTML='<option value="">New Hermes session from prompt</option>' + jobs.map(job => {
      const details=[job.provider,job.model,job.enabled ? '' : 'disabled'].filter(Boolean).join(' · ');
      return `<option value="${escapeHTML(job.id)}">${escapeHTML(job.name || job.id)}${details ? ` — ${escapeHTML(details)}` : ''}</option>`;
    }).join('');
    if (selected && !jobs.some(job => job.id === selected)) {
      select.innerHTML += `<option value="${escapeHTML(selected)}">${escapeHTML(selected)} — unavailable</option>`;
    }
    select.value=selected || '';
    status.textContent=`${jobs.length} existing job${jobs.length === 1 ? '' : 's'} available from ${context.runtime_connection_id}.`;
  } catch(error) {
    select.innerHTML='<option value="">New Hermes session from prompt</option>';
    status.textContent=`Could not load Hermes jobs: ${error.message}`;
  }
}
async function openTask(id='') {
  try {
    await Promise.all([loadProfiles(),loadTaskTemplates(),loadRuntimeConfiguration()]); showTaskError(''); $('#task-form').reset(); $('#task-id').value = id;
    let task = null;
    if (id) task = await apiRequest(`/v1/tasks/${encodeURIComponent(id)}`);
    $('#task-dialog-title').textContent = task ? 'Manage scheduled job' : 'New scheduled job';
    $('#task-template-field').hidden = !!task;
    $('#task-template').innerHTML = '<option value="">Blank job</option>' + taskTemplates.map(template => `<option value="${escapeHTML(template.id)}">${escapeHTML(template.name)}</option>`).join('');
    $('#task-template-description').textContent = 'Choose an editable starter prompt or begin from scratch.';
    $('#task-name').value = task?.name || '';
    $('#task-profile').value = task?.execution_profile_id || profiles[0]?.id || '';
    await updateTaskRuntimeJobs(task?.runtime_job_id || '');
    $('#task-priority').value = task?.priority ?? 50;
    $('#task-tier').value = task?.dispatch_tier || 'behind';
    $('#task-type').value = task?.type || 'one_off';
    $('#task-interval').value = durationInput(task?.min_interval || 0);
    $('#task-prompt').value = task?.prompt || '';
    $('#task-prompt-file').value = task?.prompt_file || '';
    $('#task-repo-change').checked = !!task?.require_repo_change;
    $('#delete-task').hidden = !task || task.state === 'running';
    $('#toggle-task').hidden = !task || task.state === 'running';
    $('#toggle-task').textContent = task?.enabled ? 'Disable' : 'Enable';
    $('#toggle-task').dataset.action = task?.enabled ? 'disable' : 'enable';
    $('#task-dialog').showModal();
  } catch (error) { $('#error-banner').hidden = false; $('#error-banner').textContent = `Could not open job: ${error.message}`; }
}
function applyTaskTemplate() {
  const template = taskTemplates.find(item => item.id === $('#task-template').value);
  if (!template) {
    $('#task-template-description').textContent = 'Choose an editable starter prompt or begin from scratch.';
    return;
  }
  $('#task-name').value = template.name || '';
  $('#task-priority').value = template.priority ?? 50;
  $('#task-tier').value = template.dispatch_tier || 'behind';
  $('#task-type').value = template.type || 'recurring';
  $('#task-interval').value = durationInput(template.min_interval || 0);
  $('#task-prompt').value = template.prompt || '';
  $('#task-prompt-file').value = '';
  $('#task-repo-change').checked = !!template.require_repo_change;
  const requirements = (template.requirements || []).join(' · ');
  $('#task-template-description').textContent = `${template.description || 'Editable starter prompt.'}${requirements ? ` Requires: ${requirements}.` : ''}`;
}
async function saveTask(event) {
  event.preventDefault(); showTaskError('');
  const id = $('#task-id').value, payload = {
    name:$('#task-name').value.trim(), execution_profile_id:$('#task-profile').value,
    runtime_job_id:$('#task-runtime-job-field').hidden ? '' : $('#task-runtime-job').value,
    priority:Number($('#task-priority').value), type:$('#task-type').value,
    dispatch_tier:$('#task-tier').value,
    min_interval:$('#task-interval').value.trim(), prompt:$('#task-prompt').value,
    prompt_file:$('#task-prompt-file').value.trim(), require_repo_change:$('#task-repo-change').checked
  };
  const save = $('#save-task'); save.disabled = true;
  try {
    await apiRequest(id ? `/v1/tasks/${encodeURIComponent(id)}` : '/v1/tasks',{method:id ? 'PATCH' : 'POST',body:JSON.stringify(payload)});
    $('#task-dialog').close(); await refresh();
  } catch (error) { showTaskError(error.message); } finally { save.disabled = false; }
}

const lines = (value) => String(value || '').split('\n').map(item => item.trim()).filter(Boolean);
function showProfileError(message) {
  $('#profile-form-error').hidden = !message; $('#profile-form-error').textContent = message || '';
}
function renderProfiles() {
  $('#profiles-list').innerHTML = profiles.length ? profiles.map(profile => `<button type="button" class="profile-card ${profile.id === editingProfile ? 'active' : ''}" data-profile="${escapeHTML(profile.id)}"><strong>${escapeHTML(profile.id)}</strong><span>${escapeHTML(profile.provider_account_id)} · ${escapeHTML(profile.model || profile.harness_type)}</span><small>${escapeHTML(title(profile.workspace_provider))}</small></button>`).join('') : '<p class="empty">No profiles yet.</p>';
  document.querySelectorAll('[data-profile]').forEach(button => button.addEventListener('click',() => editProfile(button.dataset.profile)));
}
function providerKind() {
  return providerCatalog.find(item => item.id === $('#profile-provider').value)?.provider || '';
}
function modelLabel(model) {
  const detail = [model.context_window ? `${model.context_window} context` : '', model.max_output ? `${model.max_output} output` : ''].filter(Boolean).join(' · ');
  const identity = model.label && model.label !== model.id ? `${model.label} — ${model.id}` : model.id;
  return detail ? `${identity} · ${detail}` : identity;
}
function suggestedModels(harness) {
  const discovered = harnessCatalog.find(item => item.id === harness)?.models?.[providerKind()] || [];
  const known = [{value:'default',label:'Default model (harness decides)'}, ...discovered.map(model => ({value:model.id,label:modelLabel(model)}))];
  const values = new Set(known.map(option => option.value));
  profiles.filter(profile => profile.harness_type === harness && profile.model && !values.has(profile.model)).forEach(profile => {
    values.add(profile.model); known.push({value:profile.model,label:`${profile.model} · previously used`});
  });
  return known;
}
function setHarnessControl(selected='') {
  const options = harnessCatalog.length ? harnessCatalog : [{id:'command',label:'Custom command',installed:true}];
  const hasSelected = options.some(option => option.id === selected);
  $('#profile-harness').innerHTML = options.map(option => {
    const version = option.version ? ` · v${option.version}` : '', unavailable = !option.installed ? ' · not found' : '';
    return `<option value="${escapeHTML(option.id)}" ${!option.installed && option.id !== selected ? 'disabled' : ''}>${escapeHTML(option.label + version + unavailable)}</option>`;
  }).join('') + (!hasSelected && selected ? `<option value="${escapeHTML(selected)}">${escapeHTML(selected)} · unavailable</option>` : '');
  $('#profile-harness').value = selected || preferredHarness();
  if (!$('#profile-harness').value) $('#profile-harness').value = 'command';
}
function setModelControl(harness, selected='default') {
  const options = suggestedModels(harness), known = options.some(option => option.value === selected);
  $('#profile-model-choice').innerHTML = options.map(option => `<option value="${escapeHTML(option.value)}">${escapeHTML(option.label)}</option>`).join('') + '<option value="__other__">Other model…</option>';
  $('#profile-model-choice').value = known ? selected : '__other__';
  $('#profile-model-custom').hidden = known; $('#profile-model-custom').value = known ? '' : selected;
}
function selectedModel() {
  return $('#profile-model-choice').value === '__other__' ? $('#profile-model-custom').value.trim() : $('#profile-model-choice').value;
}
function populateRepositoryChoices(selected='') {
  const repositories = [...new Set(profiles.map(profile => profile.repository).filter(Boolean))];
  $('#profile-repository-recent').innerHTML = '<option value="">Recently used repositories…</option>' + repositories.map(repository => `<option value="${escapeHTML(repository)}">${escapeHTML(repository)}</option>`).join('');
  $('#profile-repository-recent').value = repositories.includes(selected) ? selected : '';
}
function preferredHarness() {
  const provider = providerCatalog.find(item => item.id === $('#profile-provider').value)?.provider;
  return provider === 'claude' ? 'claude-code' : provider === 'codex' ? 'codex-cli' : 'command';
}
function updateHarnessFields(selectedModelValue) {
  const harness = $('#profile-harness').value, custom = harness === 'command';
  $('#profile-hermes-fields').hidden = harness !== 'hermes';
  $('#profile-model-field').hidden = custom; $('#profile-command-field').hidden = !custom;
  setModelControl(harness, selectedModelValue || 'default');
}
function resetProfileForm() {
  editingProfile = ''; $('#profile-form').reset(); $('#profile-id').disabled = false; $('#profile-id').value = '';
  $('#profile-provider').innerHTML = providerAccounts.map(id => `<option value="${escapeHTML(id)}">${escapeHTML(id)}</option>`).join('');
  setHarnessControl(preferredHarness()); $('#profile-workspace').value = 'devx'; $('#profile-budget-group').value = ''; $('#profile-context-concurrency').value = 1;
  updateHarnessFields('default'); populateRepositoryChoices(); $('#delete-profile').hidden = true; showProfileError(''); renderProfiles();
}
async function openProfiles() {
  try { await Promise.all([loadProfiles(true),loadProfileOptions(),loadRuntimeConfiguration()]); resetProfileForm(); $('#profiles-dialog').showModal(); }
  catch (error) { $('#error-banner').hidden = false; $('#error-banner').textContent = `Could not load profiles: ${error.message}`; }
}
async function editProfile(id) {
  try {
    const profile = await apiRequest(`/v1/profiles/${encodeURIComponent(id)}`); editingProfile = id; showProfileError('');
    $('#profile-provider').innerHTML = providerAccounts.map(account => `<option value="${escapeHTML(account)}">${escapeHTML(account)}</option>`).join('');
    $('#profile-id').value = profile.id; $('#profile-id').disabled = true; $('#profile-provider').value = profile.provider_account_id;
    setHarnessControl(profile.harness_type || preferredHarness()); updateHarnessFields(profile.model || 'default'); $('#profile-budget-group').value = profile.budget_model_group || '';
    if (profile.harness_type === 'hermes') {
      const context = agentContexts.find(item => item.id === profile.agent_context_id);
      if (context) {
        $('#profile-runtime-connection').value = context.runtime_connection_id;
        $('#profile-session-mode').value = context.session_mode || 'isolated';
        $('#profile-context-concurrency').value = context.max_concurrent_runs || 1;
        await discoverSelectedHermes(context.profile,context.project,profile.model || 'default');
      }
    }
    $('#profile-workspace').value = profile.workspace_provider || 'devx'; $('#profile-repository').value = profile.repository || ''; $('#profile-base-branch').value = profile.base_branch || '';
    populateRepositoryChoices(profile.repository || '');
    $('#profile-cleanup').value = profile.cleanup_policy || ''; $('#profile-require-clean').checked = !!profile.require_clean;
    $('#profile-harness-command').value = profile.harness_command || ''; $('#profile-harness-args').value = (profile.harness_args || []).join('\n'); $('#profile-workspace-args').value = (profile.workspace_args || []).join('\n');
    $('#profile-prepare').value = profile.prepare_command || ''; $('#profile-finalize').value = profile.finalize_command || ''; $('#delete-profile').hidden = false; renderProfiles();
  } catch (error) { showProfileError(error.message); }
}
async function saveProfile(event) {
  event.preventDefault(); showProfileError('');
  $('#save-profile').disabled = true;
  try {
    const id=$('#profile-id').value.trim(), harness=$('#profile-harness').value;
    let agentContextID='', repository=$('#profile-repository').value.trim(), workspaceProvider=$('#profile-workspace').value;
    if (harness === 'hermes') {
      const existing=profiles.find(item => item.id === editingProfile), context=agentContexts.find(item => item.id === existing?.agent_context_id);
      agentContextID=context?.id || `${id}-context`;
      const projectOption=$('#profile-runtime-project').selectedOptions[0];
      repository=projectOption?.dataset.path || '';
      if (!repository) throw new Error('The selected Hermes context does not provide a working directory.');
      const contextPayload={id:agentContextID,runtime_connection_id:$('#profile-runtime-connection').value,profile:$('#profile-runtime-profile').value,project:$('#profile-runtime-project').value,working_directory:repository,session_mode:$('#profile-session-mode').value,max_concurrent_runs:Number($('#profile-context-concurrency').value || 1)};
      const savedContext=await apiRequest(context ? `/v1/agent-contexts/${encodeURIComponent(agentContextID)}` : '/v1/agent-contexts',{method:context ? 'PATCH' : 'POST',body:JSON.stringify(contextPayload)});
      const contextIndex=agentContexts.findIndex(item => item.id === savedContext.id);
      if (contextIndex >= 0) agentContexts[contextIndex]=savedContext; else agentContexts.push(savedContext);
      workspaceProvider='runtime-owned';
    }
    const payload = {id,provider_account_id:$('#profile-provider').value,agent_context_id:agentContextID,harness_type:harness,model:selectedModel(),budget_model_group:$('#profile-budget-group').value,workspace_provider:workspaceProvider,repository,base_branch:$('#profile-base-branch').value.trim(),cleanup_policy:$('#profile-cleanup').value,require_clean:$('#profile-require-clean').checked,harness_command:$('#profile-harness-command').value.trim(),harness_args:lines($('#profile-harness-args').value),workspace_args:lines($('#profile-workspace-args').value),prepare_command:harness === 'hermes' ? '' : $('#profile-prepare').value,finalize_command:harness === 'hermes' ? '' : $('#profile-finalize').value};
    if (editingProfile) delete payload.id;
    const saved = await apiRequest(editingProfile ? `/v1/profiles/${encodeURIComponent(editingProfile)}` : '/v1/profiles',{method:editingProfile ? 'PATCH' : 'POST',body:JSON.stringify(payload)});
    await loadProfiles(true); await editProfile(saved.id); await refresh();
  } catch (error) { showProfileError(error.message); } finally { $('#save-profile').disabled = false; }
}
async function deleteProfile() {
  if (!editingProfile || !confirm(`Delete profile “${editingProfile}”? Profiles assigned to jobs cannot be deleted.`)) return;
  try {
    const profile=profiles.find(item => item.id === editingProfile), contextID=profile?.agent_context_id || '';
    await apiRequest(`/v1/profiles/${encodeURIComponent(editingProfile)}`,{method:'DELETE'});
    if (contextID) {
      await apiRequest(`/v1/agent-contexts/${encodeURIComponent(contextID)}`,{method:'DELETE'});
      agentContexts=agentContexts.filter(item => item.id !== contextID);
    }
    await loadProfiles(true); resetProfileForm(); await refresh();
  }
  catch (error) { showProfileError(error.message); }
}
async function toggleTask() {
  const id = $('#task-id').value, action = $('#toggle-task').dataset.action;
  try { await apiRequest(`/v1/tasks/${encodeURIComponent(id)}/${action}`,{method:'POST',body:'{}'}); $('#task-dialog').close(); await refresh(); }
  catch (error) { showTaskError(error.message); }
}
async function deleteTask() {
  const id = $('#task-id').value;
  if (!confirm(`Delete “${$('#task-name').value}”? Jobs with run history cannot be deleted.`)) return;
  try { await apiRequest(`/v1/tasks/${encodeURIComponent(id)}`,{method:'DELETE'}); $('#task-dialog').close(); await refresh(); }
  catch (error) { showTaskError(error.message); }
}
async function refresh() {
  const button = $('#refresh'); button.disabled = true;
  try {
    const response = await fetch('/v1/dashboard',{headers:{Accept:'application/json'}}), data = await response.json();
    if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
    render(data);
  } catch (error) {
    $('#error-banner').hidden = false; $('#error-banner').textContent = `Dashboard refresh failed: ${error.message}`;
  } finally { button.disabled = false; }
}
function connectLive() {
  const stream = new EventSource('/v1/dashboard/events'), pill = $('#live-pill');
  stream.onopen = () => { pill.className = 'live-pill'; pill.innerHTML = '<i></i><span>live</span>'; };
  stream.addEventListener('dashboard', event => { try { render(JSON.parse(event.data)); } catch (_) {} });
  stream.onerror = () => { pill.className = 'live-pill offline'; pill.innerHTML = '<i></i><span>reconnecting</span>'; };
}

$('#refresh').addEventListener('click',refresh);
$('#new-task').addEventListener('click',() => openTask());
$('#manage-profiles').addEventListener('click',openProfiles);
$('#task-form').addEventListener('submit',saveTask);
$('#task-profile').addEventListener('change',() => updateTaskRuntimeJobs());
$('#task-template').addEventListener('change',applyTaskTemplate);
$('#close-task').addEventListener('click',() => $('#task-dialog').close());
$('#cancel-task').addEventListener('click',() => $('#task-dialog').close());
$('#toggle-task').addEventListener('click',toggleTask);
$('#delete-task').addEventListener('click',deleteTask);
$('#profile-form').addEventListener('submit',saveProfile);
$('#new-profile').addEventListener('click',resetProfileForm);
$('#reset-profile').addEventListener('click',resetProfileForm);
$('#delete-profile').addEventListener('click',deleteProfile);
$('#close-profiles').addEventListener('click',() => $('#profiles-dialog').close());
$('#profile-provider').addEventListener('change',() => { const model=selectedModel(), harness=$('#profile-harness').value; if (!editingProfile && harness !== 'pi' && harness !== 'command') setHarnessControl(preferredHarness()); updateHarnessFields(editingProfile ? model : 'default'); });
$('#profile-harness').addEventListener('change',() => updateHarnessFields('default'));
$('#profile-runtime-connection').addEventListener('change',() => { $('#edit-runtime-connection').disabled=!$('#profile-runtime-connection').value; discoverSelectedHermes().catch(error => showProfileError(`Hermes discovery failed: ${error.message}`)); });
$('#profile-runtime-profile').addEventListener('change',() => { renderHermesProjects(); installHermesModels(); setModelControl('hermes','default'); });
$('#import-hermes-desktop').addEventListener('click',() => importHermesDesktop().catch(error => showProfileError(`Hermes import failed: ${error.message}`)));
$('#new-runtime-connection').addEventListener('click',() => openRuntimeConnection());
$('#edit-runtime-connection').addEventListener('click',() => openRuntimeConnection($('#profile-runtime-connection').value));
$('#runtime-form').addEventListener('submit',saveRuntimeConnection);
$('#delete-runtime').addEventListener('click',deleteRuntimeConnection);
$('#close-runtime').addEventListener('click',() => $('#runtime-dialog').close());
$('#cancel-runtime').addEventListener('click',() => $('#runtime-dialog').close());
$('#refresh-hermes-options').addEventListener('click',() => discoverSelectedHermes($('#profile-runtime-profile').value,$('#profile-runtime-project').value,selectedModel()).catch(error => showProfileError(`Hermes discovery failed: ${error.message}`)));
$('#refresh-profile-options').addEventListener('click',async () => { const button=$('#refresh-profile-options'), selected=$('#profile-harness').value, model=selectedModel(); button.disabled=true; try { await loadProfileOptions(true); setHarnessControl(selected); updateHarnessFields(model); } catch(error) { showProfileError(`Discovery failed: ${error.message}`); } finally { button.disabled=false; } });
$('#profile-model-choice').addEventListener('change',() => { $('#profile-model-custom').hidden = $('#profile-model-choice').value !== '__other__'; if (!$('#profile-model-custom').hidden) $('#profile-model-custom').focus(); });
$('#profile-repository-recent').addEventListener('change',() => { if ($('#profile-repository-recent').value) $('#profile-repository').value = $('#profile-repository-recent').value; });
$('#close-logs').addEventListener('click',() => $('#logs-dialog').close());
document.querySelectorAll('.log-tabs button[data-stream]').forEach(button => button.addEventListener('click',async () => { document.querySelectorAll('.log-tabs button[data-stream]').forEach(b => b.classList.remove('active')); button.classList.add('active'); await loadLog(button.dataset.stream); }));
document.querySelectorAll('[data-log-view]').forEach(button => button.addEventListener('click',() => { currentLogView=button.dataset.logView; document.querySelectorAll('[data-log-view]').forEach(b => b.classList.toggle('active',b === button)); renderLogContent(); }));
document.addEventListener('click',() => document.querySelectorAll('.provider-compact.open').forEach(card => card.classList.remove('open')));
document.addEventListener('keydown',event => { if (event.key === 'Escape') document.querySelectorAll('.provider-compact.open').forEach(card => card.classList.remove('open')); });
refresh(); connectLive();
