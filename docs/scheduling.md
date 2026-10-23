# Scheduling and the allowance model

This guide explains how Redline decides whether to release deferred work: dispatch policies and
tiers, automatic scheduling, window-cost calibration, empirical token capacity, and the
operational history Redline keeps for every attempt.

## Decisions: RUN, WAIT, UNKNOWN

Redline records why each provider chose `WAIT`, `RUN`, or fail-closed `UNKNOWN`. Near-expiry is a
specific `RUN` case: unused allowance is approaching reset while the configured reserve remains
held. `UNKNOWN` is returned when telemetry is stale or unavailable; Redline never dispatches on an
`UNKNOWN` decision.

```bash
redline decision --provider codex-main
redline decision --provider claude-main --json
```

## Redeeming banked resets

Start with a dry run for each provider, then perform the first real redemption manually:

```bash
redline reset redeem --provider claude-main --dry-run
redline reset redeem --provider codex-main --dry-run
redline reset redeem --provider claude-main
```

Dry runs use nonexistent IDs, not a real reset: Claude expects `ineligible` / `unknown_grant`,
Codex expects `no_credit`, reported as `dry_run_ok`. Real redemption spends Claude's claimable
next grant or Codex's soonest-expiring itemized credit. A missing Codex detail list means to wait
for collection and try again shortly. Claude eligibility and cooldowns are respected. Multiple
configured accounts of the same provider type are refused because the local CLI login is ambiguous.

Redline reads credentials without refreshing or rewriting them. If Claude's login needs refreshing,
run `claude`, then retry or redeem in Claude Code with `/limit-reset`. Codex requires a ChatGPT
OAuth login, not an API key. Each operation persists an idempotency key before contacting the
provider, reuses pending keys on retry, allows only one redemption per provider, and shares usage
reads' rate-limit lockout. Inspect recent attempts through the API's `banked-resets/redemptions` route.

These are **undocumented provider endpoints** and may change. Codex has a known enforcement bug:
a redemption can be acknowledged while limits still enforce the old window, and single-window
accounts may fail. Redline refreshes real provider telemetry after success and reports `verified`
only when weekly remaining increases or the weekly reset moves later. `verified: false` includes a
warning; do not rely on new capacity until usage confirms it. Provider telemetry is still stored,
never replaced with an invented refill.

After dry runs and a manual first redemption, opt in per provider:

```yaml
banked_resets:
  on_expiry: redeem       # default: notify; off disables the expiry action
  redeem_lead: 1h
  remind_before: [24h]
```

The usage monitor attempts the next eligible reset no earlier than `expires_at - redeem_lead`,
even with the scheduler disabled. `on_expiry: redeem` requires `usage_monitor.enabled: true` and
native reset telemetry (not `usage_source: openusage`); other configurations fail to load.
Claude grants must be claimable, explicitly usable, out of cooldown, and not require hitting a
limit. Codex needs itemized credits with resets left. Demo mode and multiple accounts remain
blocked. **Provider pause is not respected for redemption**: pause controls job dispatch, not
whether an expiring reset is saved from being lost. Notifications may be disabled without
preventing redemption; reminders still require notifications.

In redeem mode, usable banked resets that clear the weekly window and will be applied before
the real weekly reset shorten the effective scheduling horizon to their redeem time. This can
trigger firesale earlier. `off` and `notify` never shorten it; model-specific pools keep their
own reset. A redeemed grant stays guarded until telemetry observed after the redemption shows a
lower, positive remaining count; then its next use can be selected. Codex credits are single-use.
A provider acknowledgement alone never invents capacity: only refreshed, verified usage demonstrates
a refill.

Automatic attempts are limited to one per provider per five minutes. Transient errors retry on
later successful monitor cycles before expiry, reusing pending request keys and respecting provider
cooldowns and 429 lockouts. `nothing_to_reset` waits at least 30 minutes before trying again;
`redeemed` and `gone` stop retries for that reset. Retry state survives daemon restarts. If a failed
reset expires unused, the next successful monitor cycle sends one `reset.redeem_failed` notice,
even if the credit has disappeared from telemetry. When automatic redemption is enabled, redeem
mode suppresses the “redeem it now” notification; the redemption sends its own outcome events.
`on_expiry: redeem` is rejected at config load when the usage monitor is disabled or the provider
is pinned to `usage_source: openusage`. Redeem mode suppresses `redeem_now` only for resets
automatic redemption will attempt before they expire; any other reset (demo mode, multiple
accounts of the provider type, or a Claude grant that is unclaimable, unusable, requires a limit,
or stays in cooldown past expiry) still gets `redeem_now`.

If a request's response is lost, automatic retries adopt that pending request and reuse its key
before considering another reset, even if it disappears from telemetry. At expiry an unresolved
request is reported as **outcome unknown**, not expired unused; check the provider. If no expiry can
be recovered, retries stop with that same unknown outcome 24 hours after the first attempt.

MCP deliberately has no redemption tool: spending resets from an agent is out of scope.

## Dispatch tiers and priority

Each task has a `dispatch_tier` that controls when it becomes eligible:

```yaml
dispatch_tier: behind # behind, well_behind, expiring, or firesale
priority: 60
```

The active provider policy first decides whether background work is safe. Redline then derives the
currently unlocked tier from the weekly pace gap, or from unavoidable throughput overflow when a
five-hour window exists. `priority` only orders tasks whose tiers are already unlocked. Recurrence
intervals, repository-change checks, and enable/disable state remain independent eligibility gates.
Existing databases migrate tasks to `behind`, preserving the prior default behavior.

Tiers unlock in order: `behind` (1), `well_behind` (2), `expiring` (3), `firesale` (4).
In the dashboard they appear as **Standard surplus**, **High surplus**, **Near expiry**, and
**Firesale (lots expiring)**.

## Policies

Policies may set `pace_gap_trigger` to admit work whenever the fraction of weekly allowance
remaining exceeds the fraction of time remaining by that amount. For example, `0.30` means Redline
can run when an account is at least 30 percentage points behind an even weekly burn pace. The
standard bundled policy uses `0.30`, early uses `0.15`, and late omits the setting so that it
continues to require a configured time threshold or unavoidable overflow.

Each provider's usage detail in the dashboard includes a dispatch-policy selector. Selecting a named
policy stores an override in SQLite; selecting **Default** returns to the provider-level YAML policy
when present, or to `active_policy` otherwise.

See `config.example.yaml` for the bundled `late`, `standard`, and `early` policy definitions.

## Effective horizon and firesale

Decisions expose `effective_reset_at` and `effective_reset_source`. The effective horizon is the
earliest future reset event that clears the weekly allowance, no later than its real reset.
The service always supplies the real `weekly` event, plus a `banked_reset` event for the next
auto-redeemable reset that clears the weekly allowance before that boundary. This requires
`banked_resets.on_expiry: redeem` and automatic redemption to be able to run: usage monitoring
enabled, a single account of that provider type, and not demo mode. The event is timed at the
earliest permitted attempt, respecting redeem lead, provider throttling, retry deadlines,
cooldowns, and provider lockouts. Predicted global resets are not implemented yet.
Slots, overflow, pace thresholds, and projections use this horizon; pace gap still measures time
remaining as a fraction of a seven-day week. Model-specific weekly pools retain their own resets.

A policy's optional `firesale_trigger` (0–1) upgrades an admitted decision to `firesale: true`
and unlocks the highest tier when `max(pace_gap, overflow) >= firesale_trigger`. Overflow is used
only when a five-hour window exists. The bundled early policy uses `0.35`, standard uses `0.45`,
and late omits it (disabled). Firesale never changes WAIT or UNKNOWN into RUN, bypasses the
rolling reserve, or relaxes pause, enable, repository-change, or exhausted-pool gates.

```yaml
policies:
  standard:
    firesale_trigger: 0.45 # alongside the normal policy fields
providers:
  claude-main:
    firesale_max_concurrent_runs: 3
```

During a provider-level firesale, a task's optional `firesale_min_interval` replaces its normal
`min_interval`. It must be positive when set; it may be shorter or longer than the normal interval.
Unset (zero) uses the normal interval. The provider's optional `firesale_max_concurrent_runs`
raises its effective cap only if larger; allowance-pool, runtime-connection, and agent-context
limits still apply. Finished runs promptly wake the scheduler while the provider's latest recorded
automatic usage decision is firesale. A model pool can unlock the firesale tier independently,
but cannot activate these provider-level cooldown or concurrency overrides.

Automatic dispatch emits `firesale.started` and `firesale.ended` once per provider state transition
when notifications are enabled and the event is selected (both are in the default list). Events
include string-valued `pace_gap`, `overflow`, `effective_reset_at`, and `unlocked_tier` data.
Manual evaluations do not emit transitions; decisions without a provider usage evaluation do not
change the recorded state.

## Automatic dispatch

Automatic execution is disabled by default. Enable it in the service configuration only after
profiles and tasks have been reviewed:

```yaml
scheduler:
  enabled: true
  poll_interval: 5m
```

The service evaluates every configured provider once at startup and then at the configured
interval. Each cycle skips paused providers, then fills available capacity up to
`max_concurrent_runs` (default `1`). Optional `pool_concurrency` entries independently cap
overlap within allowance pools such as `model:fable:weekly`. Each candidate task's Git revision
is resolved independently, and automatic decisions are recorded with `"trigger":"automatic"`.
The dashboard can persist a provider-level override without rewriting the YAML configuration;
resetting the override restores the configured default. Remote Hermes jobs must also satisfy the
selected runtime connection and agent context limits, so the effective concurrency is the
strictest applicable provider, allowance-pool, connection, or context limit.

Inspect the live loop with:

```bash
redline scheduler status
```

### Manual evaluation and execution

Simulated evaluation records the decision and selected task but does not change task state.
Execution atomically claims the task and returns a preparing run while work continues in the
service:

```bash
redline scheduler evaluate --provider codex-main --revision "$(git rev-parse HEAD)"
redline scheduler execute --provider codex-main --revision "$(git rev-parse HEAD)"
redline run show <run-id>
redline scheduler history --provider codex-main
redline pause --provider codex-main
redline resume --provider codex-main
```

