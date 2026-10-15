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
  flag marks an edited alert message, not a retraction).
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

## Deliberate non-goals

This command has no account-awareness or action path. It does not poll, persist feed state, send
notifications, inspect or select queued jobs, assess reserves or concurrency, mutate configuration,
call the scheduler, or start a run. Applicability to any individual account remains unknown.

Any later advisory experiment should be a separate, off-by-default design: it would accept only
validated, deduplicated normalized evidence and return a bounded explanation with original links,
tracker correlation, freshness, ambiguity, applicability, and uncertainty. It must use an existing
subscribed Codex allowance only, have strict time/token/material-event caching limits, use no tools,
and fall back to this deterministic report. Such prose could never authorize work or override
source facts, user opt-in, or Redline's existing safety policy. No such advisory is invoked or
wired in this phase.
