// Redline mobile PWA — Phase 4 mobile dashboard
'use strict';

// ── Utilities ────────────────────────────────────────────────────────────────
const $ = (sel) => document.querySelector(sel);
const esc = (v = '') => String(v).replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const title = (v) => String(v || '').replace(/[_-]/g, ' ').replace(/\b\w/g, c => c.toUpperCase());
const pct = (r) => Math.max(0, Math.min(100, Math.round((r || 0) * 100)));

function relative(value) {
  if (!value) return '—';
  const secs = Math.round((new Date(value) - Date.now()) / 1000), abs = Math.abs(secs);
  const [n, u] = abs < 60 ? [abs,'sec'] : abs < 3600 ? [Math.round(abs/60),'min'] : abs < 86400 ? [Math.round(abs/3600),'hr'] : [Math.round(abs/86400),'day'];
  return secs >= 0 ? `in ${n} ${u}${n===1?'':'s'}` : `${n} ${u}${n===1?'':'s'} ago`;
}
function shortTime(value) {
  return value ? new Intl.DateTimeFormat(undefined, {month:'short',day:'numeric',hour:'numeric',minute:'2-digit'}).format(new Date(value)) : '—';
}

// One global 1-second clock (avoids multiple setIntervals).
const clockListeners = new Set();
setInterval(() => clockListeners.forEach(fn => fn()), 1000);

// ── State ────────────────────────────────────────────────────────────────────
let dashboard = null;
let taskNames = new Map();
let providerMap = new Map(); // id → dashboardProvider
let activeTab = 'usage';
let selectedQueueProvider = '';
let candidatesCache = new Map(); // provider → candidatesResponse
let openProviderIDs = new Set(); // independently collapsible usage cards
let usageInitialized = false;
let unreadRuns = 0;
let confirmPending = null; // {providerID, taskID, taskName}
let actionSheetCloser = null;
let currentRunID = null; // for detail view

// ── API ───────────────────────────────────────────────────────────────────────
async function apiFetch(path, opts = {}) {
  const resp = await fetch(path, {headers: {Accept: 'application/json', ...opts.headers}, ...opts});
  const data = await resp.json();
  if (!resp.ok) {
    const error = new Error(data.error || `HTTP ${resp.status}`);
    error.status = resp.status;
    throw error;
  }
  return data;
}

// ── SVG Ring (remaining arc) ──────────────────────────────────────────────────
// Renders an inline SVG progress ring. `remaining` ∈ [0,1]; falls back to weekly if short unavailable.
function ring(remaining, size = 40, stale = false) {
  const missing = remaining == null;
  const r = size > 50 ? 23 : 15, cx = size / 2, cy = size / 2;
  const circumference = 2 * Math.PI * r;
  const filled = stale || missing ? 0 : Math.max(0, Math.min(1, remaining));
  const dash = filled * circumference;
  const value = pct(filled);
  const tone = stale || missing ? '#91989e' : value < 15 ? '#ff7b72' : value < 35 ? '#efbd62' : '#72d9a4';
  const label = missing ? 'allowance unavailable' : stale ? 'stale' : value + '% remaining';
  return `<svg class="m-ring" data-testid="ring-svg" width="${size}" height="${size}" viewBox="0 0 ${size} ${size}" aria-label="${label}" role="img">
    <circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="#30363c" stroke-width="3.5"/>
    <circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="${esc(tone)}" stroke-width="3.5"
      stroke-dasharray="${dash.toFixed(2)} ${circumference.toFixed(2)}"
      stroke-linecap="round" transform="rotate(-90 ${cx} ${cy})"
      data-remaining="${filled}"/>
    <text x="${cx}" y="${cy + 4}" text-anchor="middle" fill="${esc(tone)}"
      font-size="9" font-family="ui-monospace,monospace" font-weight="600">${missing ? '—' : stale ? '?' : value}</text>
  </svg>`;
}

// ── Provider pressure label ───────────────────────────────────────────────────
function providerPressure(item) {
  if (item.paused) return {label: 'Paused', tone: 'paused'};
  if (item.snapshot_stale) return {label: 'Stale', tone: 'near'};
  if (item.error && !item.snapshot) return {label: 'No data', tone: 'paused'};
  const d = item.latest_decision;
  if (!d) return {label: 'Evaluating', tone: 'neutral'};
  if (d.decision === 'RUN') return {label: 'Run now', tone: 'triggered'};
  const points = Math.max(0, Math.ceil((d.pace_gap || 0) * 100));
  if (d.projected_trigger_at && new Date(d.projected_trigger_at) > Date.now()) {
    return {label: `Eligible ${relative(d.projected_trigger_at)}`, tone: 'neutral'};
  }
  if (points > 0) return {label: `${points}% surplus`, tone: points >= 15 ? 'near' : 'neutral'};
  return {label: 'Watching', tone: 'neutral'};
}

