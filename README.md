# Redline

Redline is a budget-aware dispatcher for deferred LLM work. It models subscription
allowances, maintains a durable priority queue, and explains which task would be admitted.

The current implementation includes the Phase 2 service and simulated scheduler. It does
not launch agents or create workspaces yet.

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
- Graceful service shutdown.

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
```

The default API is `http://127.0.0.1:7436`. Override it before the command:

```bash
go run ./cmd/redline --api http://127.0.0.1:8000 status --provider claude-main
```

## Profiles and tasks

```bash
go run ./cmd/redline profile add --file examples/codex-devx-profile.yaml --json
go run ./cmd/redline task add --file examples/add-tests-task.yaml --json
go run ./cmd/redline profile list
go run ./cmd/redline task list
go run ./cmd/redline task disable add-tests
go run ./cmd/redline task enable add-tests
```

Simulated evaluation records the decision and selected task but does not change task state:

```bash
go run ./cmd/redline scheduler evaluate --provider codex-main --revision "$(git rev-parse HEAD)"
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
GET  /v1/scheduler/decisions?provider={account}
```

The service binds to loopback by default and currently has no authentication. Do not expose
it to an untrusted network.

## Development

```bash
go test -race ./...
go vet ./...
go test -cover ./...
```

See [the observation report](docs/phase-1-observation.md) for live-provider findings.

