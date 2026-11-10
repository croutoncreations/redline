# HTTP API

Redline's service exposes a local HTTP API on `http://127.0.0.1:7436` by default. The dashboard,
native app, CLI, and MCP server are all clients of this API. The service owns operational SQLite
access.

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

### Principals and permissions

The master API token sent as `Authorization: Bearer` is the **owner** credential. Pairing redeem
and local dashboard bootstrap instead issue fresh **device** tokens in the HttpOnly
`redline_api_session` cookie (the redeem response remains `204` with `Set-Cookie`). Native pairing
clients can extract that cookie value and send it as a bearer token. Only a SHA-256 hash is stored;
active device sessions survive restarts, and `last_used_at` updates at most once per minute.

| Action | Owner bearer | Paired device (bearer or cookie) / legacy master cookie |
| --- | --- | --- |
| View data; create/edit/manage jobs and profiles; start runs | Allowed | Allowed |
| Pause/resume providers; start/stop firesales | Allowed | Allowed |
| Redeem banked resets (including dry runs) | Allowed | Only with `api.paired_devices.allow_reset_redemption: true` (default false) |
| Create pairing tokens (`POST /v1/pairing`) | Allowed | Forbidden |
| List/revoke devices | Allowed | Forbidden |
| List push registrations | Allowed | Forbidden |
| Register/remove own push token | Forbidden | Allowed (device session required) |
| Test notifications (`POST /v1/notifications/test`) | Allowed | Forbidden |
| Rotate API token | Local owner CLI only | No HTTP endpoint |

Existing pairings keep working. A legacy cookie equal to the master token has device permissions
and upgrades transparently to a fresh device cookie on its next request. A legacy master-token
**bearer** cannot be distinguished from CLI/Mac/MCP owner use and remains owner-authorized.
Re-pairing gives that phone a properly scoped device token. Each device session is bound to the
master token that issued it (an HMAC fingerprint, never the token itself), so local
`redline token rotate --yes` followed by a restart signs out every device session, legacy pairing
and old master token at once, including any session the old process issued after the rotation.
The CLI never needs to locate the service database. Existing sessions are not revoked merely by
upgrading.

`GET /v1/devices` returns an array of `id`, `name`, `kind` (`browser` or `mobile`), `created_at`,
`last_used_at`, and optional `revoked_at`; neither raw tokens nor hashes are returned.
`DELETE /v1/devices/{id}` revokes a session (`204`, or `404` for an unknown ID). Revoked credentials
receive `401`. Pairing redeem optionally accepts `name` and `kind` (default `browser`).

`POST /v1/devices/session` (owner only) exchanges the owner credential for a new device session:
body `{"name":"Pixel 9","kind":"mobile"}`, response `201`
`{"token":"...","device":{"id","name","kind"}}` with `Cache-Control: no-store`. Phones paired
before device sessions hold the owner token; they call this once to move to a scoped, revocable
credential without re-pairing. A device session calling it gets `403`.

## Push registrations

See [push notifications](push-notifications.md) for configuration, privacy, validation, and examples.
`PUT /v1/push/registration` and `DELETE /v1/push/registration` require a device session and
return 204; the owner bearer gets 403. Owner-only `GET /v1/push/registrations` returns
`{registrations: [...], dropped: N, coalesced: N}` with device id/name, platform, provider,
a token suffix (never the full token), and updated_at. Revoked/rotated sessions are excluded.
`POST /v1/notifications/test` accepts `"push": true` to await test wakes and adds
`"push": {"configured": false, "woken": 0}` to its delivery response when push is disabled.
The global notifications enabled switch still applies (409 when off).

## Remote access

Cross-origin requests and untrusted Host headers are rejected independently. Exact Tailscale
MagicDNS hostnames may be added under `api.trusted_hosts` for HTTPS access through Tailscale Serve;
keep Redline bound to loopback and proxy it with `tailscale serve --bg localhost:7436`. Remote
session cookies are Secure. Never expose Redline publicly (for example with Tailscale Funnel).
See [Mobile dashboard setup](mobile.md) for the HTTPS proxy and pairing flow.

## Dashboard

Without the native app, sign a browser in once by opening `/?access_token=<api-token>` on a
loopback address (for example `open "http://127.0.0.1:7436/?access_token=$(<api-token)"` from the
directory holding the token). Redline sets the HttpOnly session cookie and redirects to the clean
URL; a bare `/` request returns `401` until then.

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
GET  /v1/devices
POST /v1/devices/session
DELETE /v1/devices/{id}
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
GET|POST /v1/firesales
DELETE /v1/firesales/{account}
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

