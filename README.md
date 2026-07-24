# Redline

Redline is a budget-aware dispatcher for deferred LLM work. It models subscription
allowances, maintains a durable priority queue, and explains which task would be admitted.

Redline includes a local service, CLI, web dashboard, and native macOS menu-bar app. An opt-in
scheduler evaluates configured providers and launches eligible Codex CLI, Claude Code, Pi, or
generic-command tasks unattended.

## Architecture

```text
CLI / dashboard / native app
              |
              v
       local HTTP service
         |           |
         v           v
      SQLite      OpenUsage/native + Gatepost logs
         |
         v
 simulated scheduler
```

Only `redline serve` reads configuration and opens SQLite. All operational CLI commands
consume the local HTTP API. SQLite is authoritative for usage history, profiles, tasks,
queue order, pause state, runs, and scheduler decisions. YAML is used for service config
and profile/task import; large run artifacts will remain on the filesystem.

## Implemented behavior

- Sticky per-provider allowance-source selection: reuse a healthy OpenUsage loopback API, then
  fall back to native Codex and Claude collectors after repeated failures.
- Model-specific Claude allowance ingestion and scheduling; Fable tasks require both shared Claude
  capacity and the Fable weekly pool, while Haiku, Sonnet, and Opus use shared pools only.
- Optional 5-hour limits: Codex works when its temporary short limit is absent.
- Prorated current and final 5-hour slots for limited providers.
- Organic calibration of five-hour-to-weekly capacity from paired usage snapshots.
- Empirical 5-hour and weekly processed-token capacity estimates from Codex, Claude Code, and
  explicitly mapped Pi subscription sessions.
- Versioned Codex-credit and Claude API-dollar-equivalent allowance estimates with pricing coverage.
- Independent read-only usage monitoring while automatic dispatch remains disabled.
- Policy-configured pace thresholds for unrestricted providers.
- Explainable `RUN`, `WAIT`, and fail-closed `UNKNOWN` decisions.
- SQLite migrations, WAL mode, foreign keys, and durable snapshot history.
- Execution-profile and one-off/recurring-task persistence.
- Priority-descending, oldest-first eligible task selection.
- Pool-aware candidate scanning so an exhausted Fable task cannot starve eligible non-Fable work.
- `min_interval` and `require_repo_change` eligibility.
- Task enable/disable/retry and provider pause/resume.
- Persistent simulated scheduler decision history.
- Existing-directory, Git worktree, DevX, and generic-command workspace providers.
- Configurable DevX creation arguments such as `workspace_args: [--target, host]`.
- Optional workspace setup/finalize hooks and opt-in cleanup policies.
- Noninteractive Codex CLI, Claude Code, and generic-command harness adapters.
- Transactional asynchronous run admission with configurable provider and allowance-pool limits.
- Run artifacts, recurring completion/requeue, and interrupted-run recovery.
- Graceful service shutdown.
- Opt-in automatic scheduling with immediate startup evaluation and configurable polling.
- Per-provider cycle status, active-run suppression, and automatic repository revision checks.
- Durable dispatch-attempt history, including usage/admission errors that produce no decision.
- Explainable task-selection rejections for cooldowns, repository state, budget pools, and dispatch tiers.
- Admission contention is recorded as a normal `WAIT`, not a false scheduler failure.
- Bounded stdout/stderr tail inspection through the service API and CLI.
- Opt-in command notifications for run completion/failure and scheduler errors.
- Durable notification delivery history and 24-hour operational health summaries.
- Ordered lifecycle audit events for every run, with prompt text excluded from event snapshots.
- Interrupted-run recovery appends a terminal lifecycle event and records whether its workspace was preserved.
- Captured prepare/finalize hook stdout and stderr exposed through the service API and CLI.

## Quick start

Copy and adjust the example config. Redline reuses OpenUsage when it is running, but can collect
Codex and Claude subscription windows natively when it is unavailable:

```bash
cp config.example.yaml redline.yaml
go run ./cmd/redline --config redline.yaml serve
```

In another terminal, all commands use the API:

