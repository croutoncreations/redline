const $ = (selector) => document.querySelector(selector);
const escapeHTML = (value = "") => String(value).replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const relative = (value) => {
  if (!value) return "—";
  const seconds = Math.round((new Date(value) - Date.now()) / 1000);
  const abs = Math.abs(seconds);
  const [amount, unit] = abs < 60 ? [abs, "sec"] : abs < 3600 ? [Math.round(abs/60), "min"] : abs < 86400 ? [Math.round(abs/3600), "hr"] : [Math.round(abs/86400), "day"];
  return seconds >= 0 ? `in ${amount} ${unit}${amount === 1 ? "" : "s"}` : `${amount} ${unit}${amount === 1 ? "" : "s"} ago`;
};
const shortTime = (value) => value ? new Intl.DateTimeFormat(undefined,{month:'short',day:'numeric',hour:'numeric',minute:'2-digit'}).format(new Date(value)) : "—";
const duration = (nanos) => {
  if (!nanos) return "Always eligible";
  const hours = nanos / 3.6e12;
  if (hours >= 24 && hours % 24 === 0) return `${hours/24}d minimum`;
  if (hours >= 1) return `${Math.round(hours)}h minimum`;
  return `${Math.round(nanos/6e10)}m minimum`;
};
const title = (value) => String(value || "").replace(/[_-]/g," ").replace(/\b\w/g,c=>c.toUpperCase());

let currentRun = null;
function meter(label, remaining, reset) {
  const percent = Math.max(0, Math.min(100, Math.round((remaining || 0) * 100)));
  const tone = percent < 15 ? "danger" : percent < 35 ? "warn" : "";
  return `<div><div class="meter-head"><span>${escapeHTML(label)}</span><b>${percent}% left</b></div><div class="meter-track"><div class="meter-fill ${tone}" style="width:${percent}%"></div></div><div class="reset">Resets ${escapeHTML(relative(reset))} · ${escapeHTML(shortTime(reset))}</div></div>`;
}
function providerCard(item) {
  const snap = item.snapshot;
  const initial = (item.provider || item.id).slice(0,2);
  let body = `<p class="no-data">${escapeHTML(item.error || "Waiting for usage data.")}</p>`;
  if (snap) {
    const windows = [];
    if (snap.short) windows.push(meter("5-hour window", snap.short.remaining, snap.short.resets_at));
    windows.push(meter("Weekly allowance", snap.weekly.remaining, snap.weekly.resets_at));
    (snap.allowances || []).filter(window => window.scope === 'model').forEach(window => windows.push(meter(window.source_label || title(window.key), window.remaining, window.resets_at)));
    body = `<div class="meters">${windows.join("")}</div>`;
  }
  return `<article class="usage-card"><div class="provider-head"><div class="provider-name"><span class="provider-icon">${escapeHTML(initial)}</span><div><h2>${escapeHTML(title(item.provider))}</h2><p>${escapeHTML(item.id)}</p></div></div><span class="freshness">${snap ? `sampled ${escapeHTML(relative(snap.observed_at))}` : "offline"}</span></div>${body}</article>`;
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
async function openLogs(run) {
  currentRun = run; $('#log-title').textContent = run; $('#logs-dialog').showModal();
  document.querySelectorAll('.log-tabs button').forEach(b => b.classList.toggle('active', b.dataset.stream === 'stdout'));
  await loadLog('stdout');
}
async function loadLog(stream) {
  const output = $('#log-content'); output.textContent = 'Loading…';
  try {
    const response = await fetch(`/v1/runs/${encodeURIComponent(currentRun)}/logs?stream=${stream}&tail_bytes=100000`);
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
    output.textContent = data.content || '(empty log)';
  } catch (error) { output.textContent = `Log unavailable: ${error.message}`; }
}
async function refresh() {
  const button = $('#refresh'); button.disabled = true; $('#error-banner').hidden = true;
  try {
    const response = await fetch('/v1/dashboard', {headers:{Accept:'application/json'}});
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
    $('#usage-grid').innerHTML = data.providers.map(providerCard).join('');
    $('#policy').textContent = data.active_policy || '—';
    $('#next-check').textContent = data.scheduler.next_cycle_at ? relative(data.scheduler.next_cycle_at) : data.scheduler.enabled ? 'starting' : 'disabled';
    $('#active-runs').textContent = data.health.active_runs;
    $('#updated-at').textContent = `Synced ${shortTime(data.generated_at)}`;
    const healthy = data.health.status === 'ok' || data.health.status === 'healthy';
    $('#health-pill').className = `pill ${healthy ? '' : 'bad'}`; $('#health-pill').innerHTML = `<i></i>${escapeHTML(data.health.status)}`;
    renderTasks(data.tasks); renderRuns(data.runs); renderAttempts(data.attempts);
  } catch (error) {
    $('#error-banner').hidden = false; $('#error-banner').textContent = `Dashboard refresh failed: ${error.message}`;
    $('#health-pill').className = 'pill bad'; $('#health-pill').innerHTML = '<i></i> unavailable';
  } finally { button.disabled = false; }
}

$('#refresh').addEventListener('click', refresh);
$('#close-logs').addEventListener('click', () => $('#logs-dialog').close());
document.querySelectorAll('.log-tabs button').forEach(button => button.addEventListener('click', async () => { document.querySelectorAll('.log-tabs button').forEach(b => b.classList.remove('active')); button.classList.add('active'); await loadLog(button.dataset.stream); }));
refresh(); setInterval(refresh, 30000);
