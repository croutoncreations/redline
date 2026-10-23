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

When `cursor_usage` is enabled, `/v1/dashboard` also carries a `cursor_usage` report (and
`GET /v1/cursor-usage` returns it alone). That report is display-only and includes Grok Bot,
on-demand spend and credits. A separate `provider: cursor` account is scheduled only from the
monthly subscription pools; see [Cursor](profiles-and-tasks.md#cursor). A monthly account window
carries `snapshot.weekly.period_seconds` (about 2592000); clients should label it monthly.

## Endpoints

```text
GET  /v1/health
GET  /v1/health/details?window={duration}
GET  /v1/dashboard
GET  /v1/dashboard/events
GET  /v1/cursor-usage
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

Run log responses are tail-bounded to 64 KiB and may only resolve regular files beneath the
configured `run_artifacts_dir`; paths and symlinks that escape that root are rejected.

## Firesale task fields

Task create/update requests accept `dispatch_tier: "firesale"` and `firesale_min_interval`, a
positive duration string such as `"10m"`. Omit the interval to use the normal cooldown; PATCH
with `""` clears it. It may be shorter or longer than `min_interval`. Task responses represent
both intervals as integer nanoseconds. Decision results expose `firesale`, `effective_reset_at`,
and `effective_reset_source`; automatic scheduler records also retain `provider_firesale` when
provider usage was evaluated, independently of model-pool tier unlocks.

## Banked reset redemption

Redemption POSTs require the bearer API token; mobile pairing session cookies alone receive **403** (including dry runs). The GET history route still accepts session cookie authentication.

- `POST /v1/providers/{id}/banked-resets/redeem` with `{"dry_run": true}` validates the provider
  request using a nonexistent reset ID. `{"dry_run": false}` spends the selected one-time reset.
  Automatic redemption is opt-in via `banked_resets.on_expiry: redeem`
  ([setup](scheduling.md#redeeming-banked-resets)); this endpoint always performs a manual or
  dry-run redemption. An optional `expected_reset_id` binds real redemption to
  a previously confirmed ID; a different current selection returns **409**, names both IDs, and
  spends nothing. It is ignored for dry runs. Unresolved redemptions are retried before selecting
  another reset. Completed successes return `replayed: true` and `completed_at` without a new
  provider call or verification claim.
- `GET /v1/providers/{id}/banked-resets/redemptions` returns at most 20 recent attempts, newest
  first, including pending operations whose idempotency keys survive restarts. An ambiguous
  request finalized at expiry has outcome `unknown`; check the provider before trying another reset.

Example result:

```json
{"outcome":"redeemed","code":"reset","reason":"","reset_id":"grant_1","request_id":"a-persisted-key","verified":true,"cleared":["five_hour","weekly"]}
```

Outcomes are `redeemed`, `nothing_to_reset`, `unavailable`, `cooldown`, `gone`, `failed`, and
`dry_run_ok`. `cooldown_until` is an optional RFC3339 timestamp; `resets_left` is optional.
Refill verification compares weekly remaining allowance and the reset boundary after a usage
refresh. Unverified redemption still returns `redeemed` with a note. HTTP errors cover invalid
requests, unknown providers, transport/storage failures, and **409** for an in-progress redemption
or ambiguous local login (multiple configured accounts of the same provider type, including profile/card accounts).
Accounts with `claude_config_dir` or a non-default `openusage_card` also return **409**:
redemption (including dry runs and automatic redemption) only supports the default provider login.
These accounts retain reminder notifications but cannot shorten the automatic reset horizon. Demo servers
refuse both real and dry-run redemption with **409**, without reading credentials or sending requests.

Attempts include `id`, `provider_account_id`, `reset_id`, `request_id`, `trigger`
(`manual`, `automatic`, or `dry_run`), `state` (`pending` or `done`), `outcome`, `code`, `reason`,
`created_at`, and optional `completed_at`. Transport failures remain pending with failure details
so retries reuse the original key. Provider endpoints are fixed, not user-configurable, to prevent
sending bearer tokens to another host. See [redemption safety and caveats](scheduling.md#redeeming-banked-resets).

## Related

- [MCP and agent interface](mcp.md)
- [CLI reference](cli.md)
- [Architecture](architecture.md)
