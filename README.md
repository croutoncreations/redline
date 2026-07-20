# Redline

Redline is a budget-aware dispatcher for deferred LLM work. It models subscription
allowances, maintains a durable priority queue, and explains which task would be admitted.

The current implementation includes the Phase 4 automatic execution service. Simulated and
explicit execution remain available, while an opt-in service loop can evaluate configured
providers and launch eligible Codex CLI, Claude Code, or generic-command tasks unattended.

## Architecture

```text
CLI / future MCP / future UI
              |
              v
       local HTTP service
         |           |
         v           v
      SQLite      OpenUsage + Gatepost logs
         |
         v
 simulated scheduler
```

Only `redline serve` reads configuration and opens SQLite. All operational CLI commands
consume the local HTTP API. SQLite is authoritative for usage history, profiles, tasks,
queue order, pause state, runs, and scheduler decisions. YAML is used for service config
and profile/task import; large run artifacts will remain on the filesystem.

## Implemented behavior

- Codex and Claude usage ingestion from OpenUsage `/v1/usage/{provider}`.
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
- Transactional asynchronous run admission with one active run per provider.
- Run artifacts, recurring completion/requeue, and interrupted-run recovery.
- Graceful service shutdown.
- Opt-in automatic scheduling with immediate startup evaluation and configurable polling.
- Per-provider cycle status, active-run suppression, and automatic repository revision checks.
- Durable dispatch-attempt history, including usage/admission errors that produce no decision.
- Bounded stdout/stderr tail inspection through the service API and CLI.
- Opt-in command notifications for run completion/failure and scheduler errors.
- Durable notification delivery history and 24-hour operational health summaries.
- Ordered lifecycle audit events for every run, with prompt text excluded from event snapshots.
- Captured prepare/finalize hook stdout and stderr exposed through the service API and CLI.

## Quick start

Install and run OpenUsage, then copy and adjust the example config:

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

In the dashboard, harness and model choices are guided but not restrictive: Codex CLI and Claude
Code expose common model presets, while **Other model** and **Custom command** preserve arbitrary
local integrations. Repository paths previously used by profiles are remembered as suggestions and
can always be typed directly. The advanced **Allowance routing override** corresponds to
`budget_model_group`; leave it automatic unless a provider exposes a separate model-specific pool
that model-name inference cannot identify.

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
POST /v1/providers/{account}/pause|resume
GET|POST /v1/profiles
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

The service binds to loopback by default and currently has no authentication. Do not expose
it to an untrusted network.

For durable macOS operation, see [the launchd guide](docs/launchd.md). The LaunchAgent template
uses `RunAtLoad` and `KeepAlive`, captures stdout/stderr, and documents the required harness `PATH`.

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
interval. Each cycle skips paused providers and providers with an active run. It admits at most
one task per provider, resolves each candidate task's Git revision independently, and records
automatic decisions with `"trigger":"automatic"`. Inspect the live loop with:

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
```

See [the observation report](docs/phase-1-observation.md) for live-provider findings.