// ── Usage tab ────────────────────────────────────────────────────────────────
function renderUsageProvider(item) {
  const stale = Boolean(item.snapshot_stale);
  const errState = !item.snapshot && !stale;
  const paused = item.paused;
  const snap = item.snapshot;
  const provider = String(item.provider || item.id).toLowerCase();
  const icon = provider === 'claude' ? '/assets/claude.svg' : provider === 'codex' ? '/assets/codex.svg' : '';
  const accountAllowances = (snap?.allowances || []).filter(a => a.scope === 'account');
  const session = accountAllowances.find(a => a.role === 'session' || a.role === 'short') || snap?.short || null;
  const weekly = accountAllowances.find(a => a.role === 'weekly') || snap?.weekly || null;
  const pressure = providerPressure(item);

  const cardClass = ['m-provider-card', stale ? 'stale' : '', paused ? 'paused' : '', errState ? 'error-state' : ''].filter(Boolean).join(' ');
  const isOpen = openProviderIDs.has(item.id);

  let detailHTML = '';
  if (isOpen) {
    detailHTML = renderUsageDetail(item);
  }

  return `<div class="${esc(cardClass)}" data-provider-id="${esc(item.id)}" data-testid="provider-card">
    <button class="m-provider-header" type="button"
            aria-expanded="${isOpen}"
            aria-controls="m-provider-detail-${esc(item.id)}"
            data-provider-toggle="${esc(item.id)}"
            aria-label="Show ${esc(title(provider))} usage details">
      <span class="m-provider-logo ${esc(provider)}">${icon
        ? `<img src="${esc(icon)}" alt="${esc(title(provider))} logo" width="18" height="18">`
        : `<span class="m-provider-fallback" aria-hidden="true">R</span>`
      }</span>
      <span class="m-provider-summary">
        <span class="m-provider-name">${esc(title(provider))}</span>
        <span class="m-provider-status ${esc(pressure.tone)}">${esc(pressure.label)}</span>
      </span>
      <span class="m-rings" aria-label="Account allowances">
        <span class="m-ring-cell">${ring(session?.remaining ?? null, 58, stale)}<small>Session</small><em>${session ? relative(session.resets_at) : 'not available'}</em></span>
        <span class="m-ring-cell">${ring(weekly?.remaining ?? null, 58, stale)}<small>Weekly</small><em>${weekly ? `${weekly.reset_inferred ? '~ ' : ''}${relative(weekly.resets_at)}` : 'not available'}</em></span>
      </span>
    </button>
    <div id="m-provider-detail-${esc(item.id)}" class="m-provider-detail" ${isOpen ? '' : 'hidden'} data-testid="provider-detail-${esc(item.id)}">
      ${detailHTML}
    </div>
  </div>`;
}

