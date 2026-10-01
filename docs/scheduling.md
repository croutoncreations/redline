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

## Dispatch tiers and priority

Each task has a `dispatch_tier` that controls when it becomes eligible:

```yaml
dispatch_tier: behind # behind, well_behind, or expiring
priority: 60
```

The active provider policy first decides whether background work is safe. Redline then derives the
currently unlocked tier from the weekly pace gap, or from unavoidable throughput overflow when a
five-hour window exists. `priority` only orders tasks whose tiers are already unlocked. Recurrence
intervals, repository-change checks, and enable/disable state remain independent eligibility gates.
Existing databases migrate tasks to `behind`, preserving the prior default behavior.

In the dashboard these tiers appear as **Behind pace**, **Well behind pace**, and **Capacity
likely to expire**.

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

## Window primer

Claude's 5-hour window starts with the first message after the previous window expires, so the
reset time depends on when you happen to start. The window primer sends one tiny message
("Reply with only: ok", Haiku by default) so the window starts when you choose. It does not add
allowance, and weekly limits still apply; it only moves when resets happen.

Two modes are available per Claude account:

- **At scheduled times** pings a minute after each configured time on the selected days, in the
  chosen time zone (daylight saving is handled). A 06:00 time opens a window from 06:00 to 11:00.
- **After every reset** pings a minute after each reset so a new window starts right away and one
  is always open.

The one-minute delay matters: provider resets land within about a second of the boundary, and a
ping that arrives even slightly early falls inside the expiring window and does nothing.

A ping is skipped, with the reason recorded, when a window is already open or when a Redline run
is active on the account (that run's next message opens the window itself). If the service was
asleep or stopped at a scheduled time, it still pings on wake within the catch-up allowance (45
minutes by default); later than that the slot is recorded as missed so a window does not start at
the wrong time. A time saved after it has already passed today waits for its next occurrence.
About 90 seconds after a ping, Redline checks the next usage sample and marks the attempt
*verified* if a window resets about five hours after the ping, *unverified* if the sample shows
otherwise, or *unknown* if no sample arrives within 30 minutes (the ping is then presumed to have
worked). After failed pings (signed out, CLI missing) and after pings that opened no window at
all (the local Claude Code login may not be the monitored account), retries back off from 15
minutes to 4 hours. One `primer.failed` notification is sent per run of failures: at the first
hard failure, or the second ping in a row that opened no window. Attempt history is kept for 90
days.

The ping runs `claude --print` from an empty temporary directory with `--safe-mode`,
`--no-session-persistence`, no tools, and no MCP servers. `ANTHROPIC_API_KEY`,
`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_CUSTOM_HEADERS`, every `ANTHROPIC_*BASE_URL` override, and the
`CLAUDE_CODE_USE_*` cloud switches are removed from its environment so it always uses the local
Claude Code subscription login; an API key would bill API credits and leave the window untouched.
A ping that hangs is killed after two minutes, along with any child processes on macOS and Linux
(Windows kills only the `claude` process itself).

The primer is unavailable when several Claude accounts are configured: they share this machine's
one Claude Code login, so a ping could open any of their windows and could not be verified.

The primer runs independently of automatic dispatch. Configure it from the **Window primer**
section of the Claude usage card in the dashboard, with `redline primer` (see
[CLI reference](cli.md#window-primer)), or through the HTTP API. A Mac must be awake to ping; for
early-morning schedules, `sudo pmset repeat wakeorpoweron MTWRF 05:55:00` wakes it shortly before.

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