```bash
go run ./cmd/redline usage refresh --provider codex-main --json
go run ./cmd/redline status --provider codex-main
go run ./cmd/redline calibration --provider claude-main
go run ./cmd/redline token sync --provider claude-main
go run ./cmd/redline capacity --provider claude-main
go run ./cmd/redline decision --provider codex-main
go run ./cmd/redline scheduler evaluate --provider codex-main --json
go run ./cmd/redline scheduler execute --provider codex-main --json
go run ./cmd/redline scheduler status
go run ./cmd/redline scheduler attempts --provider codex-main
go run ./cmd/redline run list
go run ./cmd/redline run events <run-id>
go run ./cmd/redline run logs <run-id> --stream stderr
go run ./cmd/redline health
go run ./cmd/redline notification list
```

The default API is `http://127.0.0.1:7436`. Override it before the command:

```bash
go run ./cmd/redline --api http://127.0.0.1:8000 status --provider claude-main
```

Claude status includes supplemental model pools reported by OpenUsage:

```text
claude: 5-hour 100.0% remaining, 73.0% weekly remaining (...)
  Fable: 48.0% remaining (...)
```

## Profiles and tasks

```bash
go run ./cmd/redline profile add --file examples/codex-devx-profile.yaml --json
go run ./cmd/redline profile add --file examples/claude-worktree-profile.yaml --json
go run ./cmd/redline profile add --file examples/claude-fable-devx-profile.yaml --json
go run ./cmd/redline task add --file examples/add-tests-task.yaml --json
go run ./cmd/redline profile list
go run ./cmd/redline task list
go run ./cmd/redline task disable add-tests
go run ./cmd/redline task enable add-tests
```

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

Fable profiles may set `budget_model_group: fable`; Redline also recognizes `fable`,
`claude-fable-5`, and `claude-fable-latest` model aliases. Haiku, Sonnet, and Opus remain
account-pool-only models.

The dashboard discovers installed Codex CLI, Claude Code, and Pi harnesses, shows each CLI version,
and builds model choices from local, refreshable catalogs rather than hardcoded presets. Codex uses
its own model cache. Pi uses its offline model listing; those subscription-backed Pi models also
supply Claude Code's versioned choices because Claude Code accepts full model names but does not
provide a model-list command. Pi profiles retain provider-qualified model IDs such as
`openai-codex/gpt-5.6-sol` and `anthropic-cli/claude-opus-4-8`, keeping the model tied to the usage
pool Redline monitors. **Other model** and **Custom command** remain available when discovery is
incomplete or a local integration is not built in.

Repository paths previously used by profiles are remembered as suggestions and can always be typed
directly. The advanced **Allowance routing override** corresponds to `budget_model_group`; leave it
automatic unless a provider exposes a separate model-specific pool that model-name inference cannot
identify. Provider-qualified Pi Fable IDs are inferred automatically.

For small, self-contained tasks, the minimal example profiles suppress personal hooks, plugin
activation, MCP servers, rules, and session persistence. This reduces startup variability and
prevents a background one-word task from inheriting a large interactive environment:

```bash
go run ./cmd/redline profile add --file examples/codex-minimal-devx-profile.yaml
go run ./cmd/redline profile add --file examples/claude-minimal-devx-profile.yaml
```

Minimal profiles deliberately omit repository instructions and disable tools. Do not use them for
code changes, tests, reviews, or any task that needs `AGENTS.md`, `CLAUDE.md`, skills, MCP servers,
or filesystem tools. Use a normal profile with only the customizations that task requires.

Simulated evaluation records the decision and selected task but does not change task state.
Execution atomically claims the task and returns a preparing run while work continues in the
service:

```bash
go run ./cmd/redline scheduler evaluate --provider codex-main --revision "$(git rev-parse HEAD)"
go run ./cmd/redline scheduler execute --provider codex-main --revision "$(git rev-parse HEAD)"
go run ./cmd/redline run show <run-id>
go run ./cmd/redline scheduler history --provider codex-main
go run ./cmd/redline pause --provider codex-main
go run ./cmd/redline resume --provider codex-main
```

## API

The main endpoints are:

```text
GET  /v1/health
GET  /v1/health/details?window={duration}
GET  /v1/dashboard
GET  /v1/dashboard/events
POST /v1/providers/{account}/refresh
GET  /v1/providers/{account}/status
GET  /v1/providers/{account}/calibration
GET  /v1/providers/{account}/capacity
POST /v1/providers/{account}/token-sync
POST /v1/providers/{account}/decision
PATCH /v1/providers/{account}/policy
POST /v1/providers/{account}/pause|resume
GET|POST /v1/profiles
GET  /v1/profile-options?refresh={true|false}
GET|PATCH|DELETE /v1/profiles/{id}
GET|POST /v1/tasks
GET|PATCH|DELETE /v1/tasks/{id}
POST /v1/tasks/{id}/enable|disable|retry
POST /v1/scheduler/evaluate
POST /v1/scheduler/execute
GET  /v1/scheduler/decisions?provider={account}
GET  /v1/scheduler/status
GET  /v1/usage-monitor/status
GET  /v1/scheduler/attempts?provider={account}
GET  /v1/runs
GET  /v1/runs/{id}
GET  /v1/runs/{id}/events?limit={n}
GET  /v1/runs/{id}/logs?stream={stream}&tail_bytes={n}
GET  /v1/notifications
```

The service includes a local dashboard at
[`http://127.0.0.1:7436/`](http://127.0.0.1:7436/). It summarizes current provider allowances,
the dispatch queue, recent runs, scheduler decisions, and bounded run-log tails. Jobs and execution
profiles can be created and managed there. A server-sent event stream keeps the page current while
it is open; the aggregate API does not expose task prompts or lifecycle commands.

Each provider's usage detail includes a dispatch-policy selector. Selecting a named policy stores
an override in SQLite; selecting **Default** returns to the provider-level YAML policy when present,
or to `active_policy` otherwise.

The service binds to loopback by default and currently has no authentication. Do not expose
it to an untrusted network.

For durable macOS operation, see [the launchd guide](docs/launchd.md). The LaunchAgent template
uses `RunAtLoad` and `KeepAlive`, captures stdout/stderr, and documents the required harness `PATH`.

## Native macOS menu bar

The first native shell lives under `macos/`. It shows provider allowance, scheduler, active-run,
and operational-health state through a compact menu-bar summary and native quick-status popover.
It embeds the full local dashboard in a native app window for management.
Configured release builds use Sparkle 2 for automatic and manual signed update checks; local builds
remain update-disabled unless an HTTPS appcast and Ed25519 public key are explicitly supplied.
It adopts an already-running Redline service or starts the Go service embedded in its app bundle,
so the eventual install remains a single application rather than separate UI and daemon packages.
On first run it can create a safe starter configuration with automatic dispatch disabled. Its
**App Setup…** flow can enable launch at login and replace an existing `com.jfox.redline`
LaunchAgent without moving the configured database, queue, history, or run artifacts; the old plist
is retained as a recoverable backup.

```bash
swift test --package-path macos
./scripts/build-macos-app.sh
open dist/Redline.app
```

See [the native macOS guide](docs/native-macos.md) for service ownership, packaging, and release
configuration.

## Execution lifecycle

```text
queued task -> preparing workspace -> running harness -> finalize -> optional cleanup
            -> completed (one-off) or requeued (recurring)
            -> failed on workspace/harness failure
```

Redline does not impose a maximum runtime. Once admitted, the harness owns the task until it
exits. Finalize or cleanup failure is recorded separately and does not turn a successful agent
run into a failed run. On service startup, runs interrupted by a prior process exit are marked
failed for explicit inspection and retry.

Cleanup defaults to `never`. Supported values are `never`, `on_success`, and `always`.
Agent instructions and lifecycle hooks remain responsible for commit, push, and PR behavior.

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
Inspect the live loop with:

```bash
go run ./cmd/redline scheduler status
```

### Window-cost calibration

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
go run ./cmd/redline calibration --provider claude-main
go run ./cmd/redline decision --provider claude-main --json
```

Decisions expose `window_weekly_cost`, `window_weekly_cost_source`, and
`calibration_confidence`. Providers without a five-hour window, such as Codex while that limit is
temporarily absent, cannot produce paired calibration evidence and continue using pace rules.

### Empirical token capacity

The read-only usage monitor imports Codex and Claude Code assistant-call records from Gatepost and
refreshes OpenUsage snapshots independently from automatic scheduling. It also reads Pi session
files from Gatepost's session index and includes only explicit subscription transports:

```text
Pi anthropic-cli  -> Claude subscription allowance
Pi openai-codex   -> Codex subscription allowance
```

Other Pi providers are excluded rather than inferred from model names.

```yaml
usage_monitor:
  enabled: true
  poll_interval: 5m
  gatepost_database: ~/.gatepost/viewer.db
```

Redline accumulates local processed tokens until the provider's quantized percentage moves, then
closes a correlation span without crossing a 5-hour or weekly reset. It reports estimated input,
output, cache-read, cache-creation, and total capacity where the source preserves those classes.
Gatepost's broad Codex/Claude index currently provides context/input-like and output tokens. Pi's
raw records preserve input, output, cache-read, and cache-creation classes.

```bash
go run ./cmd/redline token sync --provider claude-main
go run ./cmd/redline capacity --provider claude-main
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

## Operational history and run output

Scheduler decisions answer “what did the budget model conclude?” Dispatch attempts answer “what
happened operationally when Redline tried to release work?” Attempts persist `admitted`, `wait`,
`no_task`, and `error` outcomes for both manual and automatic execution:

```bash
go run ./cmd/redline scheduler attempts --provider codex-main
```

When budget permits work but no job can be admitted, `task_selection_reason` and bounded
`candidate_rejections` explain cooldown deadlines, unchanged or unreadable repositories,
model-pool exhaustion, saturated concurrency pools, and locked dispatch tiers. Concurrent
scheduler requests are serialized at admission; requests exceeding a configured provider or pool
limit record an `active_run` WAIT rather than degrading operational health. Different providers
always have independent capacity.

Completed run output can be inspected without reading artifact paths directly. Responses are tail
bounded to 64 KiB and may only resolve regular files beneath the configured `run_artifacts_dir`;
paths and symlinks that escape that root are rejected.

```bash
go run ./cmd/redline run logs <run-id>
go run ./cmd/redline run logs <run-id> --stream stderr --tail-bytes 8192
go run ./cmd/redline run logs <run-id> --json
```

The lifecycle timeline records workspace preparation, harness execution, finalization, cleanup,
and the terminal run result. Task prompts are intentionally omitted. Execution-profile fields,
including lifecycle commands, are retained for reproducibility, so secrets should be passed via
the environment rather than embedded in profile command strings.

```bash
go run ./cmd/redline run events <run-id>
go run ./cmd/redline run logs <run-id> --stream prepare_stderr
go run ./cmd/redline run logs <run-id> --stream finalize_stdout
```

## Notifications and health

Notifications are disabled by default. When enabled, Redline invokes a trusted local command with
a versioned event document on stdin. Supported events are `run.completed`, `run.failed`, and
`scheduler.error`.

```yaml
notifications:
  enabled: true
  command: ./scripts/redline-notify
  timeout: 30s
  events: [run.completed, run.failed, scheduler.error]
```

The hook also receives `REDLINE_EVENT_TYPE`, `REDLINE_PROVIDER_ACCOUNT_ID`, `REDLINE_TASK_ID`, and
`REDLINE_RUN_ID`. Delivery failures are persisted but never alter the associated run or scheduler
outcome. A service restart marks an indeterminate pending delivery failed instead of retrying a
possibly non-idempotent hook.

```bash
go run ./cmd/redline notification list
go run ./cmd/redline health --window 24h
```

Detailed health reports active/recent run counts, dispatch errors, and notification failures.
The lightweight `/v1/health` probe remains independent of recent operational failures.

## Development

```bash
go test -race ./...
go vet ./...
go test -cover ./...
npm install
npx playwright install chromium # first run only
npm run test:dashboard
```

The Playwright suite runs the embedded dashboard in an isolated browser with deterministic API and
server-sent-event fixtures. It covers task/profile workflows, dynamic harness and model selection,
run logs, live updates, responsive layout, and loading/error states without touching a live queue or
using provider quota.