function renderUsageDetail(item) {
  const snap = item.snapshot;
  const stale = Boolean(item.snapshot_stale);
  const paused = item.paused;
  const provider = String(item.provider || item.id).toLowerCase();
  const pressure = providerPressure(item);

  let metersHTML = '';
  if (snap) {
    const lastKnown = stale ? 'Last known ' : '';
    const windows = [];
    const accountPools = (snap.allowances || []).filter(a => a.scope === 'account');
    // The canonical `session` and `weekly` pools are the same data as
    // snap.short/snap.weekly — collectors populate both from a single source
    // line — so render them once here and skip them in the generic list below.
    const shortWindow = snap.short || accountPools.find(a => a.key === 'session') || null;
    const weeklyWindow = snap.weekly || accountPools.find(a => a.key === 'weekly') || null;
    if (shortWindow) {
      const v = pct(shortWindow.remaining), tone = v < 15 ? 'danger' : v < 35 ? 'warn' : '';
      windows.push(`<div>
        <div class="m-meter-head"><span>${esc(lastKnown)}5-hour window</span><b>${v}% left</b></div>
        <progress class="m-meter-bar ${esc(tone)}" max="100" value="${v}" aria-label="${v}% remaining"></progress>
        <div class="m-reset">Resets ${esc(relative(shortWindow.resets_at))} · ${esc(shortTime(shortWindow.resets_at))}</div>
      </div>`);
    }
    if (weeklyWindow) {
      const v = pct(weeklyWindow.remaining), tone = v < 15 ? 'danger' : v < 35 ? 'warn' : '';
      windows.push(`<div>
        <div class="m-meter-head"><span>${esc(lastKnown)}Weekly allowance</span><b>${v}% left</b></div>
        <progress class="m-meter-bar ${esc(tone)}" max="100" value="${v}" aria-label="${v}% remaining"></progress>
        <div class="m-reset">Resets ${esc(relative(weeklyWindow.resets_at))} · ${esc(shortTime(weeklyWindow.resets_at))}</div>
      </div>`);
    }
    // Remaining account-level pools (extra allowances beyond session/weekly)
    accountPools.filter(a => a.key !== 'session' && a.key !== 'weekly').forEach(a => {
      const label = `${lastKnown}${esc(a.source_label || title(a.key))}${a.reset_inferred ? ' · reset inferred' : ''}`;
      const v = pct(a.remaining), tone = v < 15 ? 'danger' : v < 35 ? 'warn' : '';
      windows.push(`<div>
        <div class="m-meter-head"><span>${label}</span><b>${v}% left</b></div>
        <progress class="m-meter-bar ${esc(tone)}" max="100" value="${v}" aria-label="${v}% remaining"></progress>
        <div class="m-reset">Resets ${esc(relative(a.resets_at))}${a.reset_inferred ? ' (inferred)' : ''}</div>
      </div>`);
    });
    // Model-level pools (allowances with scope="model")
    (snap.allowances || []).filter(a => a.scope === 'model').forEach(a => {
      const label = `${lastKnown}${esc(a.source_label || title(a.key))}${a.reset_inferred ? ' · reset inferred' : ''}`;
      const v = pct(a.remaining), tone = v < 15 ? 'danger' : v < 35 ? 'warn' : '';
      windows.push(`<div>
        <div class="m-meter-head"><span>${label}</span><b>${v}% left</b></div>
        <progress class="m-meter-bar ${esc(tone)}" max="100" value="${v}" aria-label="${v}% remaining"></progress>
        <div class="m-reset">Resets ${esc(relative(a.resets_at))}${a.reset_inferred ? ' (inferred)' : ''}</div>
      </div>`);
    });
    metersHTML = `<div class="m-meters">${windows.join('')}</div>`;
  } else {
    metersHTML = `<p class="m-inline-muted">${esc(item.error || 'No usage data available.')}</p>`;
  }

  // Decision block
  const d = item.latest_decision;
  let decisionHTML = '';
  if (d) {
    const detail = d.projected_trigger_at && new Date(d.projected_trigger_at) > Date.now()
      ? `Likely eligible ${relative(d.projected_trigger_at)}.`
      : (d.reason || '');
    decisionHTML = `<details class="m-provider-decision ${esc(pressure.tone)}" data-testid="decision-detail">
      <summary><b>${esc(pressure.label)}</b></summary>
      <span>${esc(detail)}</span>
    </details>`;
  }

  // Pool concurrency
  let poolHTML = '';
  if (item.pool_concurrency && Object.keys(item.pool_concurrency).length > 0) {
    const rows = Object.entries(item.pool_concurrency).sort().map(([pool, limit]) =>
      `<div class="m-pool-row"><span>${esc(title(pool))}</span><b>${item.active_pool_claims?.[pool] || 0}/${limit}</b></div>`
    ).join('');
    poolHTML = `<div class="m-pools-label">POOLS</div>${rows}`;
  }

  const stateLabel = paused ? 'Paused' : `${item.active_runs || 0}/${item.max_concurrent_runs || 1} active`;

  return `
    ${decisionHTML}
    <div class="m-source-meta">
      ${esc(item.usage_source?.active || 'unknown')} source
      ${item.usage_source?.last_error ? `<span class="m-source-error">${esc(item.usage_source.last_error)}</span>` : ''}
      · ${esc(stateLabel)}
      ${snap ? ` · sampled ${esc(relative(snap.observed_at))}` : ''}
    </div>
    ${metersHTML}
    ${poolHTML}
    <div class="m-provider-actions">
      <button class="m-btn m-view-queue-btn" type="button"
              data-action="view-queue" data-provider="${esc(item.id)}"
              aria-label="View queue for ${esc(title(provider))}">View Queue</button>
      ${paused
        ? `<button class="m-btn" type="button" data-action="resume-provider" data-provider="${esc(item.id)}" aria-label="Resume ${esc(title(provider))}">Resume</button>`
        : `<button class="m-btn danger" type="button" data-action="pause-provider" data-provider="${esc(item.id)}" aria-label="Pause ${esc(title(provider))}">Pause</button>`
      }
    </div>
    <div class="m-refresh-usage">
      <button type="button" data-action="refresh-usage" data-provider="${esc(item.id)}"
              aria-label="Refresh usage for ${esc(title(provider))}">Refresh usage ↻</button>
    </div>
  `;
}

function renderUsage(providers) {
  const list = $('#m-usage-list');
  if (!providers || !providers.length) {
    list.innerHTML = '<p class="m-empty">No providers configured.</p>';
    return;
  }
  if (!usageInitialized) {
    openProviderIDs = new Set(providers.map(provider => provider.id));
    usageInitialized = true;
  }
  list.innerHTML = providers.map(renderUsageProvider).join('');
  // Wire provider toggle
  list.querySelectorAll('[data-provider-toggle]').forEach(btn => {
    btn.addEventListener('click', () => {
      const id = btn.dataset.providerToggle;
      if (openProviderIDs.has(id)) openProviderIDs.delete(id);
      else openProviderIDs.add(id);
      renderUsage(dashboard?.providers || []);
    });
  });
  // Wire actions in detail panels
  list.querySelectorAll('[data-action]').forEach(btn => wireProviderAction(btn));
}

