# Runs, logs, and notifications

## Run output

Completed runs retain a concise result, delivery artifacts, and bounded formatted output so useful
work does not disappear just because it ran unattended. Output can be inspected without reading
artifact paths directly. Responses are tail-bounded to 64 KiB and may only resolve regular files
beneath the configured `run_artifacts_dir`; paths and symlinks that escape that root are rejected.

```bash
redline run logs <run-id>
redline run logs <run-id> --stream stderr --tail-bytes 8192
redline run logs <run-id> --json
```

## Lifecycle events

The lifecycle timeline records workspace preparation, harness execution, finalization, cleanup,
and the terminal run result. Task prompts are intentionally omitted. Execution-profile fields,
including lifecycle commands, are retained for reproducibility, so secrets should be passed via
the environment rather than embedded in profile command strings.

```bash
redline run events <run-id>
redline run logs <run-id> --stream prepare_stderr
redline run logs <run-id> --stream finalize_stdout
```

See [Architecture](architecture.md#execution-lifecycle) for the lifecycle state machine.

## Notifications

Native macOS alerts can be enabled from the Redline app and cover job starts, completions,
failures, banked-reset reminders and redemption outcomes, and firesale starts/ends. Reset and
firesale alerts come from the app itself, using recorded deliveries, not an external notifier.
The app polls on its 20-second refresh cadence using the local API token. It seeds existing
history silently on first use and persists its highest delivery ID across restarts. Clicking a
reset or firesale alert opens the dashboard. Only redeem-now reminders and failed redemptions
play a sound (subject to macOS notification settings).

Notifications are disabled by default. Set `notifications.enabled: true` to record deliveries.
An empty or omitted `command` enables **record-only mode**: deliveries are marked `delivered`
without running an external hook, ready for the native Mac app to display. You must still list
the desired reset/firesale events in `events` and authorize notifications in the app.
When `command` is set, Redline also invokes that trusted local command with a versioned event
document on stdin. Reset/firesale payloads include an optional, human-readable `title` plus
`message`; existing consumers can continue using the unchanged data fields. Supported events are `run.started`,
`run.completed`, `run.failed`, `scheduler.error`, `reset.expiring`, `reset.redeemed`,
`reset.redeem_failed`, `reset.announced`, `firesale.started`, and `firesale.ended`. When `events` is omitted or
empty, only `run.started`, `run.completed`, `run.failed`, and `scheduler.error` are sent; list the
`reset.*` and `firesale.*` events explicitly to receive them. Hooks should ignore event types they
do not recognize.

```yaml
notifications:
  enabled: true
  command: ./scripts/redline-notify
  timeout: 30s
  events: [run.started, run.completed, run.failed, scheduler.error, reset.expiring, reset.redeemed, reset.redeem_failed, firesale.started, firesale.ended]
```

The hook also receives `REDLINE_EVENT_TYPE`, `REDLINE_PROVIDER_ACCOUNT_ID`, `REDLINE_TASK_ID`, and
`REDLINE_RUN_ID`. Delivery failures are persisted but never alter the associated run or scheduler
outcome. A service restart marks an indeterminate pending delivery failed instead of retrying a
possibly non-idempotent hook.

### Public reset announcements

`reset_signals.enabled: true` opts into independent public-feed monitoring (default poll interval
10m, minimum 5m). `reset_signals.notify` defaults to true when enabled, but notifications also
require `notifications.enabled` and explicitly listing `reset.announced` in `notifications.events`.
This global event has no provider account ID. It alerts only on the selected fresh, linked,
unretracted promise, schedule, or completion published within 72 hours. Odds-only predictions
never alert, however high. With `reset_signals.firesale: notify` (the enabled default), a promise
or schedule with a future deadline offers a firesale, but does not start one until opt-in.
`auto` starts automatically; `off` leaves scheduling unchanged. Completion alerts never start one.

Data fields are strings: `key` (`announcement:<status id>`, stable across mirrors), `kind`, `url`,
`account`, `quote`, `quote_source` (`original_post` or `tracker_summary`), `published_at`,
`window_end`, `window_certainty`, `feeds` (comma-separated), `attribution` and `attribution_urls`
(comma-separated tracker credits and homepage links), `direct_source_verified` (`false`), and
`retracted`. Missing values are empty strings. The original post is not fetched. Quotes are untrusted
plain text, control-stripped, whitespace-collapsed, and capped at 280 characters. Tracker-interpreted
deadlines are explicitly attributed in messages.

Like `reset.expiring`, delivery is claimed persistently **before** calling the hook: at most one
attempt per announcement key and kind, with another only when the kind escalates (promise to
schedule or completion). A fresh matching withdrawal, correction, or retraction gets one follow-up
per key, with `retracted: "true"`. Restarts and repeated polls do not duplicate attempts; failed
hooks are recorded by the notification service but not retried. Disabled/filtered notifications
do not claim delivery. See [public reset signals](reset-signal-preview.md#optional-monitoring-and-announcement-alerts)
for cached API/CLI access and interpretation limits.

### Firesale transitions

Announcement-triggered sales emit these same events once per provider episode, using durable
atomic start/end claims rather than scheduler flips. Data includes `trigger: reset_announcement`,
`announcement_key`, `deadline` (RFC3339 UTC), `mode` (`notify`/`auto`), and `reason` on end:
`deadline_passed`, `retracted`, `completed`, `stopped`, or `disabled`. A completed end also adds
`completion_source`: `announcement` for linked source evidence, or `tracker_reset` for a fresh,
report-selected unlinked completion newer than the sale's announcement. For example:

- **Codex firesale started:** Tibo announced a reset by Sun 08:15 UTC. Running queued work before it lands.
- **Codex firesale ended:** The reset announcement was retracted. Back to normal pacing.

`reset.announced` adds string-valued `firesale_mode` and `firesale_action`: `start` for a notify
mode offer with a usable deadline, `active` when auto has started that announcement, otherwise
an empty action. The Mac app continues to display notifications only; no Mac action UI is added.
Stopping/starting uses the bearer API or `redline reset firesale start|stop`.


Automatic dispatch emits `firesale.started` and `firesale.ended` on provider state transitions,
using the previous recorded automatic usage decision. Manual evaluations and skipped usage checks
do not change this state. Data includes `pace_gap`, `overflow`, `effective_reset_at` (RFC3339 UTC),
`effective_reset_source`, and `unlocked_tier`. Messages describe unused capacity (or the pace gap
when overflow is unavailable) and the effective reset deadline in UTC. See [firesale scheduling](scheduling.md#effective-horizon-and-firesale).

### Banked-reset redemptions

Manual and automatic redemption emit `reset.redeemed` on provider-acknowledged success, or `reset.redeem_failed`
for failed, unavailable, cooldown, or gone outcomes. Dry runs and `nothing_to_reset` emit neither.
Each event includes string-valued data: `reset_id`, `request_id`, `outcome`, `code`, `reason`, and
`verified` (`true` or `false`), plus `weekly_remaining` when a post-redeem snapshot is available.
Replayed results never notify. A successful response with no observed weekly refill sends only
`reset.redeemed`, with `verified: "false"` and a note to check usage; it never assumes allowance
has refilled. In automatic (`on_expiry: redeem`) mode, retries may emit further failure events;
an attempted reset that expires unused also emits one `reset.redeem_failed` with `reset_id`,
`expires_at`, and `reason: "expired unused"`. This final notice is deduplicated by provider and
expiry with kind `expired_unredeemed`, including across restarts and vanished credit details.
Disabled or filtered notifications do not claim it. See
[automatic redemption and retry rules](scheduling.md#redeeming-banked-resets).

### Banked-reset reminders

The usage monitor checks banked reset deadlines independently of `scheduler.enabled`.
Per-provider `banked_resets` defaults are `remind_before: [24h]`, `on_expiry: notify`, and
`redeem_lead: 1h`. Durations must be positive. `remind_before: []` disables ordinary reminders;
`on_expiry: off` disables the expiry action. `notify` asks the user to redeem at the action
deadline; opt-in `redeem` automatically attempts redemption instead, without a `redeem_now`
notification when automatic redemption is enabled. `redeem` is rejected at config load unless
`usage_monitor.enabled: true`, and for providers pinned to `usage_source: openusage` (which has no
itemized reset telemetry). Redeem mode suppresses `redeem_now` only for resets automatic
redemption will actually attempt before they expire. Every other reset gets `redeem_now` as in
notify mode, for example when automatic redemption cannot run at all (demo mode or multiple
accounts of the provider type), or a Claude grant is unclaimable, not usable now, requires a
limit, or stays in cooldown or retry backoff past its expiry.
Earlier ordinary reminders still send. Automatic redemption does not require notifications,
scheduler enablement, or an unpaused provider.

`reset.expiring` includes `provider_account_id`, a human-readable `message`, and string-valued
`data` fields: `reset_id`, `expires_at` (RFC3339 UTC), `lead` (duration), `action` (`remind` or
`redeem_now`), and `count`. One notification is sent per distinct expiry; `count` sums remaining
resets sharing it. `reset_id` is informational (the first credit ID, or `aggregate` when details
are unavailable or their positive remaining counts disagree with the authoritative aggregate).
Spent details are ignored. `lead` is the configured threshold; the message describes time
actually remaining.

Each (provider account, expiry, kind) is claimed persistently before delivery, so restarts and
switching between aggregate and itemized snapshots do not double-send.
Missed reminder thresholds produce only the shortest-lead applicable reminder, not a burst.
Once the expiry action is due, it replaces ordinary reminders for that deadline.
Expired resets are ignored. Disabled or filtered notifications do not claim notices, so enabling
`reset.expiring` later can still send relevant reminders. Failed deliveries are not retried.

Notification commands, workspace prepare/finalize hooks, and the **Custom command** harness are
run through `/bin/sh -lc` on macOS and Linux and through `cmd /C` on Windows. Windows hooks that
need a POSIX shell should invoke one explicitly (for example `bash -lc "..."` from Git Bash or WSL).

```bash
redline notification list
redline notification test
redline notification test --event firesale.started --provider codex-main
redline notification test --event all --json
```

`notification test` records and delivers a synthetic event through the same service and message
builders without contacting providers or redeeming anything. It defaults to `reset.expiring` and
the first Codex account (otherwise the first account, sorted by ID). `--event all` sends each of
the ten supported event types about one second apart. Titles start with `Test: ` and data includes
`test: "true"`. Tests bypass the event subscription list, but disabled notifications return HTTP
409 with instructions to enable them. The API is `POST /v1/notifications/test` with optional
`event` and `provider` fields; it requires the bearer API token, not a pairing/browser cookie.
It returns the delivery record, including `id`, `status`, and `last_error` on failure. Human CLI
output includes the sent title/message and delivery status; `--json` prints the record (an array
for `all`). A failed delivery exits non-zero. Native run alerts still use real run transitions;
only reset/firesale test deliveries are displayed by the app. The `reset.announced` sample is a
fake post with a placeholder link; it is global and ignores `--provider`.

## Health

```bash
redline health --window 24h
```

Detailed health reports active/recent run counts, dispatch errors, and notification failures.
The lightweight `/v1/health` probe remains independent of recent operational failures.

## Related

- [Scheduling](scheduling.md#operational-history)
- [CLI reference](cli.md)
