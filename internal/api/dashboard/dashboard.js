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

let currentRun = null, profiles = [];
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
  $('#tasks-body').innerHTML = tasks.length ? tasks.map(task => `<tr><td><span class="priority">P${task.priority}</span></td><td><span class="job-name">${escapeHTML(task.name)}</span><span class="subtle">${escapeHTML(task.id)}</span></td><td><span class="tag">${escapeHTML(task.provider_account_id)}</span><span class="tag">${escapeHTML(task.model || task.harness_type)}</span></td><td><span class="job-name">${escapeHTML(title(task.type))}</span><span class="subtle">${escapeHTML(duration(task.min_interval))}${task.require_repo_change ? ' · repo change required' : ''}</span></td><td><span class="status ${escapeHTML(task.state)}">${escapeHTML(task.state)}</span></td><td><button class="manage-button" type="button" data-task="${escapeHTML(task.id)}">Manage</button></td></tr>`).join('') : '<tr><td colspan="6" class="empty">No jobs are queued yet. Create one to start using spare capacity.</td></tr>';
  document.querySelectorAll('.manage-button').forEach(button => button.addEventListener('click',() => openTask(button.dataset.task)));
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
async function loadProfiles() {
  if (!profiles.length) profiles = await apiRequest('/v1/profiles');
  $('#task-profile').innerHTML = profiles.map(profile => `<option value="${escapeHTML(profile.id)}">${escapeHTML(profile.id)} · ${escapeHTML(profile.provider_account_id)}${profile.model ? ` · ${escapeHTML(profile.model)}` : ''}</option>`).join('');
}
function showTaskError(message) {
  $('#task-form-error').hidden = !message; $('#task-form-error').textContent = message || '';
}
async function openTask(id='') {
  try {
    await loadProfiles(); showTaskError(''); $('#task-form').reset(); $('#task-id').value = id;
    let task = null;
    if (id) task = await apiRequest(`/v1/tasks/${encodeURIComponent(id)}`);
    $('#task-dialog-title').textContent = task ? 'Manage scheduled job' : 'New scheduled job';
    $('#task-name').value = task?.name || '';
    $('#task-profile').value = task?.execution_profile_id || profiles[0]?.id || '';
    $('#task-priority').value = task?.priority ?? 50;
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
async function saveTask(event) {
  event.preventDefault(); showTaskError('');
  const id = $('#task-id').value, payload = {
    name:$('#task-name').value.trim(), execution_profile_id:$('#task-profile').value,
    priority:Number($('#task-priority').value), type:$('#task-type').value,
    min_interval:$('#task-interval').value.trim(), prompt:$('#task-prompt').value,
    prompt_file:$('#task-prompt-file').value.trim(), require_repo_change:$('#task-repo-change').checked
  };
  const save = $('#save-task'); save.disabled = true;
  try {
    await apiRequest(id ? `/v1/tasks/${encodeURIComponent(id)}` : '/v1/tasks',{method:id ? 'PATCH' : 'POST',body:JSON.stringify(payload)});
    $('#task-dialog').close(); await refresh();
  } catch (error) { showTaskError(error.message); } finally { save.disabled = false; }
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
$('#task-form').addEventListener('submit',saveTask);
$('#close-task').addEventListener('click',() => $('#task-dialog').close());
$('#cancel-task').addEventListener('click',() => $('#task-dialog').close());
$('#toggle-task').addEventListener('click',toggleTask);
$('#delete-task').addEventListener('click',deleteTask);
$('#close-logs').addEventListener('click',() => $('#logs-dialog').close());
document.querySelectorAll('.log-tabs button').forEach(button => button.addEventListener('click',async () => { document.querySelectorAll('.log-tabs button').forEach(b => b.classList.remove('active')); button.classList.add('active'); await loadLog(button.dataset.stream); }));
document.addEventListener('click',() => document.querySelectorAll('.provider-compact.open').forEach(card => card.classList.remove('open')));
document.addEventListener('keydown',event => { if (event.key === 'Escape') document.querySelectorAll('.provider-compact.open').forEach(card => card.classList.remove('open')); });
refresh(); connectLive();