function wireProviderAction(btn) {
  const action = btn.dataset.action, providerID = btn.dataset.provider;
  btn.addEventListener('click', async () => {
    if (action === 'view-queue') {
      selectedQueueProvider = providerID;
      switchTab('queue');
      return;
    }
    if (action === 'pause-provider' || action === 'resume-provider') {
      const control = action === 'pause-provider' ? 'pause' : 'resume';
      btn.disabled = true;
      try {
        await apiFetch(`/v1/providers/${encodeURIComponent(providerID)}/${control}`, {method: 'POST', body: '{}'});
        await refreshDashboard();
      } catch (err) {
        showError(`Could not ${control} provider: ${err.message}`);
        btn.disabled = false;
      }
      return;
    }
    if (action === 'refresh-usage') {
      btn.disabled = true;
      try {
        await apiFetch(`/v1/providers/${encodeURIComponent(providerID)}/refresh`, {method: 'POST', body: '{}'});
        await refreshDashboard();
      } catch (err) {
        showError(`Could not refresh usage: ${err.message}`);
        btn.disabled = false;
      }
    }
  });
}

// ── Queue tab ─────────────────────────────────────────────────────────────────
function renderQueueSelector(providers) {
  const sel = $('#m-queue-provider');
  const prev = sel.value;
  sel.innerHTML = (providers || []).map(p =>
    `<option value="${esc(p.id)}">${esc(p.id)} · ${esc(p.provider)}</option>`
  ).join('');
  // Restore or set initial selection
  if (prev && providers.some(p => p.id === prev)) {
    sel.value = prev;
  } else if (selectedQueueProvider && providers.some(p => p.id === selectedQueueProvider)) {
    sel.value = selectedQueueProvider;
  }
  selectedQueueProvider = sel.value;
}

async function loadCandidates(providerID) {
  $('#m-snapshot-meta').textContent = 'Loading…';
  $('#m-candidates-list').innerHTML = '';
  $('#m-next-up').hidden = true;
  $('#m-queue-empty').hidden = true;
  try {
    const data = await apiFetch(`/v1/providers/${encodeURIComponent(providerID)}/candidates`);
    candidatesCache.set(providerID, data);
    renderCandidates(data, providerID);
  } catch (err) {
    $('#m-snapshot-meta').textContent = `Error: ${err.message}`;
    $('#m-snapshot-meta').className = 'm-snapshot-meta stale';
  }
}

function renderCandidates(data, providerID) {
  const snapshotAt = data.snapshot_observed_at;
  const stale = Boolean(data.snapshot_stale);
  const ready = data.dispatch_available && !stale;
  const metaEl = $('#m-snapshot-meta');
  metaEl.className = `m-snapshot-meta${stale ? ' stale' : ''}`;
  if (snapshotAt) {
    metaEl.textContent = `Snapshot ${relative(snapshotAt)}${stale ? ' · stale' : ''}${ready ? '' : ` · ${data.provider_reason || 'not ready'}`}`;
  } else {
    metaEl.textContent = data.provider_reason || 'No snapshot';
  }

  const candidates = data.candidates || [];
  const selectedID = data.selected_task_id;
  const readyCount = candidates.filter(candidate => candidate.eligible).length;
  const blockedCount = candidates.length - readyCount;

  // Next Up banner
  const nextUp = $('#m-next-up');
  if (ready && selectedID) {
    const task = candidates.find(c => c.task_id === selectedID);
    nextUp.hidden = false;
    nextUp.innerHTML = `<div class="m-next-up-banner" data-testid="next-up">
      <div>
        <strong>${esc(task?.name || selectedID)}</strong>
        <span>Next up · P${task?.priority ?? '—'} · ${readyCount} ready · ${blockedCount} blocked</span>
      </div>
      <button class="m-btn primary" type="button"
              data-action="run-now" data-provider="${esc(providerID)}" data-task="${esc(selectedID)}"
              data-task-name="${esc(task?.name || selectedID)}"
              aria-label="Dispatch ${esc(task?.name || selectedID)}">Run</button>
    </div>`;
    nextUp.querySelector('[data-action="run-now"]').addEventListener('click', openRunConfirm);
  } else {
    nextUp.hidden = true;
  }

  if (!candidates.length) {
    $('#m-candidates-list').innerHTML = '';
    $('#m-queue-empty').hidden = false;
    return;
  }
  $('#m-queue-empty').hidden = true;

  $('#m-candidates-list').innerHTML = candidates.map(c => {
    const eligible = c.eligible;
    const cardClass = `m-candidate-card${eligible ? ' eligible' : ''}`;
    const reasonClass = `m-candidate-reason${eligible ? ' eligible' : ''}`;
    const reason = eligible
      ? (ready ? 'Eligible — will dispatch next' : `Eligible (provider not ready: ${data.provider_reason || 'unknown'})`)
      : (c.reason || 'Not eligible');
    return `<div class="${esc(cardClass)}" data-testid="candidate-${esc(c.task_id)}">
      <div class="m-candidate-header">
        <span class="m-candidate-priority">P${c.priority}</span>
        <div>
          <div class="m-candidate-name">${esc(c.name)}</div>
          <div class="m-candidate-sub">${esc(c.task_id)}</div>
        </div>
        <button class="m-btn candidate-run ${eligible ? 'primary' : 'muted'}" type="button"
                data-action="candidate-run"
                data-provider="${esc(providerID)}"
                data-task="${esc(c.task_id)}"
                data-task-name="${esc(c.name)}"
                aria-label="Dispatch ${esc(c.name)}">Run</button>
        <button class="m-btn m-overflow-button" type="button"
                data-action="candidate-overflow"
                data-provider="${esc(providerID)}"
                data-task="${esc(c.task_id)}"
                data-task-name="${esc(c.name)}"
                data-eligible="${eligible}"
                aria-label="Actions for ${esc(c.name)}"
                title="More actions">⋯</button>
      </div>
      <div class="${esc(reasonClass)}" data-testid="candidate-reason-${esc(c.task_id)}">${esc(reason)}</div>
    </div>`;
  }).join('');

  $('#m-candidates-list').querySelectorAll('[data-action="candidate-run"]').forEach(btn => {
    btn.addEventListener('click', openRunConfirm);
  });
  $('#m-candidates-list').querySelectorAll('[data-action="candidate-overflow"]').forEach(btn => {
    btn.addEventListener('click', () => openCandidateOverflow(btn));
  });
}

