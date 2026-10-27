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
  let n, u;
  if (abs < 60) { n = abs; u = 'sec'; }
  else if (abs < 3600) { n = Math.round(abs/60); u = 'min'; if (n >= 60) { n = 1; u = 'hr'; } }
  else if (abs < 86400) { n = Math.round(abs/3600); u = 'hr'; if (n >= 24) { n = 1; u = 'day'; } }
  else { n = Math.round(abs/86400); u = 'day'; }
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
// Multi-account carousel: the page shown per provider family (0 = Overview),
// held by account id so a reorder or refresh keeps the same account.
let accountCarouselPage = new Map(); // provider → account id, or '' for Overview
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
// Stale because the provider's CLI signed out on the Mac -- the one stale
// cause a person fixes -- so it is named, with the command, not just "Stale".
function signedOut(item) {
  return Boolean(item.snapshot_stale && item.usage_source?.reason === 'signed_out');
}
// The Cursor CLI and the Cursor app are signed in to different accounts.
function accountMismatch(item) {
  return Boolean(item.snapshot_stale && item.usage_source?.reason === 'account_mismatch');
}
function loginCommand(provider) {
  return {claude: 'claude auth login', codex: 'codex login', cursor: 'agent login'}[String(provider || '').toLowerCase()] || '';
}
function isMonthly(snap) {
  return (snap?.weekly?.period_seconds || 0) > 8 * 86400;
}
// Cursor has no session window. Its tightest subscription pool is the one a
// queued job is most likely to hit, so the first ring shows that pool.
function cursorPoolRing(snap, stale) {
  const pools = (snap?.allowances || []).filter(a => a.scope === 'model' && a.role === 'monthly');
  const tightest = pools.reduce((low, pool) => (!low || pool.remaining < low.remaining ? pool : low), null);
  const label = tightest ? (tightest.source_label || title(tightest.key)).replace(/ Models$/, '') : 'Pool';
  return `<span class="m-ring-cell">${ring(tightest?.remaining ?? null, 58, stale)}<small>${esc(label)}</small><em>${tightest ? 'lowest pool' : 'not available'}</em></span>`;
}
function providerLabel(provider) {
  return {claude: 'Claude', codex: 'Codex', cursor: 'Cursor'}[provider] || title(provider);
}
function providerPressure(item) {
  if (item.paused) return {label: 'Paused', tone: 'paused'};
  if (accountMismatch(item)) return {label: 'Accounts differ', tone: 'near'};
  if (signedOut(item)) return {label: 'Signed out', tone: 'near'};
  if (item.snapshot_stale) return {label: 'Stale', tone: 'near'};
  if (item.error && !item.snapshot) return {label: 'No data', tone: 'paused'};
  if (item.snapshot?.weekly_unavailable && !['RUN', 'ADMIT'].includes(item.latest_decision?.decision)) return {label: 'Held · weekly n/r', tone: 'neutral'};
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
  // A source that reports no weekly limit (Team seats) has none to draw:
  // never a 0% ring, and never the 5-hour number standing in.
  const weekly = snap?.weekly_unavailable ? null : (accountAllowances.find(a => a.role === 'weekly') || snap?.weekly || null);
  // Cursor's subscription resets monthly and has no 5-hour session.
  const monthly = isMonthly(snap);
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
        : `<span class="m-provider-fallback" aria-hidden="true">${esc(providerLabel(provider).charAt(0))}</span>`
      }</span>
      <span class="m-provider-summary">
        <span class="m-provider-name">${esc(providerLabel(provider))}</span>
        <span class="m-provider-status ${esc(pressure.tone)}">${esc(pressure.label)}</span>
      </span>
      <span class="m-rings" aria-label="Account allowances">
        ${monthly ? cursorPoolRing(snap, stale) : `<span class="m-ring-cell">${ring(session?.remaining ?? (snap?.short_window_not_started ? 1 : null), 58, stale)}<small>Session</small><em>${session ? relative(session.resets_at) : snap?.short_window_not_started ? 'on first use' : 'not available'}</em></span>`}
        <span class="m-ring-cell">${ring(weekly?.remaining ?? null, 58, stale)}<small>${monthly ? 'Monthly' : 'Weekly'}</small><em>${weekly ? `${weekly.reset_inferred ? '~ ' : ''}${relative(weekly.resets_at)}` : snap?.weekly_unavailable ? 'not reported' : 'not available'}</em></span>
      </span>
    </button>
    <div id="m-provider-detail-${esc(item.id)}" class="m-provider-detail" ${isOpen ? '' : 'hidden'} data-testid="provider-detail-${esc(item.id)}">
      ${detailHTML}
    </div>
  </div>`;
}

// opts.accountPage: rendered as one account inside a multi-account carousel.
// Out-of-date numbers there read as last known with fixed reset times, never
// a countdown, and every credit deadline is listed.
function renderUsageDetail(item, opts = {}) {
  const outOfDate = Boolean(opts.accountPage) && accountState(item) !== 'ok';
  const snap = outOfDate && accountState(item) === 'signed_out' ? null : item.snapshot;
  const stale = Boolean(item.snapshot_stale);
  const resetLine = (window, {inferred = false, pool = false} = {}) => {
    if (!outOfDate) {
      // Exactly the lines this card has always shown: windows with their
      // clock time, pools with the inferred note.
      return pool
        ? `Resets ${relative(window.resets_at)}${inferred ? ' (inferred)' : ''}`
        : `Resets ${relative(window.resets_at)} · ${shortTime(window.resets_at)}`;
    }
    if (!window.resets_at) return 'Reset time unknown';
    const at = fixedTime(window.resets_at);
    return new Date(window.resets_at).getTime() <= Date.now()
      ? `Reset was due ${at} · awaiting fresh numbers`
      : `Last reported reset ${at}${inferred ? ' (estimated)' : ''}`;
  };
  const meter = (label, remaining, line) => {
    const v = pct(remaining), tone = v < 15 ? 'danger' : v < 35 ? 'warn' : '';
    const value = outOfDate ? `was ${v}% left` : `${v}% left`;
    // Out of date: no bar, which would read as a current level.
    const bar = outOfDate ? '' : `<progress class="m-meter-bar ${esc(tone)}" max="100" value="${v}" aria-label="${v}% remaining"></progress>`;
    return `<div${outOfDate ? ' class="m-acct-stale-window"' : ''}>
        <div class="m-meter-head"><span>${label}</span><b>${esc(value)}</b></div>
        ${bar}
        <div class="m-reset">${esc(line)}</div>
      </div>`;
  };
  const paused = item.paused;
  const provider = String(item.provider || item.id).toLowerCase();
  const pressure = providerPressure(item);

  let metersHTML = '';
  if (snap) {
    // On an account page "was" already says it; elsewhere the label does.
    const lastKnown = stale && !outOfDate ? 'Last known ' : '';
    const windows = [];
    const accountPools = (snap.allowances || []).filter(a => a.scope === 'account');
    // The canonical `session` and `weekly` pools are the same data as
    // snap.short/snap.weekly — collectors populate both from a single source
    // line — so render them once here and skip them in the generic list below.
    const shortWindow = snap.short || accountPools.find(a => a.key === 'session') || null;
    const weeklyWindow = snap.weekly_unavailable ? null : (snap.weekly || accountPools.find(a => a.key === 'weekly') || null);
    if (shortWindow) {
      windows.push(meter(`${esc(lastKnown)}5-hour window`, shortWindow.remaining, resetLine(shortWindow)));
    } else if (snap.short_window_not_started && outOfDate) {
      windows.push(`<div class="m-acct-stale-window" data-testid="short-window-not-started">
        <div class="m-meter-head"><span>5-hour window</span><b>was 100% left</b></div>
        <div class="m-reset">Not started when last read</div>
      </div>`);
    } else if (snap.short_window_not_started) {
      // Full, with no reset yet: the provider starts the clock on first use,
      // so say that instead of inventing a time.
      windows.push(`<div data-testid="short-window-not-started">
        <div class="m-meter-head"><span>${esc(lastKnown)}5-hour window</span><b>100% left</b></div>
        <progress class="m-meter-bar" max="100" value="100" aria-label="100% remaining"></progress>
        <div class="m-reset">Starts on first use · resets 5h after</div>
      </div>`);
    }
    if (weeklyWindow) {
      windows.push(meter(`${esc(lastKnown)}${isMonthly(snap) ? 'Monthly subscription' : 'Weekly allowance'}`, weeklyWindow.remaining, resetLine(weeklyWindow)));
    } else if (snap.weekly_unavailable) {
      windows.push(`<div data-testid="weekly-not-reported">
        <div class="m-meter-head"><span>Weekly allowance</span><b>Not reported</b></div>
        <div class="m-reset">Weekly limit not reported for this account · dispatch holds</div>
      </div>`);
    }
    // Remaining account-level pools (extra allowances beyond session/weekly)
    accountPools.filter(a => a.key !== 'session' && a.key !== 'weekly').forEach(a => {
      const label = `${lastKnown}${esc(a.source_label || title(a.key))}${a.reset_inferred ? ' · reset inferred' : ''}`;
      windows.push(meter(label, a.remaining, resetLine(a, {inferred: a.reset_inferred, pool: true})));
    });
    // Model-level pools (allowances with scope="model")
    (snap.allowances || []).filter(a => a.scope === 'model').forEach(a => {
      const label = `${lastKnown}${esc(a.source_label || title(a.key))}${a.reset_inferred ? ' · reset inferred' : ''}`;
      windows.push(meter(label, a.remaining, resetLine(a, {inferred: a.reset_inferred, pool: true})));
    });
    // Banked resets: on-demand refills. Absent means not reported, not zero.
    if (opts.accountPage && snap.banked_resets != null) {
      const credits = accountCredits(snap, {outOfDate});
      const soon = !outOfDate && credits.count > 0 && credits.soonest != null && credits.soonest - Date.now() < 3 * 86400000;
      const deadlines = credits.count > 0 && credits.deadlines.length
        ? `Credit deadlines: ${credits.deadlines.join(' · ')}`
        : credits.count > 0 ? 'Spend one to refill an exhausted limit' : 'None available right now';
      windows.push(`<div class="m-banked-resets${soon ? ' soon' : ''}" data-testid="banked-resets">
        <div class="m-meter-head"><span>Banked resets</span><b>${esc(credits.headline)}</b></div>
        <div class="m-reset">${esc(deadlines)}</div>
      </div>`);
    } else if (snap.banked_resets != null) {
      const count = snap.banked_resets;
      const expires = count > 0 && snap.banked_resets_expire_at && new Date(snap.banked_resets_expire_at) > Date.now() ? snap.banked_resets_expire_at : null;
      const soon = expires && new Date(expires) - Date.now() < 3 * 86400000;
      const detail = expires
        ? `${count === 1 ? 'Expires' : 'Next expires'} ${relative(expires)} · ${shortTime(expires)}`
        : count > 0 ? 'Spend one to refill an exhausted limit' : 'None available right now';
      windows.push(`<div class="m-banked-resets${soon ? ' soon' : ''}" data-testid="banked-resets">
        <div class="m-meter-head"><span>Banked resets</span><b>${esc(count)} available</b></div>
        <div class="m-reset">${esc(detail)}</div>
      </div>`);
    }
    metersHTML = `<div class="m-meters"${outOfDate ? ' data-testid="stale-meters"' : ''}>${windows.join('')}</div>`;
  } else {
    metersHTML = `<p class="m-inline-muted">${esc(item.error || 'No usage data available.')}</p>`;
  }

  // Decision block
  const d = item.latest_decision;
  let decisionHTML = '';
  if (d && outOfDate) {
    decisionHTML = '';
  } else if (d) {
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
  // Inside a carousel the buttons name the account, not just the provider.
  const who = opts.accountPage ? accountName(item) : title(provider);

  return `
    ${decisionHTML}
    <div class="m-source-meta">
      ${esc(item.usage_source?.active || 'unknown')} source
      ${accountMismatch(item)
        ? '<span class="m-source-error" data-testid="account-mismatch">Cursor CLI and app are on different accounts · sign both in to the same one on your Mac</span>'
        : signedOut(item)
        ? `<span class="m-source-error" data-testid="signed-out">${esc(loginCommand(item.provider) ? `Signed out on your Mac · run ${loginCommand(item.provider)}` : 'Signed out on your Mac · sign in again')}</span>`
        : item.usage_source?.last_error ? `<span class="m-source-error">${esc(item.usage_source.last_error)}</span>` : ''}
      · ${esc(stateLabel)}
      ${snap ? ` · sampled ${esc(relative(snap.observed_at))}` : ''}
    </div>
    ${metersHTML}
    ${poolHTML}
    <div class="m-provider-actions">
      <button class="m-btn m-view-queue-btn" type="button"
              data-action="view-queue" data-provider="${esc(item.id)}"
              aria-label="View queue for ${esc(who)}">View Queue</button>
      ${paused
        ? `<button class="m-btn" type="button" data-action="resume-provider" data-provider="${esc(item.id)}" aria-label="Resume ${esc(who)}">Resume</button>`
        : `<button class="m-btn danger" type="button" data-action="pause-provider" data-provider="${esc(item.id)}" aria-label="Pause ${esc(who)}">Pause</button>`
      }
    </div>
    <div class="m-refresh-usage">
      <button type="button" data-action="refresh-usage" data-provider="${esc(item.id)}"
              aria-label="Refresh usage for ${esc(who)}">Refresh usage ↻</button>
    </div>
  `;
}

// ── Cursor usage (display only) ───────────────────────────────────────────────
// Cursor's model pools, Grok Bot and on-demand spend are shown for reference.
// They are never scheduled, so this card has no Pause, Queue or Refresh controls.
function fullDate(value) {
  return new Intl.DateTimeFormat(undefined, {
    weekday: 'short', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', timeZoneName: 'short',
  }).format(new Date(value));
}
function money(value) {
  return new Intl.NumberFormat(undefined, {style: 'currency', currency: 'USD'}).format(value);
}
function amount(value, unit) {
  if (unit === 'usd') return money(value);
  if (unit === 'percent') return `${Math.round(value)}%`;
  const n = Number.isInteger(value) ? String(value) : value.toFixed(1);
  return unit ? `${n} ${unit}` : n;
}
// Out of date only once the source has missed its refresh: expires_at plus the
// same 2-minute grace the server uses (OpenUsage rotates a few seconds late).
const CURSOR_STALE_GRACE_MS = 2 * 60 * 1000;
function cursorStale(report) {
  if (!report?.fetched_at) return false;
  return !report.expires_at || new Date(report.expires_at).getTime() + CURSOR_STALE_GRACE_MS <= Date.now();
}
function cursorState(report) {
  const hasData = (report.meters || []).length || (report.balances || []).length;
  if (!hasData) return {label: 'No data', tone: 'paused'};
  if (cursorStale(report)) return {label: 'Out of date', tone: 'near'};
  if ((report.errors || []).length) return {label: 'Partial', tone: 'near'};
  return {label: 'Up to date', tone: 'healthy'};
}

function renderCursorMeter(meter, lastKnown) {
  const label = `${lastKnown ? 'Last known ' : ''}${meter.label}`;
  const reset = meter.resets_at
    ? `Resets ${relative(meter.resets_at)} · ${fullDate(meter.resets_at)}`
    : 'Reset date not reported';
  const limit = meter.limit > 0 ? meter.limit : null;
  if (limit == null) {
    // No denominator: report what was spent, never a bar or a percentage.
    const spent = meter.unit === 'usd' ? `${money(meter.used)} spent` : `${amount(meter.used, meter.unit)} used`;
    return `<div class="m-cursor-meter" data-testid="cursor-meter-${esc(meter.key)}">
      <div class="m-meter-head"><span>${esc(label)}</span><b>${esc(spent)}</b></div>
      <div class="m-reset">${esc(meter.resets_at ? reset : 'No limit reported')}</div>
    </div>`;
  }
  // What is left, like every provider meter: a full bar is plenty, and the
  // tone thresholds are the same 35% / 15% left.
  const leftPct = Math.max(0, Math.min(100, (limit - meter.used) / limit * 100));
  const exhausted = meter.used >= limit;
  const left = Math.round(leftPct);
  const tone = exhausted || left < 15 ? 'danger' : left < 35 ? 'warn' : '';
  const value = exhausted ? 'Used up'
    : meter.unit === 'percent' && limit === 100 ? `${left}% left`
    : `${amount(limit - meter.used, meter.unit)} of ${amount(limit, meter.unit)} left`;
  const detail = exhausted && meter.resets_at
    ? `Available again ${relative(meter.resets_at)} · ${fullDate(meter.resets_at)}`
    : reset;
  return `<div class="m-cursor-meter${exhausted ? ' exhausted' : ''}" data-testid="cursor-meter-${esc(meter.key)}">
    <div class="m-meter-head"><span>${esc(label)}</span><b>${esc(value)}</b></div>
    <progress class="m-meter-bar ${esc(tone)}" max="100" value="${leftPct.toFixed(1)}" aria-label="${esc(meter.label)} ${left}% remaining"></progress>
    <div class="m-reset">${esc(detail)}</div>
  </div>`;
}

function renderCursorBalance(balance, lastKnown) {
  const label = `${lastKnown ? 'Last known ' : ''}${balance.label}`;
  const upcoming = (balance.expires_at || []).filter(value => new Date(value) > Date.now()).sort();
  const detail = upcoming.length ? `Next expires ${relative(upcoming[0])} · ${fullDate(upcoming[0])}` : '';
  return `<div class="m-cursor-meter" data-testid="cursor-balance-${esc(balance.key)}">
    <div class="m-meter-head"><span>${esc(label)}</span><b>${esc(amount(balance.available, balance.unit))} available</b></div>
    ${detail ? `<div class="m-reset">${esc(detail)}</div>` : ''}
  </div>`;
}

const cursorGroups = [
  ['monthly', 'Monthly'],
  ['weekly', 'Weekly'],
  ['on_demand', 'On-demand'],
];

function renderCursorUsage(report) {
  if (!report) return '';
  const stale = cursorStale(report);
  const state = cursorState(report);
  const meters = report.meters || [];
  const groups = cursorGroups.map(([group, heading]) => {
    const rows = meters.filter(meter => meter.group === group);
    if (!rows.length) return '';
    return `<div class="m-cursor-group" data-testid="cursor-group-${esc(group)}">
      <div class="m-pools-label">${esc(heading)}</div>
      <div class="m-meters">${rows.map(meter => renderCursorMeter(meter, stale)).join('')}</div>
    </div>`;
  }).join('');
  const balances = (report.balances || []).length
    ? `<div class="m-cursor-group" data-testid="cursor-group-balances"><div class="m-pools-label">Credits</div>
        <div class="m-meters">${report.balances.map(balance => renderCursorBalance(balance, stale)).join('')}</div></div>`
    : '';
  const source = report.source === 'openusage' ? 'from OpenUsage' : report.source === 'cursor' ? 'from Cursor' : '';
  const updated = report.fetched_at
    ? `Updated ${relative(report.fetched_at)}${source ? ` · ${source}` : ''}`
    : 'Not read yet';
  const errors = (report.errors || []).slice(0, 2).map(error => `<span class="m-source-error">${esc(error)}</span>`).join('');
  const partial = meters.length && (report.errors || []).length
    ? '<span class="m-cursor-note">Some Cursor usage could not be read; the rest is shown.</span>' : '';
  const notReported = (report.not_reported || []).length && (meters.length || (report.balances || []).length)
    ? `<span class="m-cursor-note" data-testid="cursor-not-reported">Not reported: ${esc(report.not_reported.join(', '))}</span>` : '';
  const refreshError = report.refresh_error ? `<span class="m-source-error">${esc(report.refresh_error)}</span>` : '';
  return `<div class="m-provider-card m-cursor-card${stale ? ' stale' : ''}" data-testid="cursor-card" data-stale="${stale}">
    <div class="m-provider-header m-cursor-header">
      <span class="m-provider-logo cursor"><span class="m-provider-fallback" aria-hidden="true">C</span></span>
      <span class="m-provider-summary">
        <span class="m-provider-name">Cursor${report.plan ? ` <small class="m-cursor-plan">${esc(report.plan)}</small>` : ''}</span>
        <span class="m-provider-status ${esc(state.tone)}" data-testid="cursor-state">${esc(state.label)}</span>
      </span>
    </div>
    <div class="m-provider-detail">
      <div class="m-source-meta" data-testid="cursor-meta">${esc(updated)} · ${(dashboard?.providers || []).some(p => p.provider === 'cursor') ? 'monthly pools schedule Cursor jobs; Grok Bot, on-demand and credits never do' : 'not used for scheduling'}${partial}${errors}${refreshError}${notReported}</div>
      ${groups}${balances}
    </div>
  </div>`;
}

let cursorRenderedStale = null;
clockListeners.add(() => {
  // Re-render once when the report crosses its expiry, even without new data.
  const report = dashboard?.cursor_usage;
  if (!report || activeTab !== 'usage') return;
  if (cursorStale(report) !== cursorRenderedStale) renderUsage(dashboard.providers || []);
});


// ── Multi-account carousel (mirrors the Android app) ────────────────────────
// A provider family with two or more accounts shows one fixed-height card:
// page 0 is the Overview (a compact row per account), pages 1..N are one
// account each, reusing the single-account detail. Rules shared with Android
// and the Mac tray: accounts stand alone (no averaged or summed percentages),
// the service's order is kept, out-of-date numbers read "was N%" with a fixed
// last-reported reset and never a countdown, and missing numbers are "—".

/** Groups providers into single cards and multi-account families, in order. */
function usageGroups(providers) {
  const families = new Map();
  for (const item of providers) {
    const key = String(item.provider || item.id).toLowerCase();
    if (!families.has(key)) families.set(key, []);
    families.get(key).push(item);
  }
  const groups = [];
  const seen = new Set();
  for (const item of providers) {
    const key = String(item.provider || item.id).toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    const members = families.get(key);
    groups.push(members.length >= 2 ? {kind: 'family', provider: key, accounts: members} : {kind: 'single', item: members[0]});
  }
  return groups;
}

function accountName(item) {
  const label = String(item.label || '').trim();
  return label || item.id;
}

/** The one word every surface uses for an account's collection state. */
function accountState(item) {
  const status = item.account_status;
  if (status === 'signed_out' || signedOut(item)) return 'signed_out';
  if (status === 'error') return 'error';
  if (status === 'no_data' || (!item.snapshot && !status)) return 'no_data';
  if (status === 'stale' || item.snapshot_stale) return 'stale';
  return 'ok';
}

function accountDispatching(item) {
  const d = item.latest_decision?.decision;
  return accountState(item) === 'ok' && !item.paused && (d === 'RUN' || d === 'ADMIT');
}

function accountStateLine(item) {
  if (item.paused) return {text: 'Paused', tone: 'paused'};
  switch (accountState(item)) {
    case 'signed_out': return {text: loginCommand(item.provider) ? `Signed out · run ${loginCommand(item.provider)}` : 'Signed out', tone: 'danger'};
    case 'stale': return {text: 'Out of date · dispatch held', tone: 'near'};
    case 'error': return {text: 'Usage unavailable · dispatch held', tone: 'danger'};
    case 'no_data': return {text: 'No usage yet · dispatch held', tone: 'near'};
  }
  const d = item.latest_decision?.decision;
  if (item.snapshot?.weekly_unavailable && item.weekly_policy === 'session_only') return {text: 'Weekly not reported · session-only scheduling', tone: 'neutral'};
  if (item.snapshot?.weekly_unavailable && d !== 'RUN' && d !== 'ADMIT') return {text: 'Dispatch held · weekly not reported', tone: 'neutral'};
  if (d === 'RUN' || d === 'ADMIT') return {text: 'Dispatching', tone: 'healthy'};
  if (d === 'WAIT') return {text: 'Waiting', tone: 'neutral'};
  if (d === 'UNKNOWN') return {text: 'Dispatch held', tone: 'near'};
  return {text: 'No scheduler decision yet', tone: 'neutral'};
}

/** "today 10:07 AM", "tomorrow 9:00 AM", "Wed 9:00 AM", "Oct 20": fixed, never a countdown. */
function fixedTime(value, now = Date.now()) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const today = new Date(now);
  const days = Math.round((day(date) - day(today)) / 86400000);
  const time = new Intl.DateTimeFormat(undefined, {hour: 'numeric', minute: '2-digit'}).format(date);
  if (days === 0) return `today ${time}`;
  if (days === 1) return `tomorrow ${time}`;
  if (days === -1) return `yesterday ${time}`;
  if (days > 1 && days < 7) return `${new Intl.DateTimeFormat(undefined, {weekday: 'short'}).format(date)} ${time}`;
  const sameYear = date.getFullYear() === today.getFullYear();
  return new Intl.DateTimeFormat(undefined, sameYear ? {month: 'short', day: 'numeric'} : {month: 'short', day: 'numeric', year: 'numeric'}).format(date);
}

/** The compact form of a fixed time for a narrow column: "10:07 AM" today, else "Wed", else "Oct 20". */
function compactFixedTime(value, now = Date.now()) {
  const full = fixedTime(value, now);
  if (full.startsWith('today ')) return full.slice(6);
  if (full.startsWith('tomorrow ') || full.startsWith('yesterday ')) {
    return new Intl.DateTimeFormat(undefined, {weekday: 'short'}).format(new Date(value));
  }
  const m = full.match(/^(\S{2,4}) \d{1,2}:\d{2}/);
  return m ? m[1] : full;
}

/** One window's compact cell: value, reset words and the spoken sentence. */
function accountWindow(name, window, {notStarted = false, notReported = false, outOfDate = false, now = Date.now()} = {}) {
  if (notReported) return {value: 'n/r', reset: 'not reported', spoken: `${name}, not reported for this account`, percent: null};
  if (notStarted) {
    return outOfDate
      ? {value: 'was 100%', reset: 'not started', spoken: `${name}, last known: not started`, percent: null}
      : {value: '100%', reset: 'on first use', spoken: `${name}, 100 percent left, starts on first use`, percent: 100};
  }
  if (!window) return {value: '—', reset: '', spoken: `${name}, unknown`, percent: null};
  const v = pct(window.remaining);
  if (outOfDate) {
    if (!window.resets_at) return {value: `was ${v}%`, reset: '', spoken: `${name}, last known and out of date: was ${v} percent left`, percent: null};
    const at = fixedTime(window.resets_at, now);
    const due = new Date(window.resets_at).getTime() <= now;
    const short = compactFixedTime(window.resets_at, now);
    return {
      value: `was ${v}%`,
      reset: due ? `was due ${at}` : `last reported ${at}`,
      compactReset: due ? `due ${short}` : short,
      spoken: `${name}, last known and out of date: was ${v} percent left, ${due ? `reset was due ${at}` : `last reported reset ${at}`}`,
      percent: null,
    };
  }
  const reset = window.resets_at ? relative(window.resets_at) : '';
  return {value: `${v}%`, reset, spoken: `${name}, ${v} percent left${reset ? `, resets ${reset}` : ''}`, percent: v};
}

/** Banked resets with the soonest deadline; null when not reported. */
function accountCredits(snapshot, {outOfDate = false, now = Date.now()} = {}) {
  if (!snapshot || snapshot.banked_resets == null) return null;
  const count = snapshot.banked_resets;
  let deadlines = (snapshot.banked_resets_expiries || []).slice();
  if (!deadlines.length && snapshot.banked_resets_expire_at) deadlines = [snapshot.banked_resets_expire_at];
  deadlines = deadlines.map(d => new Date(d)).filter(d => !Number.isNaN(d.getTime())).sort((a, b) => a - b);
  const upcoming = deadlines.filter(d => d.getTime() > now);
  const expired = deadlines.length - upcoming.length;
  const prefix = outOfDate ? 'Last known: ' : '';
  // headline: the count, and for old numbers how many have since lapsed.
  let headline = `${prefix}${count} available`;
  if (outOfDate && expired > 0) headline += ` · ${expired} since expired`;
  const text = count > 0 && upcoming.length ? `${headline} · soonest expires ${fixedTime(upcoming[0], now)}` : headline;
  return {count, headline, text, soonest: upcoming[0]?.getTime() ?? null, deadlines: upcoming.map(d => fixedTime(d, now))};
}

function familyOverview(accounts) {
  const dispatching = accounts.filter(accountDispatching).length;
  const attention = accounts.filter(a => accountState(a) !== 'ok').length;
  const parts = [`${accounts.length} accounts`, `${dispatching} dispatching`];
  if (attention) parts.push(attention === 1 ? '1 needs attention' : `${attention} need attention`);
  return parts.join(' · ');
}

function renderOverviewRow(item, index) {
  const outOfDate = accountState(item) !== 'ok';
  const snap = accountState(item) === 'signed_out' ? null : item.snapshot;
  const pools = (snap?.allowances || []).filter(a => a.scope === 'account');
  const shortWindow = snap?.short || pools.find(a => a.role === 'session' || a.role === 'short') || null;
  const weeklyWindow = snap?.weekly_unavailable ? null : (snap?.weekly || pools.find(a => a.role === 'weekly') || null);
  const session = accountWindow('5-hour', shortWindow, {notStarted: Boolean(snap?.short_window_not_started && !shortWindow), outOfDate});
  const weekly = accountWindow(isMonthly(snap) ? 'Monthly' : 'Weekly', weeklyWindow, {outOfDate, notReported: Boolean(snap?.weekly_unavailable)});
  const credits = accountCredits(snap, {outOfDate});
  const state = accountStateLine(item);
  // The row carries one word for a problem; the page and the spoken label
  // carry the whole sentence.
  const compactState = outOfDate && !item.paused
    ? {signed_out: 'Signed out', stale: 'Out of date', error: 'Usage unavailable', no_data: 'No usage yet'}[accountState(item)]
    : state.text;
  const plan = snap?.plan || item.snapshot?.plan || '';
  const spoken = [accountName(item), plan ? `${plan} plan` : '', state.text, session.spoken, weekly.spoken, credits?.text || '']
    .filter(Boolean).join('. ');
  const cell = (w) => `<span class="m-acct-cell${w.percent == null ? ' muted' : ''}">
      <b>${esc(w.value)}</b><em>${esc(w.compactReset || w.reset || ' ')}</em></span>`;
  return `<button type="button" class="m-acct-row" data-account-open="${esc(item.id)}" data-testid="overview-row"
      aria-label="${esc(spoken)}. Open ${esc(accountName(item))}">
    <span class="m-acct-name">
      <span class="m-acct-title">${esc(accountName(item))}</span>
      <span class="m-acct-sub">${plan ? `<span class="m-acct-plan">${esc(plan)}</span>` : ''}<span class="m-acct-state ${esc(state.tone)}">${esc(compactState)}</span></span>
    </span>
    ${cell(session)}${cell(weekly)}
    <span class="m-acct-go" aria-hidden="true">›</span>
  </button>`;
}

function renderAccountPage(item) {
  const plan = item.snapshot?.plan || '';
  const state = accountStateLine(item);
  return `<div class="m-acct-page-head">
      <span class="m-acct-title">${esc(accountName(item))}</span>
      <span class="m-acct-sub">${plan ? `<span class="m-acct-plan">${esc(plan)}</span>` : ''}<span class="m-acct-id">ID ${esc(item.id)}</span></span>
      <span class="m-acct-state ${esc(state.tone)}">${esc(state.text)}</span>
    </div>${renderUsageDetail(item, {accountPage: true})}`;
}

function renderFamily(group) {
  const {provider, accounts} = group;
  const selected = accountCarouselPage.get(provider) || '';
  const index = selected ? accounts.findIndex(a => a.id === selected) : -1;
  const page = index >= 0 ? index + 1 : 0;
  if (index < 0 && selected) accountCarouselPage.set(provider, '');
  const name = providerLabel(provider);
  const icon = provider === 'claude' ? '/assets/claude.svg' : provider === 'codex' ? '/assets/codex.svg' : '';
  const position = page === 0 ? 'Overview · swipe or use › for each account' : `Account ${page} of ${accounts.length}`;
  const pages = [
    `<div class="m-acct-page" data-page="0" ${page === 0 ? '' : 'hidden'} data-testid="carousel-page-0"
        role="group" aria-roledescription="slide" aria-label="Overview">
      <div class="m-acct-columns" aria-hidden="true"><span>Account</span><span>5-hour</span><span>Weekly</span><span></span></div>
      ${accounts.map(renderOverviewRow).join('')}
    </div>`,
    ...accounts.map((item, i) => `<div class="m-acct-page" data-page="${i + 1}" ${page === i + 1 ? '' : 'hidden'} data-testid="carousel-page-${i + 1}"
        role="group" aria-roledescription="slide" aria-label="Account ${i + 1} of ${accounts.length}">
      ${renderAccountPage(item)}
    </div>`),
  ];
  return `<section class="m-provider-card m-acct-carousel" data-family="${esc(provider)}" data-testid="account-carousel"
      aria-roledescription="carousel" aria-label="${esc(name)} accounts">
    <div class="m-acct-head">
      <span class="m-provider-logo ${esc(provider)}">${icon ? `<img src="${esc(icon)}" alt="" width="18" height="18">` : ''}</span>
      <span class="m-provider-summary">
        <span class="m-provider-name">${esc(name)} · ${esc(familyOverview(accounts))}</span>
      </span>
    </div>
    <div class="m-acct-strip">
      <button type="button" class="m-acct-overview${page === 0 ? ' current' : ''}" data-carousel-go="0" ${page === 0 ? 'aria-current="true"' : ''}>${page === 0 ? 'Overview' : '‹ Overview'}</button>
      <span class="m-acct-dots" aria-hidden="true">${[0, ...accounts.map((_, i) => i + 1)].map(p => `<i class="${p === page ? 'on' : ''}"></i>`).join('')}</span>
      <button type="button" class="m-acct-step" data-carousel-step="-1" ${page === 0 ? 'disabled' : ''} aria-label="${page === 1 ? 'Overview' : 'Previous account'}">‹</button>
      <button type="button" class="m-acct-step" data-carousel-step="1" ${page === accounts.length ? 'disabled' : ''} aria-label="Next account">›</button>
    </div>
    <div class="m-acct-position" data-testid="carousel-position" tabindex="-1">${esc(position)}</div>
    <div class="m-acct-pages" data-testid="carousel-pages">${pages.join('')}</div>
  </section>`;
}

/** Sizes every carousel to its tallest page, so the cards below never jump. */
function fixCarouselHeights(root) {
  root.querySelectorAll('.m-acct-pages').forEach(container => {
    // A hidden tab measures every page as 0; leave its last height alone and
    // measure again when the tab is shown.
    if (!container.offsetParent || container.getBoundingClientRect().width === 0) return;
    const pages = [...container.querySelectorAll('.m-acct-page')];
    const visible = pages.find(page => !page.hidden);
    container.style.minHeight = '';
    // The page on screen is measured as it is; it is never hidden, so focus
    // inside it (a summary, Pause) survives. Each other page is shown
    // invisibly in the same flow at the same width, measured, and hidden
    // again: an absolutely positioned copy would wrap differently.
    const style = getComputedStyle(container);
    const padding = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom);
    let tallest = visible ? visible.getBoundingClientRect().height : 0;
    for (const page of pages) {
      if (page === visible) continue;
      page.hidden = false;
      page.style.visibility = 'hidden';
      tallest = Math.max(tallest, page.getBoundingClientRect().height);
      page.style.visibility = '';
      page.hidden = true;
    }
    container.style.minHeight = `${Math.ceil(tallest + padding)}px`;
  });
}

let carouselResizeTimer = null;
window.addEventListener('resize', () => {
  clearTimeout(carouselResizeTimer);
  carouselResizeTimer = setTimeout(() => fixCarouselHeights(document), 100);
});
document.fonts?.ready?.then(() => fixCarouselHeights(document));

/** The one carousel announcement, outside the re-rendered list so it is
 *  spoken on page changes only, never on a data refresh. */
function carouselAnnouncer() {
  let region = document.getElementById('m-carousel-announcer');
  if (!region) {
    region = document.createElement('div');
    region.id = 'm-carousel-announcer';
    region.className = 'm-sr-only';
    region.setAttribute('aria-live', 'polite');
    region.dataset.testid = 'carousel-announcement';
    document.body.appendChild(region);
  }
  return region;
}
// Present and empty from the start: a live region that appears with its
// text already in it is often not read.
if (document.body) carouselAnnouncer();
else document.addEventListener('DOMContentLoaded', carouselAnnouncer);

function announceCarousel(text) {
  carouselAnnouncer().textContent = text;
}

/** Which carousel control had focus, so a re-render can give it back. */
function carouselFocusKey() {
  const el = document.activeElement;
  const section = el?.closest?.('.m-acct-carousel');
  if (!section) return null;
  const family = section.dataset.family;
  for (const attr of ['data-carousel-step', 'data-carousel-go', 'data-account-open', 'data-action']) {
    if (el.hasAttribute(attr)) {
      const extra = attr === 'data-action' ? `[data-provider="${CSS.escape(el.dataset.provider || '')}"]` : '';
      return {family, selector: `[${attr}="${CSS.escape(el.getAttribute(attr))}"]${extra}`};
    }
  }
  return {family, selector: '.m-acct-position'};
}

function restoreCarouselFocus(key) {
  if (!key) return;
  const section = document.querySelector(`.m-acct-carousel[data-family="${CSS.escape(key.family)}"]`);
  if (!section) return;
  let target = [...section.querySelectorAll(key.selector)].find(el => !el.closest('[hidden]') && !el.disabled);
  if (!target) target = section.querySelector('.m-acct-position');
  target?.focus({preventScroll: true});
}

function wireCarousels(root) {
  root.querySelectorAll('.m-acct-carousel').forEach(section => {
    const provider = section.dataset.family;
    const accounts = (dashboard?.providers || []).filter(p => String(p.provider || p.id).toLowerCase() === provider);
    const go = (page) => {
      const target = Math.max(0, Math.min(accounts.length, page));
      if (target === current() && page !== target) return;  // already at an end
      accountCarouselPage.set(provider, target === 0 ? '' : accounts[target - 1].id);
      // renderUsage gives focus back to the control that was used.
      renderUsage(dashboard?.providers || []);
      const name = providerLabel(provider);
      announceCarousel(target === 0
        ? `${name} overview, ${accounts.length} accounts`
        : `${name} account ${target} of ${accounts.length}, ${accountName(accounts[target - 1])}`);
    };
    // Opening details inside a page can grow it; re-measure so the stable
    // height stays the tallest page.
    section.querySelectorAll('details').forEach(d => d.addEventListener('toggle', () => fixCarouselHeights(section)));
    const current = () => {
      const id = accountCarouselPage.get(provider) || '';
      const index = accounts.findIndex(a => a.id === id);
      return index >= 0 ? index + 1 : 0;
    };
    section.querySelectorAll('[data-carousel-go]').forEach(b => b.addEventListener('click', () => go(Number(b.dataset.carouselGo))));
    section.querySelectorAll('[data-carousel-step]').forEach(b => b.addEventListener('click', () => go(current() + Number(b.dataset.carouselStep))));
    section.querySelectorAll('[data-account-open]').forEach(b => b.addEventListener('click', () => {
      go(accounts.findIndex(a => a.id === b.dataset.accountOpen) + 1);
    }));
    section.addEventListener('keydown', (event) => {
      if (event.target.closest('input, textarea, select')) return;
      if (event.key === 'ArrowRight') { event.preventDefault(); go(current() + 1); }
      else if (event.key === 'ArrowLeft') { event.preventDefault(); go(current() - 1); }
      else if (event.key === 'Escape' && current() > 0) { event.preventDefault(); go(0); }
    });
    // Horizontal swipe on the pages; vertical scrolling is left alone.
    const pagesEl = section.querySelector('.m-acct-pages');
    let start = null;
    pagesEl.addEventListener('touchstart', (e) => { const t = e.touches[0]; start = {x: t.clientX, y: t.clientY}; }, {passive: true});
    pagesEl.addEventListener('touchend', (e) => {
      if (!start) return;
      const t = e.changedTouches[0];
      const dx = t.clientX - start.x, dy = t.clientY - start.y;
      start = null;
      if (Math.abs(dx) > 48 && Math.abs(dx) > Math.abs(dy) * 1.5) go(current() + (dx < 0 ? 1 : -1));
    }, {passive: true});
  });
}

function renderUsage(providers) {
  const list = $('#m-usage-list');
  const cursor = dashboard?.cursor_usage || null;
  cursorRenderedStale = cursor ? cursorStale(cursor) : null;
  if (!providers || !providers.length) {
    list.innerHTML = '<p class="m-empty">No providers configured.</p>' + renderCursorUsage(cursor);
    return;
  }
  if (!usageInitialized) {
    openProviderIDs = new Set(providers.map(provider => provider.id));
    usageInitialized = true;
  }
  const focusKey = carouselFocusKey();
  list.innerHTML = usageGroups(providers)
    .map(group => group.kind === 'family' ? renderFamily(group) : renderUsageProvider(group.item))
    .join('') + renderCursorUsage(cursor);
  fixCarouselHeights(list);
  wireCarousels(list);
  restoreCarouselFocus(focusKey);
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
      ? (ready ? 'Eligible - will dispatch next' : `Eligible (provider not ready: ${data.provider_reason || 'unknown'})`)
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

  // Mark read — only for runs that are actually still unread, so reopening an
  // already-read run can't decrement the badge for other genuinely unread runs.
  if (run && !run.activity_read_at && (run.state === 'completed' || run.state === 'failed')) {
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
  }

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
  if (name === 'usage') fixCarouselHeights(document);
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
