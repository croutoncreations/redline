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

let currentRun = null;
function meter(label, remaining, reset) {
  const value = percent(remaining), tone = value < 15 ? "danger" : value < 35 ? "warn" : "";
  return `<div><div class="meter-head"><span>${escapeHTML(label)}</span><b>${value}% left</b></div><div class="meter-track"><div class="meter-fill ${tone}" style="width:${value}%"></div></div><div class="reset">Resets ${escapeHTML(relative(reset))} · ${escapeHTML(shortTime(reset))}</div></div>`;
}
function providerCompact(item) {
  const snap = item.snapshot, provider = String(item.provider || item.id).toLowerCase();
  const icon = provider === 'claude' ? 'claude.svg' : 'codex.svg';
  const weekly = snap?.weekly, value = weekly ? percent(weekly.remaining) : 0, tone = value < 35 ? 'warn' : '';
  let details = `<p class="no-data">${escapeHTML(item.error || 'Waiting for usage data.')}</p>`;
  if (snap) {
    const windows = [];
    if (snap.short) windows.push(meter('5-hour window',snap.short.remaining,snap.short.resets_at));
    windows.push(meter('Weekly allowance',snap.weekly.remaining,snap.weekly.resets_at));
    (snap.allowances || []).filter(window => window.scope === 'model').forEach(window => windows.push(meter(window.source_label || title(window.key),window.remaining,window.resets_at)));
    details = `<div class="meters">${windows.join('')}</div>`;
  }
  return `<div class="provider-compact" tabindex="0" role="button" aria-label="Show ${escapeHTML(title(provider))} usage details"><span class="provider-logo ${escapeHTML(provider)}"><img src="/assets/${icon}" alt=""></span><span class="provider-summary"><span class="provider-copy-line"><strong>${escapeHTML(title(provider))}</strong><b>${weekly ? `${value}%` : '—'}</b></span><span class="compact-track"><span class="compact-fill ${tone}" style="width:${value}%"></span></span></span><span class="provider-detail"><span class="detail-head"><strong>${escapeHTML(title(provider))} capacity</strong><span>${snap ? `sampled ${escapeHTML(relative(snap.observed_at))}` : 'offline'}</span></span>${details}</span></div>`;
}
function wireProviderDetails() {
  document.querySelectorAll('.provider-compact').forEach(card => card.addEventListener('click', event => {
    event.stopPropagation();
    document.querySelectorAll('.provider-compact.open').forEach(open => { if (open !== card) open.classList.remove('open'); });
    card.classList.toggle('open');
  }));
}
function renderTasks(tasks) {
  $('#task-count').textContent = `${tasks.length} task${tasks.length === 1 ? '' : 's'}`;
  $('#tasks-body').innerHTML = tasks.length ? tasks.map(task => `<tr><td><span class="priority">P${task.priority}</span></td><td><span class="job-name">${escapeHTML(task.name)}</span><span class="subtle">${escapeHTML(task.id)}</span></td><td><span class="tag">${escapeHTML(task.provider_account_id)}</span><span class="tag">${escapeHTML(task.model || task.harness_type)}</span></td><td><span class="job-name">${escapeHTML(title(task.type))}</span><span class="subtle">${escapeHTML(duration(task.min_interval))}${task.require_repo_change ? ' · repo change required' : ''}</span></td><td><span class="status ${escapeHTML(task.state)}">${escapeHTML(task.state)}</span></td></tr>`).join('') : '<tr><td colspan="5" class="empty">No jobs are queued yet.</td></tr>';
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
  $('#usage-strip').innerHTML = data.providers.map(providerCompact).join(''); wireProviderDetails();
  $('#policy').textContent = data.active_policy || '—';
  $('#next-check').textContent = data.scheduler.next_cycle_at ? relative(data.scheduler.next_cycle_at) : data.scheduler.enabled ? 'starting' : 'disabled';
  $('#active-runs').textContent = data.health.active_runs;
  $('#updated-at').textContent = `Updated ${shortTime(data.generated_at)}`;
  renderHealth(data.health,data.attempts); renderTasks(data.tasks); renderRuns(data.runs); renderAttempts(data.attempts);
  $('#error-banner').hidden = true;
}
async function openLogs(run) {
  currentRun = run; $('#log-title').textContent = run; $('#logs-dialog').showModal();
  document.querySelectorAll('.log-tabs button').forEach(b => b.classList.toggle('active',b.dataset.stream === 'stdout'));
  await loadLog('stdout');
}
async function loadLog(stream) {
  const output = $('#log-content'); output.textContent = 'Loading…';
  try {
    const response = await fetch(`/v1/runs/${encodeURIComponent(currentRun)}/logs?stream=${stream}&tail_bytes=100000`), data = await response.json();
    if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
    output.textContent = data.content || '(empty log)';
  } catch (error) { output.textContent = `Log unavailable: ${error.message}`; }
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
$('#close-logs').addEventListener('click',() => $('#logs-dialog').close());
document.querySelectorAll('.log-tabs button').forEach(button => button.addEventListener('click',async () => { document.querySelectorAll('.log-tabs button').forEach(b => b.classList.remove('active')); button.classList.add('active'); await loadLog(button.dataset.stream); }));
document.addEventListener('click',() => document.querySelectorAll('.provider-compact.open').forEach(card => card.classList.remove('open')));
document.addEventListener('keydown',event => { if (event.key === 'Escape') document.querySelectorAll('.provider-compact.open').forEach(card => card.classList.remove('open')); });
refresh(); connectLive();