function openRunConfirm(event) {
  const btn = event.currentTarget;
  const providerID = btn.dataset.provider;
  const taskID = btn.dataset.task;
  const taskName = btn.dataset.taskName;
  confirmPending = {providerID, taskID, taskName};

  const provider = providerMap.get(providerID);
  $('#m-run-confirm-title').textContent = `Run "${taskName}"?`;
  $('#m-run-confirm-body').textContent = `Provider: ${providerID}`;
  $('#m-run-confirm').hidden = false;
}

function openCandidateOverflow(btn) {
  const providerID = btn.dataset.provider;
  const taskID = btn.dataset.task;
  const taskName = btn.dataset.taskName;
  const eligible = btn.dataset.eligible === 'true';

  openActionSheet(taskName, [
    eligible ? {label: 'Run now', action: () => {
      closeActionSheet();
      // simulate click via confirmPending
      confirmPending = {providerID, taskID, taskName};
      const body = `Provider: ${providerID}`;
      $('#m-run-confirm-title').textContent = `Run "${taskName}"?`;
      $('#m-run-confirm-body').textContent = body;
      $('#m-run-confirm').hidden = false;
    }} : null,
    {label: 'Enable', action: async () => { closeActionSheet(); await taskControl(taskID, 'enable'); }},
    {label: 'Disable', action: async () => { closeActionSheet(); await taskControl(taskID, 'disable'); }},
    {label: 'Retry', action: async () => { closeActionSheet(); await taskControl(taskID, 'retry'); }},
    {label: 'Cancel', cancel: true, action: closeActionSheet},
  ].filter(Boolean));
}

async function taskControl(taskID, control) {
  try {
    await apiFetch(`/v1/tasks/${encodeURIComponent(taskID)}/${control}`, {method: 'POST', body: '{}'});
    await refreshDashboard();
    if (selectedQueueProvider) await loadCandidates(selectedQueueProvider);
  } catch (err) {
    showError(`Could not ${control} task: ${err.message}`);
  }
}

function openActionSheet(label, items) {
  const sheet = $('#m-action-sheet');
  $('#m-action-sheet-title').textContent = label;
  const btns = document.createElement('div');
  items.forEach(item => {
    const b = document.createElement('button');
    b.type = 'button';
    b.textContent = item.label;
    if (item.cancel) b.className = 'cancel';
    if (item.danger) b.className = 'danger';
    b.addEventListener('click', item.action);
    btns.appendChild(b);
  });
  $('#m-action-sheet-buttons').innerHTML = '';
  $('#m-action-sheet-buttons').appendChild(btns);
  sheet.hidden = false;
  actionSheetCloser = closeActionSheet;
  sheet.addEventListener('click', handleActionSheetBackdrop);
}

function handleActionSheetBackdrop(e) {
  if (e.target === $('#m-action-sheet')) closeActionSheet();
}

function closeActionSheet() {
  const sheet = $('#m-action-sheet');
  sheet.hidden = true;
  sheet.removeEventListener('click', handleActionSheetBackdrop);
  actionSheetCloser = null;
}

