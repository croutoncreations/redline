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
      SQLite      OpenUsage
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
- Optional 5-hour limits: Codex works when its temporary short limit is absent.
- Prorated current and final 5-hour slots for limited providers.
- Policy-configured pace thresholds for unrestricted providers.
- Explainable `RUN`, `WAIT`, and fail-closed `UNKNOWN` decisions.
- SQLite migrations, WAL mode, foreign keys, and durable snapshot history.
- Execution-profile and one-off/recurring-task persistence.
- Priority-descending, oldest-first eligible task selection.
- `min_interval` and `require_repo_change` eligibility.
- Task enable/disable/retry and provider pause/resume.
- Persistent simulated scheduler decision history.
- Existing-directory, Git worktree, DevX, and generic-command workspace providers.
- Optional workspace setup/finalize hooks and opt-in cleanup policies.
- Noninteractive Codex CLI, Claude Code, and generic-command harness adapters.
- Transactional asynchronous run admission with one active run per provider.
- Run artifacts, recurring completion/requeue, and interrupted-run recovery.
- Graceful service shutdown.
- Opt-in automatic scheduling with immediate startup evaluation and configurable polling.
- Per-provider cycle status, active-run suppression, and automatic repository revision checks.
- Durable dispatch-attempt history, including usage/admission errors that produce no decision.
- Bounded stdout/stderr tail inspection through the service API and CLI.

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
go run ./cmd/redline decision --provider codex-main
go run ./cmd/redline scheduler evaluate --provider codex-main --json
go run ./cmd/redline scheduler execute --provider codex-main --json
go run ./cmd/redline scheduler status
go run ./cmd/redline scheduler attempts --provider codex-main
go run ./cmd/redline run list
go run ./cmd/redline run logs <run-id> --stream stderr
```

The default API is `http://127.0.0.1:7436`. Override it before the command:

```bash
go run ./cmd/redline --api http://127.0.0.1:8000 status --provider claude-main
```

## Profiles and tasks

```bash
go run ./cmd/redline profile add --file examples/codex-devx-profile.yaml --json
go run ./cmd/redline profile add --file examples/claude-worktree-profile.yaml --json
go run ./cmd/redline task add --file examples/add-tests-task.yaml --json
go run ./cmd/redline profile list
go run ./cmd/redline task list
go run ./cmd/redline task disable add-tests
go run ./cmd/redline task enable add-tests
```

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
POST /v1/providers/{account}/refresh
GET  /v1/providers/{account}/status
POST /v1/providers/{account}/decision
POST /v1/providers/{account}/pause|resume
GET|POST /v1/profiles
GET|POST /v1/tasks
POST /v1/tasks/{id}/enable|disable|retry
POST /v1/scheduler/evaluate
POST /v1/scheduler/execute
GET  /v1/scheduler/decisions?provider={account}
GET  /v1/scheduler/status
GET  /v1/scheduler/attempts?provider={account}
GET  /v1/runs
GET  /v1/runs/{id}
GET  /v1/runs/{id}/logs?stream=stdout|stderr&tail_bytes={n}
```

The service binds to loopback by default and currently has no authentication. Do not expose
it to an untrusted network.

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

## Development

```bash
go test -race ./...
go vet ./...
go test -cover ./...
```

See [the observation report](docs/phase-1-observation.md) for live-provider findings.
