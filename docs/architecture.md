# Architecture

## Components

```text
CLI / MCP / dashboard / native app
              |
              v
       local HTTP service
         |           |
         v           v
      SQLite      OpenUsage / native collectors + local token logs
         |
         v
 opt-in scheduler
```

Only `redline serve` reads configuration and opens SQLite. All operational CLI commands
consume the local HTTP API. SQLite is authoritative for usage history, profiles, tasks,
queue order, pause state, runs, and scheduler decisions. YAML is used for service config
and profile/task import; large run artifacts remain on the filesystem.

The native macOS app adopts an already-running Redline service or starts the Go service embedded
in its app bundle, so the install is a single application rather than separate UI and daemon
packages. The MCP server (`redline mcp`) is a thin authenticated client of the same loopback
service; it never opens SQLite or starts a second scheduler.

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

## Capabilities reference

### Usage monitoring and allowance modeling

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

### Decisions and scheduling

- Policy-configured pace thresholds for unrestricted providers.
- Explainable `RUN`, `WAIT`, and fail-closed `UNKNOWN` decisions.
- Priority-descending, oldest-first eligible task selection.
- Pool-aware candidate scanning so an exhausted Fable task cannot starve eligible non-Fable work.
- `min_interval` and `require_repo_change` eligibility.
- Task enable/disable/retry and provider pause/resume.
- Persistent simulated scheduler decision history.
- Opt-in automatic scheduling with immediate startup evaluation and configurable polling.
- Per-provider cycle status, active-run suppression, and automatic repository revision checks.
- Durable dispatch-attempt history, including usage/admission errors that produce no decision.
- Explainable task-selection rejections for cooldowns, repository state, budget pools, and dispatch tiers.
- Admission contention is recorded as a normal `WAIT`, not a false scheduler failure.

### Workspaces and harnesses

- Existing-directory, Git worktree, DevX, and generic-command workspace providers.
- Configurable DevX creation arguments such as `workspace_args: [--target, host]`.
- Optional workspace setup/finalize hooks and opt-in cleanup policies.
- Noninteractive Codex CLI, Claude Code, Pi, Hermes Gateway, and generic-command harness adapters.
- Local/remote runtime connections and agent contexts for selecting runtime-owned profiles,
  projects, working directories, and isolated sessions.

### Runs and persistence

- SQLite migrations, WAL mode, foreign keys, and durable snapshot history.
- Execution-profile and one-off/recurring-task persistence.
- Transactional asynchronous run admission with configurable provider and allowance-pool limits.
- Run artifacts, recurring completion/requeue, and interrupted-run recovery.
- Graceful service shutdown.
- Bounded stdout/stderr tail inspection through the service API and CLI.
- Ordered lifecycle audit events for every run, with prompt text excluded from event snapshots.
- Interrupted-run recovery appends a terminal lifecycle event and records whether its workspace was preserved.
- Captured prepare/finalize hook stdout and stderr exposed through the service API and CLI.

### Notifications, health, and agent access

- Opt-in command notifications for run completion/failure and scheduler errors.
- Durable notification delivery history and 24-hour operational health summaries.
- API-backed stdio MCP server with bounded operational reads, explicit task/provider controls,
  and a separately annotated scheduler-dispatch tool.

## Related

- [Scheduling and allowance model](scheduling.md)
- [HTTP API](api.md)
- [Native macOS app internals](native-macos.md)