// ── Runs tab ──────────────────────────────────────────────────────────────────
function renderRuns(runs, unread) {
  unreadRuns = unread || 0;
  const badge = $('#m-runs-badge');
  badge.hidden = unreadRuns === 0;
  badge.textContent = unreadRuns > 99 ? '99+' : String(unreadRuns);
  badge.setAttribute('aria-label', `${unreadRuns} unread runs`);

  const list = $('#m-runs-list');
  const empty = $('#m-runs-empty');

  if (!runs || !runs.length) {
    list.innerHTML = '';
    empty.hidden = false;
    return;
  }
  empty.hidden = true;
  list.innerHTML = runs.map(run => {
    const isUnread = (run.state === 'completed' || run.state === 'failed') && !run.activity_read_at;
    const taskName = taskNames.get(run.task_id) || run.task_id;
    const route = [run.actual_provider || run.provider_account_id, run.actual_model].filter(Boolean).join(' · ');
    const desc = run.summary || run.error || title(run.state);
    return `<button class="m-run-item${isUnread ? ' unread' : ''}" type="button"
                    data-run="${esc(run.id)}" data-testid="run-item-${esc(run.id)}"
                    aria-label="View run: ${esc(taskName)}">
      <span class="m-run-dot ${esc(run.state)}${isUnread ? ' unread-glow' : ''}" aria-hidden="true"></span>
      <span class="m-run-body">
        <span class="m-run-name">${esc(taskName)}</span>
        <span class="m-run-desc">${esc(desc)}</span>
        <span class="m-run-meta">${esc(route)} · ${esc(title(run.outcome || run.state))}</span>
      </span>
      <time class="m-run-time">${esc(relative(run.completed_at || run.started_at))}</time>
    </button>`;
  }).join('');

  list.querySelectorAll('[data-run]').forEach(btn =>
    btn.addEventListener('click', () => openRunDetail(btn.dataset.run))
  );
}

async function openRunDetail(runID) {
  currentRunID = runID;
  const run = (dashboard?.runs || []).find(r => r.id === runID);
  const taskName = run ? (taskNames.get(run.task_id) || run.task_id) : runID;

  // Show detail panel over list
  $('#m-run-detail').classList.add('visible');
  $('#m-runs-list').classList.add('m-runs-obscured');
  $('#m-runs-empty').classList.add('m-runs-obscured');
  $('#m-run-detail-title').textContent = taskName;

  const body = $('#m-run-detail-body');
  body.innerHTML = '<p class="m-inline-muted">Loading…</p>';

  // Mark read
  try {
    await apiFetch(`/v1/runs/${encodeURIComponent(runID)}/read`, {method: 'POST'});
    // Update unread count locally
    if (dashboard) {
      const r = dashboard.runs.find(x => x.id === runID);
      if (r) r.activity_read_at = new Date().toISOString();
      dashboard.unread_runs = Math.max(0, (dashboard.unread_runs || 0) - 1);
      renderRuns(dashboard.runs, dashboard.unread_runs);
    }
  } catch (_) {}

  // Load events
  try {
    const [events, logs] = await Promise.all([
      apiFetch(`/v1/runs/${encodeURIComponent(runID)}/events`).catch(() => []),
      apiFetch(`/v1/runs/${encodeURIComponent(runID)}/logs?stream=stderr`).catch(() => null),
    ]);
    renderRunDetail(run, events, logs?.content);
  } catch (err) {
    body.innerHTML = `<p class="m-inline-error">Could not load run data: ${esc(err.message)}</p>`;
  }
}

