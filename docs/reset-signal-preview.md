# Public reset-signal preview

`redline reset preview` is a first-pass, read-only report about the likelihood of a broad Codex
reset. It fetches three separate tracker services, but it does not assume their evidence is
independent: when all three reference the same public post, the report shows three mirrors of one
unique original-announcement reference. Trackers may name the post by its URL, its ID, or both;
records that share either identity (directly or through another record) count as one
announcement. A record whose URL and ID name different posts is identified by its URL alone, so
it cannot join two announcements. `direct_source_verified` remains false because the original
post is not fetched by this command.

```bash
redline reset preview
redline reset preview --timeout 8s
```

The command prints JSON containing each normalized source record, source and original-evidence
links, attribution, timestamps, freshness, window certainty, fetch/retry metadata, deduplicated
evidence groups, and plain-language uncertainty. Tracker probabilities are shown only as
attributed predictions. `combined_probability` is always null; a tracker percentage is not
Redline confidence.

## Sources and interpretation

The preview uses documented, unauthenticated GET endpoints only:

- [Reset Beacon API](https://resetbeacon.com/api/docs): `GET /api/forecast?live=1`
- [Will Codex Reset developer reference](https://willreset.com/developers): `GET /api/notify`
- [codex-reset.com developer reference](https://codex-reset.com/developers): `GET /api/forecast`

Requests identify Redline with a fixed user agent, accept JSON, do not follow redirects away from
the validated tracker endpoint, time out, cap response size, and
make at most one bounded retry while preserving `Retry-After`/backoff metadata. A server's valid
retry delay is never shortened: if it will not fit inside the command timeout, the request is
reported as deferred or rate-limited without sleeping or sending an early retry. Reads from
codex-reset.com use its documented one-minute minimum interval as the retry floor, so a normal
short-lived invocation sends only one request after a transient failure. Missing expiry,
future-dated timestamps, expired copies, non-JSON bodies, oversized responses, invalid URLs, and
decode, timeout, deferred-retry, or rate-limit errors remain visible rather than being converted
into optimistic evidence.

Only fresh snapshots can drive the current qualitative state. Source announcements older than 72
hours, expired windows, missing-TTL snapshots, future-dated records, and stale tracker probabilities
remain visible as historical evidence but are excluded from the current assessment. A tracker's
24h/48h probabilities describe the period starting at the snapshot, so whether they are current
depends only on the snapshot's own freshness (its TTL). An announcement the same record names ages
separately. A fresh codex-reset snapshot whose official signal is older than 72 hours, or whose
window has ended, keeps its probabilities in `current_predictions` and can stand as an unlinked
`PREDICTION`. The aged official claim itself is not current. A fresh
correction or withdrawal retracts only its matching deduplicated announcement; an unrelated stale
correction cannot erase a newer event.

When several current records disagree, a record linked to an original announcement outranks one
that names no post. The one exception is a dated completion: an unlinked completion whose source
time (`published_at`, for example a codex-reset completion dated by `last_reset_at`) is strictly
newer than the publication time of every current linked announcement. That reset happened after
those announcements, so it decides the state; a promise published at or after the reset still wins
as a new promise. An unlinked withdrawal or correction names no announcement, so it never retracts
one. Predictions and records dated only by their snapshot (`checked_at`) time never qualify, so a
freshly polled forecast cannot hide a source-backed announcement.

Reset Beacon now publishes event status without numerical forecasts. Its compatibility
`answer.state: "forecast"` means "no new source-backed announcement" and its numerical fields stay
empty, so Redline reports it as `UNKNOWN` (no current evidence), never as a prediction. Any post
it still lists is treated as history, so the record names no announcement and cannot outrank
another tracker's fresh evidence or live probabilities. Reset Beacon documents `checking`, `stale`
and `unavailable` as "missing or outdated current checks". When either `answer.state` or
`publicationState` has one of these values, Redline marks the record `stale`, even if `validUntil`
is in the future and `staleSince` is empty, and adds a freshness warning. Any post the record keeps
is then historical and cannot outrank another tracker's live evidence. It is never reported as a
completion or promise. Reset Beacon's `possible` and `likely` (the compatibility name for "a possible
reset backed by a public source") are reported as `PREDICTION` but publish no percentage. When the
selected prediction carries no numbers, the summary says a possible, source-backed event was
reported and that no probability is published. Redline never reconstructs a percentage.

codex-reset.com's ordinary `mode: "model"` snapshot carries only tracker probabilities and is
reported as an attributed `PREDICTION`. Its documented, stable `last_reset_at` is the tracker's
reset signal ("equivalently, /api/forecast last_reset_at moves forward"). When there is no
official signal, a `last_reset_at` within the 72-hour horizon makes the snapshot a `COMPLETED`
global event dated by that time, with the probabilities still shown with attribution. An older
value is history and leaves the prediction. A future-dated or invalid value is ignored with a
warning. Because it names no post, it never relabels an official signal (for example a promise),
and the warning says so.

The documented `last_reset_at` also takes precedence over the undocumented fields ("Depend only on
documented fields"). `banked_state` is a separate, supplemental fact. It is always kept in the
record's `banked_state` field, and the record is `BANKED` only when nothing documented decides it. A
fresh `last_reset_at`, or a confirmed reset alert, still completes the record, with a warning that
the banked state is kept separately. A banked reset is never itself treated as a completed reset.

The undocumented `latest_alert` remains supplementary. When there is no official signal, the alert
is used only while it has an `id` and its own `source_at` is within the 72-hour horizon. An alert
with no `id`, or an older, future-dated, or undated alert, is noted and does not replace or retract
the live prediction. A usable alert is then applied as follows:
- With a fresh `last_reset_at`, the completion stays unlinked whatever the order of the two times.
  An `active` alert (or a confirmed alert of another kind) is noted and not linked, so its
  announcement is never relabeled as completed. A retracting or unrecognized alert state does not
  withdraw the documented reset. The record carries the retraction separately in
  `retracted_announcement`, and a warning appears when the retraction is newer than the reset.
- With `banked_state` set and no fresh `last_reset_at`, a retracting or unrecognized alert state
  leaves the record `BANKED` and unlinked, and carries the retraction the same way.
- A `confirmed` reset alert names the announcement and is a completed global event (its `corrected`
  flag marks an edited alert message, not a retraction). When a fresh `last_reset_at` is newer than that
  confirmed alert, the alert describes an earlier reset: it is noted and not linked, and the record
  is the unlinked completion dated by `last_reset_at`.
- Otherwise (no fresh `last_reset_at`, no banked state), the alert names the announcement. An
  `active` alert leaves the record unchanged, even when the tracker flags it `corrected` (an edited
  message, noted as a warning). Explicit withdrawals and any unrecognized alert state retract it.

A carried `retracted_announcement` still retracts another tracker's current record of that same
announcement (same ID or canonical URL), which is then reported as `UNKNOWN`. An unrelated
announcement is unaffected. When an official signal is present it names the announcement, so a
matching retracting alert still retracts the official claim, and the banked state is kept only as
the supplemental field. A `Retry-After` too large to represent is treated as a very long wait, never as
permission to retry early.

Normalized states remain distinct: `PREDICTION`, `INTENT`, `PROMISED`, `SCHEDULED`, `COMPLETED`,
`BANKED`, and `UNKNOWN`. A banked reset is not a completed reset, and it does not hide a documented
one. A completed global announcement
does not prove that a particular account reset. Corrections and withdrawals retract an optimistic
summary. Public feed text is untrusted data and is never treated as an instruction.

The captured October 12 post says “Global reset landing by EOD PST time. Enjoy” and links to
`https://x.com/thsottiaux/status/2109812360596865224`. The original X page was not directly
verified for this spike; the claimed source URL, quote, and `2026-10-13T01:03:59Z` publication time
are retained as mirror-provided fields from the public tracker payloads. Because the source phrase
uses an ambiguous timezone label and
two tracker schemas expose a null or absent window, Redline does not promote a tracker's inferred
deadline into an independently verified source deadline. For a promised, scheduled, intended, or
predicted record (for example a Reset Beacon "likely" post, or a codex-reset official signal of
prediction type with a window), the tracker's deadline or window end expires the record's claim
(a fresh snapshot's probabilities stay current, as described above), and the
"reported window expired" note is shown only for such records. A codex-reset model-only snapshot
has no window and is never expired this way. Reset Beacon's interpreted deadline is kept as a
tracker-attributed window end only to expire the record: once that deadline has passed, the record
is historical even if the tracker
keeps serving it as fresh. Completed, withdrawn, and corrected records are not expired by the
deadline, so a completion stays visible and a retraction keeps retracting. A deadline never
upgrades the report from an attributed promise to `SCHEDULED`. Because the source's "EOD PST" is
ambiguous, expiry follows the tracker's reading (for example midnight Pacific daylight time), so a
promise may be treated as historical up to an hour before a literal-PST reading would expire it.

## Optional monitoring and announcement alerts

The service can poll these same feeds independently of `scheduler.enabled`. Monitoring is off by
default. When enabled, the default firesale mode offers an explicit opt-in, rather than starting
work automatically. Opt in with:

```yaml
reset_signals:
  enabled: true
  poll_interval: 10m # minimum 5m; default 10m
  notify: true      # default when enabled; announcement alerts only
  firesale: notify  # off | notify | auto; default notify when enabled
notifications:
  enabled: true
  command: ./scripts/redline-notify
  events: [reset.announced] # explicitly opt in; not a default event
```

An alert requires the report's selected current state to be `PROMISED`, `SCHEDULED`, or
`COMPLETED`, backed by a fresh, unretracted announcement published within 72 hours and linked by
original URL or announcement ID. Predictions (even 100%), intent, banked resets, historical
claims, and unlinked `last_reset_at` completions never alert. Scheduled reports without an
explicit primary window remain promises. Mirrors are deduplicated, not independent confirmation;
`direct_source_verified` remains false.

The report keeps its existing newest-first selection and state. Only when that selected group
passes the alert rules does monitoring choose among all current, linked, viable groups, preferring
more distinct mirror feeds, then an original-post quote, then the stronger kind (`COMPLETED` >
`SCHEDULED` > `PROMISED`), then the newer viable source publication time, then the group key in
ascending order. Every group must independently pass the same freshness, source dating, window,
and retraction rules. A selected nonviable record, matching retraction, or overriding unlinked
completion still blocks an alert; monitoring does not fall back around it. All mirror feeds count
and appear in `feeds`, including mirrors without a usable TTL; they do not independently verify
the post. The group's kind is the strongest viable member's kind, while its quote and account
come from the best viable member: an original-post quote first, then the newest publication time.
Noncurrent mirrors cannot supply the kind or account. One exception for the quote only: when no
viable member has an original-post quote, an unretracted mirror of the same post that does (for
example willreset, which has no TTL) supplies the post's words. This never makes a non-viable
mirror alert on its own or change the kind.

Quotes are untrusted text, stripped of control characters, whitespace-collapsed, and capped at
280 characters. `quote_source` distinguishes `original_post` (Reset Beacon `post.Quote` and
willreset `promise_quote`) from `tracker_summary` (codex-reset `official_signal.summary` and
`latest_alert.summary`, and willreset's fallback `last_reset_text`). The official signal schema
calls its text a summary, not a verbatim post; Redline does not attribute it to the post's author.
Trailing t.co links are removed from original-post quotes. Only `original_post` text is quoted as
the account's words. Other text is explicitly attributed to the supplying tracker (`quote_feed`),
with the claimed source post linked separately. Terminal punctuation inside an original quote is
not followed by a second period. Notification Data includes `quote_source` and the comma-joined
tracker `attribution` strings and comma-joined homepage `attribution_urls`; each source record
also includes its `attribution_url`. The cached API/CLI state retains these URLs so clients can
link the data credits on the same surface. Messages end with the feeds' data credits; when
codex-reset is the only feed, its credit is the explicit link `https://codex-reset.com/`.

For example, the October 25 smoke fixture has a Reset Beacon completion and willreset promise
naming post `2114237083652882932`, versus a newer codex-reset operator-observed completion naming
`2114200291319124163`. The report still selects the newer codex-reset record, but monitoring
chooses the two-mirror group, reports `COMPLETED`, and quotes the original text, not the operator's
note. This preference does not make the mirrors independent evidence.

Announcements are persisted by evidence-group key (canonical URL, otherwise announcement ID)
and kind. A different post has a different key and may alert separately, including when the
best-backed group changes. A new kind alerts only on escalation, not on repeated polls or restart. A fresh matching
withdrawal, correction, or carried retraction produces one follow-up. Missing posts and failed
polls do not imply retraction. Delivery attempts are claimed before sending and failed hooks are
not retried. Retry-After is honoured for the life of the process: delays also defer later polling
cycles and are never shortened. Feed retry deadlines are not persisted across restarts. Startup
waits a random 30–90 seconds before the first poll to avoid immediate requests in restart loops.

Read the cached state without contacting trackers:

```bash
redline reset signals
# or GET /v1/reset-signals on the authenticated service API
```

The response includes the report, current viable announcement (if any), last poll time, errors,
and `firesale: {active, mode, announcement_key, deadline, started_at, providers}`. Deadlines and
start times are RFC3339 UTC (null when inactive).
The dashboard JSON includes an optional `reset_signal` with the same state when monitoring is
enabled; there is no dashboard UI yet. Before the first poll, no report or poll timestamp is
present. Feed snapshots are process-local; announcement delivery deduplication and active firesales
survive restarts. A feed's last successful snapshot is retained through failed or Retry-After-
deferred polls and replaced on success. TTL, source-age, and window checks still apply, so a
retained expired snapshot cannot authorize a new firesale.
`redline reset preview` remains a standalone, live, read-only fetch.

## Announcement firesales

Only a viable, source-backed `PROMISED` or `SCHEDULED` announcement with a future `WindowEnd`
can start a firesale, for configured Codex providers only. A promise without `WindowEnd` has no
deadline: **no firesale is offered or started, and Redline does not invent a deadline**.
`COMPLETED` means the reset already happened; it never starts a firesale. Odds, predictions,
and intent never trigger one, regardless of probability. Trackers are not proof that an individual
account will reset.

- `off`: announcements do not affect firesale/pacing.
- `notify` (default when `reset_signals.enabled`): a usable deadline adds a start offer to
  `reset.announced`. Work starts only after `redline reset firesale start`, or the bearer API
  action, opts into that announcement. Alert delivery itself is not required for an API opt-in.
- `auto`: automatically starts on qualifying evidence, independently of notification settings.

Explicit `notify`/`auto` configuration requires `reset_signals.enabled: true`. Demo mode never
acts. An active firesale unlocks all job tiers regardless of pace gap or overflow; its announced
deadline can shorten (never lengthen) the real weekly scheduling horizon. Pauses, auth circuit
breakers, 5-hour reserves, concurrency and model budgets still apply. No backlog-burning cap is
added. Existing task intervals/repository/enable gates still apply.

```bash
redline reset signals
redline reset firesale start # defaults to the current announcement
redline reset firesale stop  # defaults to the active sale, even during a tracker outage
redline reset firesale start --announcement announcement:123
```

Active sales end on the deadline, matching explicit retraction, completion, user stop, or disable
(`firesale: off` or monitoring disabled). Missing/stale feeds do not end an existing sale: it does
not require continuous tracker uptime. A fresh viable observation of the same key can revise its
stored deadline in either direction, with a log entry but no duplicate start alert. A different
announcement cannot replace an active sale: manual start returns 409 naming the active key and
deadline; stop it first. Auto mode waits for it to end, then may start the still-viable current key.
A completion for a different key ends a sale only when its publication time is newer than the
sale's announcement (or start time if publication is unknown), not an earlier reset's completion.
A fresh, report-selected unlinked completion (such as `last_reset_at`) is also an end-only signal
under that strict time ordering. Stale/MissingTTL, corrected, or withdrawn records cannot end it;
unlinked completions still never trigger an announcement alert or start.

Each sale stores its provider's latest weekly remaining, weekly reset time, and observation time
at start. Later observations lower the persisted remaining baseline as allowance is consumed.
Fresh usage (within `max_snapshot_age`) observed strictly after the original baseline ends that
provider's sale if weekly remaining exceeds this low-water mark by more than 0.02 (two percentage
points), or weekly `resets_at` advances. Both a global refill and the regular weekly rollover
return Redline to normal pacing. No snapshot at start means no usage-based check for that episode;
stale or pre-baseline observations cannot complete it. Disable, deadline, and retraction take
precedence, followed by announcement/tracker completion, then usage refill. A usage-based end is
`reason: completed` with `completion_source: usage_refill`; its message says the weekly allowance
refilled and the reset has landed.

Starts and ends are claimed atomically in SQLite before notification delivery. Repeated polls,
API retries, and restarts never duplicate an episode's alerts; failed hooks are not retried.
Stopping is terminal for auto mode for that announcement/provider, including across restart.
An explicit start can override your stop. Disabling ends the episode but does not permanently
block auto mode after re-enabling; notify mode needs a fresh opt-in. Stopping with no active sale
is a successful no-op. Stopping never needs a current viable tracker snapshot.

## Deliberate non-goals

The standalone preview remains read-only and unauthenticated. Neither monitoring nor a firesale
redeems a banked reset or proves a particular account refilled; normal usage telemetry remains
the authority for available allowance.

Any later advisory experiment should be a separate, off-by-default design: it would accept only
validated, deduplicated normalized evidence and return a bounded explanation with original links,
tracker correlation, freshness, ambiguity, applicability, and uncertainty. It must use an existing
subscribed Codex allowance only, have strict time/token/material-event caching limits, use no tools,
and fall back to this deterministic report. Such prose could never authorize work or override
source facts, user opt-in, or Redline's existing safety policy. No such advisory is invoked or
wired in this phase.
