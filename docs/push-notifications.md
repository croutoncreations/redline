# Push notifications

The desktop push layer is provider-neutral and disabled until a transport is configured.
The relay supports FCM HTTP v1 content-free wake hints. See
[push self-hosting](push-self-hosting.md) for Firebase, relay-secret, and app-build setup.

## Privacy

A push is only a wake-up: the relay and push provider see an opaque device token, a delivery
ID, and a coarse kind (`reset`, `firesale`, `job`, or `test`), plus routing metadata (platform
and provider). They never receive notification titles, messages, prompts, or other content.
The device fetches notification text directly from the desktop using its authenticated
`GET /v1/notifications?after_id=CURSOR` API. Maintain a cursor and fetch everything after it;
multiple recorded deliveries can share one wake-up during bursts.

Push tokens are stored as-is because sending requires them. They are not desktop API
credentials. Never share them unnecessarily; list responses expose only a short suffix, and
transport logs never include tokens, shared secrets, or relay response bodies. Revoking a
device or rotating the master API token and restarting prevents subsequent sends to that
session. An already in-flight wake cannot be recalled.

## Configuration

```yaml
notifications:
  enabled: true
  command: "" # record-only; a hook can also be configured
  events: [reset.expiring, firesale.started, run.failed]
push:
  relay_url: https://relay.example
  auth_token_file: /absolute/path/to/push-shared-secret
```

Both push keys default to empty. An empty `push.relay_url` selects the disabled transport.
Otherwise, the URL must use HTTPS (HTTP is permitted only for loopback), with no URL
credentials, query, or fragment. `push.auth_token_file` must exist, be a regular file with
mode **0600**, and hold a nonempty single-line shared secret. It is read at startup and sent
as `Authorization: Bearer ...`; never put the secret in YAML. Restart after changing it.

The desktop posts to `{relay_url}/v1/push`:

```json
{"provider":"example","token":"opaque-device-token","platform":"android","hint":{"delivery_id":123,"kind":"reset"}}
```

Only recorded deliveries are dispatched, so existing event subscriptions and the global
`notifications.enabled` switch govern push too. Sending is asynchronous, at most once,
with four workers, a 256-wake queue, and a 10-second timeout per send. A wake still queued for
the same device is coalesced (one already being sent is not); queue overflow drops wakes.
A relay that reports push unconfigured is logged once per outage. Owner-visible counters report both.
There is no persistent retry queue: devices should also fetch on reconnect or app activation.
Shutdown cancels queued/in-flight sends. Failed sends do not change notification hook status.

A relay 404 or 501 logs once and suppresses sends for one hour. A 410 or an error with code
`unregistered` removes the affected registration (not a replacement registered during sending).
Other errors are logged without sensitive data and are not retried. Redirects are not followed.

## Registration API

Use a paired **device-session bearer or cookie**, not the owner/master bearer:

- `PUT /v1/push/registration` with
  `{"platform":"android","provider":"example","token":"opaque-device-token"}`.
  Returns **204**, creating or replacing that session's one registration. Platform must be
  `android` or `ios`, provider must match `[a-z0-9_-]{1,32}`, and token must contain 1–4096
  characters. Invalid input returns **400**; the token is preserved verbatim.
- `DELETE /v1/push/registration` returns **204**, including when already absent.
- The owner/master bearer receives **403** on both registration mutations.

Owner-only `GET /v1/push/registrations` returns:

```json
{"registrations":[{"device_session_id":"opaque-id","device_name":"Phone","platform":"ios","provider":"example","token_suffix":"abcd","updated_at":"2026-07-16T18:00:00Z"}],"dropped":0,"coalesced":0}
```

Only active sessions issued under the current master token are listed. A short token's suffix
is shortened further so the full token is never returned. Counters are in-memory and reset on
restart. Devices receive **403** on this list endpoint. Revoked/rotated credentials receive
**401** from authentication.

## Testing

`redline notification test --push` records a synthetic delivery, runs the ordinary notification
hook, and waits for test wakes. It respects `notifications.enabled` but bypasses subscriptions,
like the ordinary notification test. Output reports devices woken or `push not configured`;
`--json` includes `push: {"configured":true,"woken":2}` on the delivery. A successful wake means
the transport accepted it, not that the device received it. `--event` still selects the local
sample, while the push hint's kind is always `test`.

The corresponding owner-only API is `POST /v1/notifications/test` with `{"push":true}` (and
optional `event`/`provider`). Disabled notifications return **409** without recording or waking.