## Window-cost calibration

`window_weekly_cost` is the bootstrap estimate of how much weekly allowance one completely
consumed five-hour window represents. Provider usage feeds report the two percentages but do not
report this conversion directly. Redline learns it by grouping snapshots that share the same
five-hour and weekly reset boundaries and aggregating:

```text
weekly usage increase / five-hour usage increase
```

The configured value remains authoritative while evidence is insufficient or low-confidence.
An observed value becomes effective after at least two informative five-hour windows totaling at
least one full window of consumption; four windows and two full windows of consumption are marked
high-confidence. Sub-second reset timestamp jitter is normalized during grouping. Inspect both the
evidence and the value currently used by the scheduler with:

```bash
redline calibration --provider claude-main
redline decision --provider claude-main --json
```

Decisions expose `window_weekly_cost`, `window_weekly_cost_source`, and
`calibration_confidence`. Providers without a five-hour window, such as Codex while that limit is
temporarily absent, cannot produce paired calibration evidence and continue using pace rules.

## Empirical token capacity

The read-only usage monitor refreshes usage snapshots independently from automatic scheduling and
correlates them with locally observed processed-token counts to estimate how large each allowance
window actually is.

Token observations come from two sources:

- **Redline's own runs.** Every completed run's token usage is attributed to the provider and model
  allowance selected by its execution profile.
- **An optional local token log.** When `usage_monitor.gatepost_database` points at a Gatepost
  index, Redline also imports Codex and Claude Code assistant-call records and Pi session files.
  Only explicit subscription transports are included:

  ```text
  Pi anthropic-cli  -> Claude subscription allowance
  Pi openai-codex   -> Codex subscription allowance
  ```

  Other Pi providers are excluded rather than inferred from model names. Gatepost is not yet
  publicly released; the key is not present in `config.example.yaml`, and there is nothing to
  configure unless you have Gatepost installed.

```yaml
usage_monitor:
  enabled: true
  poll_interval: 5m
  # Optional, advanced: only if Gatepost is installed.
  # gatepost_database: ~/.gatepost/viewer.db
```

Redline accumulates local processed tokens until the provider's quantized percentage moves, then
closes a correlation span without crossing a 5-hour or weekly reset. It reports estimated input,
output, cache-read, cache-creation, and total capacity where the source preserves those classes.

```bash
redline token sync --provider claude-main
redline capacity --provider claude-main
```

The capacity report includes direct 5-hour and weekly estimates and a second weekly estimate derived
from `estimated_5h_tokens / window_weekly_cost`. These are explicitly empirical processed-token
equivalents, not provider billing ledgers or guaranteed fixed caps. Model choice, cache accounting,
long-context multipliers, service-side policy, partial local-log coverage, and percentage rounding
can all change the observed relationship. Redline therefore exposes evidence counts, observed
percentage movement, token classes, source, and confidence rather than presenting a precise quota.

Each window also reports attribution coverage: total provider-reported drain, the fraction with
matching local token observations, unattributed spans, and evidence composition by harness source
and model. Incomplete attribution caps confidence even when many spans exist. Overall report
confidence is the weaker available window, and `ratio_derived_difference` quantifies disagreement
between the direct weekly estimate and the 5-hour-derived cross-check. The dashboard loads this
evidence on demand from a provider's expanded usage card so routine live updates remain inexpensive.

Weighted accounting is reported alongside raw processed tokens. Codex uses OpenAI's token-based
subscription credit card; Claude uses current API pricing as an explicit proxy because Anthropic
does not publish an equivalent subscription rate card. Unknown models remain unpriced and reduce
`pricing_coverage`. Exact Pi token classes produce a narrow quote; collapsed direct-session context
and unknown Claude cache-write duration produce low/high bounds. Raw observations are never mutated
when a rate card changes.

## Operational history

Scheduler decisions answer "what did the budget model conclude?" Dispatch attempts answer "what
happened operationally when Redline tried to release work?" Attempts persist `admitted`, `wait`,
`no_task`, and `error` outcomes for both manual and automatic execution:

```bash
redline scheduler attempts --provider codex-main
```

When budget permits work but no job can be admitted, `task_selection_reason` and bounded
`candidate_rejections` explain cooldown deadlines, unchanged or unreadable repositories,
model-pool exhaustion, saturated concurrency pools, and locked dispatch tiers. Concurrent
scheduler requests are serialized at admission; requests exceeding a configured provider or pool
limit record an `active_run` WAIT rather than degrading operational health. Different providers
always have independent capacity.

## Outcome metrics

Redline can report exact automatic RUN/WAIT/UNKNOWN decisions and job outcomes, alongside
coverage- and confidence-labeled estimates of allowance converted to completed work:

```bash
redline metrics launch --days 21
```

See [the metrics methodology](launch-metrics.md) for definitions and caveats.

## Related

- [Profiles and tasks](profiles-and-tasks.md)
- [Runs, logs, and notifications](runs-and-notifications.md)
- [Architecture](architecture.md)
- [Firesale and banked-reset design](design/firesale-and-banked-resets.md)