function renderRunDetail(run, events, stderr) {
  if (!run) {
    $('#m-run-detail-body').innerHTML = '<p class="m-inline-muted">Run not found in current data.</p>';
    return;
  }
  const taskName = taskNames.get(run.task_id) || run.task_id;
  const route = [run.actual_provider || run.provider_account_id, run.actual_model].filter(Boolean).join(' · ');
  const artifacts = (run.artifacts || []).filter(a => /^https?:\/\//i.test(a.url || ''))
    .map(a => `<a class="m-run-artifact" href="${esc(a.url)}" target="_blank" rel="noreferrer">${esc(a.label || title(a.type || 'link'))} ↗</a>`).join('');

  const eventsHTML = Array.isArray(events) && events.length
    ? `<div class="m-run-events"><div class="m-run-events-head">Events</div>${events.map(ev =>
        `<div class="m-run-event"><span class="m-run-event-time">${esc(shortTime(ev.occurred_at || ev.created_at))}</span><span>${esc(ev.type || ev.kind || title(ev.state || 'event'))}${ev.message ? `: ${esc(ev.message)}` : ''}</span></div>`
      ).join('')}</div>`
    : '';

  // stderr — no wrapping, horizontal scroll
  const stderrHTML = stderr
    ? `<div class="m-stderr-wrap"><div class="m-stderr-head">stderr tail</div><pre class="m-stderr-content" data-testid="stderr-content">${esc(stderr)}</pre></div>`
    : '';

  $('#m-run-detail-body').innerHTML = `
    ${run.summary ? `<p class="m-run-summary">${esc(run.summary)}</p>` : ''}
    <div class="m-run-info">${esc(run.id)} · ${esc(title(run.state))} · ${esc(route)}</div>
    <div class="m-run-info">${run.completed_at ? `Completed ${esc(shortTime(run.completed_at))}` : `Started ${esc(shortTime(run.started_at))}`}</div>
    ${artifacts ? `<div class="m-run-artifacts">${artifacts}</div>` : ''}
    ${eventsHTML}
    ${stderrHTML}
  `;
}

function closeRunDetail() {
  currentRunID = null;
  $('#m-run-detail').classList.remove('visible');
  $('#m-runs-list').classList.remove('m-runs-obscured');
  $('#m-runs-empty').classList.remove('m-runs-obscured');
}

// ── Tab switching ─────────────────────────────────────────────────────────────
function switchTab(name) {
  activeTab = name;
  document.querySelectorAll('.m-tab').forEach(btn => {
    const active = btn.dataset.tab === name;
    btn.classList.toggle('active', active);
    btn.setAttribute('aria-selected', String(active));
  });
  document.querySelectorAll('.m-panel').forEach(panel => {
    const active = panel.id === `m-${name}-panel`;
    panel.classList.toggle('active', active);
    panel.hidden = !active;
  });

  if (name === 'queue' && selectedQueueProvider) {
    loadCandidates(selectedQueueProvider);
  }
  if (name === 'runs') {
    apiFetch('/v1/runs/read-all', {method: 'POST', body: '{}'}).then(refreshDashboard).catch(err => {
      showError(`Could not mark runs read: ${err.message}`);
    });
  } else {
    closeRunDetail();
  }
}

// ── Dashboard data render ─────────────────────────────────────────────────────
function render(data) {
  dashboard = data;
  taskNames = new Map((data.tasks || []).map(t => [t.id, t.name]));
  providerMap = new Map((data.providers || []).map(p => [p.id, p]));

  if (activeTab === 'usage') renderUsage(data.providers || []);
  renderQueueSelector(data.providers || []);
  if (activeTab === 'queue' && selectedQueueProvider) {
    const cached = candidatesCache.get(selectedQueueProvider);
    if (cached) renderCandidates(cached, selectedQueueProvider);
  }
  renderRuns(data.runs || [], data.unread_runs || 0);
  renderHealth(data);

  // Update live pill updated time
  if (data.generated_at) {
    document.body.dataset.updatedAt = data.generated_at;
  }
}

function renderHealth(data) {
  const healthy = data.health?.status === 'ok' || data.health?.status === 'healthy';
  const pill = $('#m-health-pill');
  pill.className = `m-health-pill${healthy ? '' : ' bad'}`;
  pill.innerHTML = `<i aria-hidden="true"></i><span>${healthy ? 'healthy' : 'degraded'}</span>`;
  pill.setAttribute('aria-label', healthy ? 'Scheduler health: healthy' : 'Scheduler health: degraded');
}

// ── Error display ─────────────────────────────────────────────────────────────
function showError(msg) {
  const banner = $('#m-error-banner');
  banner.textContent = msg;
  banner.hidden = !msg;
}

// ── Session expiry ────────────────────────────────────────────────────────────
// The server slides the session cookie forward on every authenticated request,
// so this is the fallback for a genuinely lapsed device (unused past the TTL,
// cookie cleared, or the API token rotated). The cached PWA shell still loads,
// so without this the only symptom is a generic refresh error.
let sessionExpired = false;

function handleExpiredSession() {
  if (sessionExpired) return;
  sessionExpired = true;
  if (eventSource) { eventSource.close(); eventSource = null; }
  setLivePill('offline');
  // Drop the cached shell so a device that re-pairs later cannot render markup
  // cached under the previous session.
  if (navigator.serviceWorker && navigator.serviceWorker.controller) {
    navigator.serviceWorker.controller.postMessage({type: 'redline-clear-cache'});
  }
  showError('Session expired. Run `redline pair --qr` on your Mac and scan the code to pair this device again.');
}

// ── Dashboard refresh ─────────────────────────────────────────────────────────
async function refreshDashboard() {
  try {
    const data = await apiFetch('/v1/dashboard');
    sessionExpired = false;
    render(data);
    showError('');
  } catch (err) {
    if (err.status === 401) {
      handleExpiredSession();
      return;
    }
    showError(`Dashboard refresh failed: ${err.message}`);
  }
}

// ── SSE connection (one global EventSource) ───────────────────────────────────
let eventSource = null;
let sseConnected = false;

function setLivePill(state) {
  const pill = $('#m-live-pill');
  pill.className = `m-live-pill ${state}`;
  const labels = {live: 'Live', connecting: 'Connecting', offline: 'Reconnecting'};
  pill.innerHTML = `<i aria-hidden="true"></i><span>${labels[state] || state}</span>`;
  pill.setAttribute('aria-label', `Connection: ${labels[state] || state}`);
  const stale = $('#m-stale-banner');
  stale.hidden = state !== 'offline';
}

function connectSSE() {
  if (eventSource) { eventSource.close(); eventSource = null; }
  if (sessionExpired) return;
  setLivePill('connecting');
  const es = new EventSource('/v1/dashboard/events');
  eventSource = es;

  // Expose for tests
  if (typeof window !== 'undefined') {
    window.__redlineEventSources = window.__redlineEventSources || [];
    window.__redlineEventSources.push(es);
  }

  es.onopen = () => {
    sseConnected = true;
    sessionProbeDone = false;
    setLivePill('live');
  };
  es.addEventListener('dashboard', event => {
    try {
      const data = JSON.parse(event.data);
      render(data);
      setLivePill('live');
      showError('');
    } catch (_) {}
  });
  es.onerror = () => {
    sseConnected = false;
    setLivePill('offline');
    // EventSource never exposes the HTTP status, so a stream that dropped
    // because the session lapsed is indistinguishable from a network blip.
    // Probe with a real request: a 401 routes into handleExpiredSession(),
    // and anything else leaves the reconnecting state alone. Without this a
    // tab left open shows "Reconnecting" forever and never offers re-pairing.
    probeSessionAfterStreamFailure();
  };
}

let sessionProbePending = false;
let sessionProbeDone = false;

// EventSource reconnects on its own timer and fires onerror on every failed
// attempt, so probing on each one would add a second request per retry cycle
// against a server that is already down. One probe per disconnection answers
// the only question we have — did the session lapse — and connectSSE() resets
// this when a stream opens again.
function probeSessionAfterStreamFailure() {
  if (sessionProbePending || sessionProbeDone || sessionExpired) return;
  sessionProbePending = true;
  sessionProbeDone = true;
  apiFetch('/v1/dashboard').then(data => {
    // Only adopt the probe snapshot while the stream is still down; a
    // reconnected stream owns the view and may already have rendered newer data.
    if (!sseConnected) render(data);
  }).catch(err => {
    if (err.status === 401) handleExpiredSession();
  }).finally(() => {
    sessionProbePending = false;
  });
}

// Visibility API: close when hidden, force GET + reconnect when visible.
document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    if (eventSource) { eventSource.close(); eventSource = null; }
  } else {
    refreshDashboard().then(connectSSE);
  }
});

