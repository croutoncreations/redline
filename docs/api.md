# HTTP API

Redline's service exposes a local HTTP API on `http://127.0.0.1:7436` by default. The dashboard,
native app, CLI, and MCP server are all clients of this API; only `redline serve` opens SQLite.

## Authentication

The service accepts loopback hosts by default. On first service or app launch, Redline creates a
random API credential named `api-token` beside the selected configuration with mode `0600`. The
native app exchanges it for an HttpOnly, same-site dashboard session; CLI and MCP clients read it
automatically from the `--config` location or the platform default data directory
(`~/Library/Application Support/Redline` on macOS, `~/.config/redline` on Linux,
`%AppData%\redline` on Windows).

For a direct API call:

```bash
# macOS
REDLINE_API_TOKEN="$(<"$HOME/Library/Application Support/Redline/api-token")"
# Linux
# REDLINE_API_TOKEN="$(<"$HOME/.config/redline/api-token")"
curl -H "Authorization: Bearer $REDLINE_API_TOKEN" \
  http://127.0.0.1:7436/v1/health
```

Do not copy the credential into logs. On Windows the file is created with an owner-only ACL; on
other platforms it is mode `0600`.

## Remote access

Cross-origin requests and untrusted Host headers are rejected independently. Exact Tailscale
MagicDNS hostnames may be added under `api.trusted_hosts` for HTTPS access through Tailscale Serve;
keep Redline bound to loopback and proxy it with `tailscale serve --bg localhost:7436`. Remote
session cookies are Secure. Never expose Redline publicly (for example with Tailscale Funnel).
See [Mobile dashboard setup](mobile.md) for the HTTPS proxy and pairing flow.

## Dashboard

The dashboard at `/` summarizes current provider allowances, the dispatch queue, recent runs,
scheduler decisions, and bounded run-log tails. Jobs and execution profiles can be created and
managed there. A server-sent event stream (`/v1/dashboard/events`) keeps the page current while it
is open; the aggregate API does not expose task prompts or lifecycle commands.

## Endpoints

```text
GET  /v1/health
GET  /v1/health/details?window={duration}
GET  /v1/dashboard
GET  /v1/dashboard/events
POST /v1/pairing
POST /v1/pairing/redeem
POST /v1/providers/{account}/refresh
GET  /v1/providers/{account}/status
GET  /v1/providers/{account}/candidates
GET  /v1/providers/{account}/calibration
GET  /v1/providers/{account}/capacity
POST /v1/providers/{account}/token-sync
POST /v1/providers/{account}/decision
PATCH /v1/providers/{account}/policy
PATCH /v1/providers/{account}/concurrency
POST /v1/providers/{account}/pause|resume
GET|POST /v1/profiles
GET  /v1/profile-options?refresh={true|false}
GET|PATCH|DELETE /v1/profiles/{id}
GET  /v1/runtime-connections/imports
GET|POST /v1/runtime-connections
GET|PATCH|DELETE /v1/runtime-connections/{id}
POST /v1/runtime-connections/{id}/discover
GET  /v1/runtime-connections/{id}/jobs
POST /v1/runtime-connections/{id}/jobs/{job}/run
GET|POST /v1/agent-contexts
GET|PATCH|DELETE /v1/agent-contexts/{id}
GET|POST /v1/tasks
GET  /v1/task-templates
GET|PATCH|DELETE /v1/tasks/{id}
POST /v1/tasks/{id}/enable|disable|retry
POST /v1/tasks/{id}/dispatch
POST /v1/scheduler/evaluate
POST /v1/scheduler/execute
GET  /v1/scheduler/decisions?provider={account}
GET  /v1/scheduler/status
GET  /v1/usage-monitor/status
GET  /v1/scheduler/attempts?provider={account}
GET  /v1/metrics/launch?days={n}&provider={account}
GET  /v1/runs
GET  /v1/runs/completions?baseline=true
GET  /v1/runs/completions?after={cursor}&limit={n}
GET  /v1/runs/attribution?branch={head}&repository={abs path}
POST /v1/branch-leases
DELETE /v1/branch-leases?repository={abs path}&branch={branch}&run_id={run}
GET  /v1/runs/{id}
GET  /v1/runs/{id}/events?limit={n}
GET  /v1/runs/{id}/logs?stream={stream}&tail_bytes={n}
POST /v1/runs/{id}/read
POST /v1/runs/read-all
GET  /v1/notifications
```

`/v1/runs/completions?baseline=true` returns `{"cursor": n, "runs": []}` to start watching
only future completions. Then request `?after=n&limit=100`, process the returned `runs` in
completion order, and use the returned `cursor` for the next page. `limit` defaults to 100 and is
capped at 100. The cursor is a durable, transactionally allocated completion sequence, not a wall
clock; it covers tied timestamps, delayed commits, and service-recovered failed runs. A client
that restarts without saving its cursor should request a new baseline; it won't replay old runs.

Tasks accept `kind` (`standard`, the default, or `pr_maintenance`) on create and update, and
return it from task, dashboard, and MCP views. A `pr_maintenance` task must be `recurring`, must
have a positive `min_interval`, and may omit `prompt`. See
[PR maintenance jobs](profiles-and-tasks.md#pr-maintenance-jobs).

`GET /v1/runs/attribution` returns `{attributable, reason, run_id, task_id, task_name,
task_kind, run_state, repository, workspace_branch}`, always with status 200 when the query is
valid. `attributable` is true only when a run recorded exactly this workspace branch, that run's
recorded workspace lies inside the repository's managed worktree directories, its profile's
repository is this repository, the run has finished, and its task is `standard`. Otherwise
`reason` names the first check that failed.

`POST /v1/branch-leases` with `{repository, branch, run_id}` grants that run exclusive permission
to change the branch. The run must exist, be preparing or running, and belong to the repository:
its profile repository must match, and its recorded workspace must be inside it. The request
returns 409 if another active run holds the lease or the run that created the branch is still
active. A lease held by a finished run is stale and is taken over; re-acquiring by the holder is
idempotent. `DELETE` releases a lease (holder only; 409 for anyone else). Leases are also released
when their run completes or is recovered after a restart.

Run log responses are tail-bounded to 64 KiB and may only resolve regular files beneath the
configured `run_artifacts_dir`; paths and symlinks that escape that root are rejected.

## Related

- [MCP and agent interface](mcp.md)
- [CLI reference](cli.md)
- [Architecture](architecture.md)