Redemption POSTs require the owner, or a device when `api.paired_devices.allow_reset_redemption`
is `true`. Otherwise devices receive **403** naming that setting (including dry runs). The GET
history route accepts both owner and device authentication.

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
or ambiguous local login (multiple configured accounts of the same provider type). Demo servers
refuse both real and dry-run redemption with **409**, without reading credentials or sending requests.

Attempts include `id`, `provider_account_id`, `reset_id`, `request_id`, `trigger`
(`manual`, `automatic`, or `dry_run`), `state` (`pending` or `done`), `outcome`, `code`, `reason`,
`created_at`, and optional `completed_at`. Transport failures remain pending with failure details
so retries reuse the original key. Provider endpoints are fixed, not user-configurable, to prevent
sending bearer tokens to another host. See [redemption safety and caveats](scheduling.md#redeeming-banked-resets).

## Manual firesales (any provider)

Owner and paired-device principals may use all three routes; unauthenticated calls receive **401**.

- `GET /v1/firesales` returns `{"manual":[{"account":"claude-main","started_at":"2026-10-27T12:00:00Z","deadline":"2026-10-27T12:15:00Z","started_by":"owner"}],"eligible":[{"account":"claude-main","label":"claude-main","provider":"claude","weekly_resets_at":null,"reason_unavailable":"weekly usage not reported"}]}`.
  All configured accounts are eligible, including paused accounts. `weekly_resets_at` and
  `reason_unavailable` are nullable. Reasons include `weekly usage not reported`,
  `weekly allowance used up`, and `provider paused`; these warnings do not prevent a start.
- `POST /v1/firesales` accepts `{"account":"claude-main","until":"15m"}` or
  `{"account":"claude-main","until":"weekly_reset"}`. Durations must be **5m–168h** (seven days).
  Returns **201** with the sale object shown in `manual`; restarting an active sale replaces its
  deadline and returns **200**, preserving its original start time and actor. Invalid durations
  receive **400**, unknown accounts **404**, unknown/past weekly resets **409** for `weekly_reset`,
  and demo mode **409**. Reads use cached usage; they never fetch the provider.
- `DELETE /v1/firesales/{account}` returns **204**, or **404** if no sale is active or the account
  is unknown. It does not stop an announcement firesale.

Sales survive restarts and end on stop (`stopped`), expiry (`deadline_passed`), or a weekly reset
moving later by at least one hour (`completed`). Missing telemetry at start means there is no
reset baseline: only stop or deadline can end that sale. Increasing remaining alone does not end it.
`firesale.started` and `firesale.ended` data contain `trigger: "manual"`, `account`, `deadline`,
`started_by` (`owner` or `device:<name>`), and `reason` (empty on start). Notices are claimed after
persistence, delivered outside lifecycle locks, and not retried if delivery fails.

To test a phone's Start action without a new event type, send
`POST /v1/notifications/test` with `{"event":"reset.announced","provider":"claude-main"}`
(owner only). This synthetic **PROMISED** announcement, titled "Test: Start a claude-main firesale?"
and worded about that account rather than Codex, has a window end two hours ahead and adds
`data.manual_account: "claude-main"` and `data.manual_for: "15m"`. Clients should prefer these keys
when present: show **Start firesale (15m)** and POST `{"account":"claude-main","until":"15m"}`
to `/v1/firesales`. Sending the sample without `provider` retains the **COMPLETED** kind, with no
manual Start keys. Sending the test never itself starts work; using its Start action can start a
real manual sale.

## Reset signals and firesales

- `GET /v1/reset-signals` reads the cached report, viable announcement, errors, and last poll
  time without fetching trackers. It adds `firesale: {active, mode, announcement_key, deadline,
  started_at, providers}`; times are RFC3339 UTC or null when inactive. Dashboard JSON includes
  this same state under `reset_signal` when monitoring is enabled.
- `POST /v1/reset-signals/firesale` accepts `{"announcement_key":"announcement:123","action":"start"}`
  or `"action":"stop"`, returning the firesale object. Both owner and device principals may
  start/stop firesales (bearer or cookie); demo mode refuses with **409**. Both actions are idempotent.
  Start returns **409** for a missing/mismatched current viable announcement, a missing/past
  deadline, or firesale off. Only source-backed PROMISED/SCHEDULED announcements qualify, for
  Codex providers. A different active key also returns **409** naming its key/deadline and telling
  the user to stop it first. Stop needs no current tracker announcement: omit the key to stop
  the active sale. An explicit key conflicting with an active sale returns **409** naming it.
  No active sale (including firesale off) is a **200** no-op with `active: false`.

See [announcement firesales](reset-signal-preview.md#announcement-firesales) for modes,
persistence, lifecycle, and notification data.

## Related

- [MCP and agent interface](mcp.md)
- [CLI reference](cli.md)
- [Architecture](architecture.md)