// ── Queue selector wiring ─────────────────────────────────────────────────────
$('#m-queue-refresh').addEventListener('click', async event => {
  if (!selectedQueueProvider) return;
  const button = event.currentTarget;
  button.disabled = true;
  try {
    await apiFetch(`/v1/providers/${encodeURIComponent(selectedQueueProvider)}/refresh`, {method: 'POST', body: '{}'});
    await refreshDashboard();
    await loadCandidates(selectedQueueProvider);
  } catch (err) {
    showError(`Could not refresh usage: ${err.message}`);
  } finally {
    button.disabled = false;
  }
});

$('#m-queue-provider').addEventListener('change', () => {
  selectedQueueProvider = $('#m-queue-provider').value;
  loadCandidates(selectedQueueProvider);
});

// ── Tab wiring ────────────────────────────────────────────────────────────────
document.querySelectorAll('.m-tab').forEach(btn => {
  btn.addEventListener('click', () => switchTab(btn.dataset.tab));
});

// ── Run confirm sheet ─────────────────────────────────────────────────────────
$('#m-run-confirm-cancel').addEventListener('click', () => {
  $('#m-run-confirm').hidden = true;
  confirmPending = null;
});
$('#m-run-confirm').addEventListener('click', e => {
  if (e.target === $('#m-run-confirm')) {
    $('#m-run-confirm').hidden = true;
    confirmPending = null;
  }
});
$('#m-run-confirm-ok').addEventListener('click', async () => {
  if (!confirmPending) return;
  const {providerID, taskID} = confirmPending;
  const btn = $('#m-run-confirm-ok');
  btn.disabled = true;
  try {
    const resp = await fetch(`/v1/tasks/${encodeURIComponent(taskID)}/dispatch`, {
      method: 'POST',
      headers: {Accept: 'application/json'},
    });
    const data = await resp.json();
    if (resp.status === 401) {
      handleExpiredSession();
    } else if (resp.status === 409) {
      showError(data.error || 'Provider is paused.');
    } else if (!resp.ok) {
      showError(data.error || `HTTP ${resp.status}`);
    } else if (resp.status === 202) {
      await refreshDashboard();
      switchTab('runs');
    } else {
      showError(data.result?.reason || 'Task was not admitted. Refreshing its eligibility.');
      await loadCandidates(providerID);
    }
  } catch (err) {
    showError(`Dispatch failed: ${err.message}`);
  } finally {
    btn.disabled = false;
    $('#m-run-confirm').hidden = true;
    confirmPending = null;
  }
});

// ── Run back button ───────────────────────────────────────────────────────────
$('#m-run-back').addEventListener('click', closeRunDetail);

// ── Service worker registration ───────────────────────────────────────────────
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}

// ── Initial load ──────────────────────────────────────────────────────────────
refreshDashboard().then(() => {
  // After first data, initialise queue selector
  const providers = dashboard?.providers || [];
  if (providers.length) selectedQueueProvider = providers[0].id;
  renderQueueSelector(providers);
  connectSSE();
});
