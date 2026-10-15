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
remain visible as historical evidence but are excluded from the current assessment. A fresh
correction or withdrawal retracts only its matching deduplicated announcement; an unrelated stale
correction cannot erase a newer event.

Reset Beacon now publishes event status without numerical forecasts. Its compatibility
`answer.state: "forecast"` means "no new source-backed announcement" and its numerical fields stay
empty, so Redline reports it as `UNKNOWN` (no current evidence), never as a prediction. Such a
record names no announcement and cannot outrank another tracker's fresh evidence or live
probabilities.

codex-reset.com's ordinary `mode: "model"` snapshot carries only tracker probabilities and is
reported as an attributed `PREDICTION`. Its documented, stable `last_reset_at` is the tracker's
reset signal ("equivalently, /api/forecast last_reset_at moves forward"). A `last_reset_at` within
the 72-hour horizon makes the snapshot a `COMPLETED` global event dated by that time, with the
probabilities still shown with attribution. An older value is history and leaves the prediction; a
future-dated or invalid value is ignored with a warning. Because it names no post, it is applied
conservatively: it never relabels an official signal (for example a promise), never replaces a
banked-reset record, never overrides a latest-alert retraction, and never completes an
alert-named announcement published after the reset; each of those cases is noted as a warning.
The undocumented `latest_alert` remains supplementary. When it has no official signal, its latest alert supplies
the announcement only while the alert's own `source_at` is within the 72-hour horizon; an older,
future-dated, or undated alert is noted and does not replace the live prediction. A `confirmed`
reset alert is a completed global event (its `corrected` flag marks an edited alert message, not a
retraction), but it never replaces a banked-reset record.
An `active` alert leaves the record unchanged, even when the tracker flags it `corrected` (an
edited message, noted as a warning); explicit withdrawals and any unrecognized alert
state retract it. A `Retry-After` too large to represent is treated as a very long wait, never as
permission to retry early.

Normalized states remain distinct: `PREDICTION`, `INTENT`, `PROMISED`, `SCHEDULED`, `COMPLETED`,
`BANKED`, and `UNKNOWN`. A banked reset is not a completed reset. A completed global announcement
does not prove that a particular account reset. Corrections and withdrawals retract an optimistic
summary. Public feed text is untrusted data and is never treated as an instruction.

The captured October 12 post says “Global reset landing by EOD PST time. Enjoy” and links to
`https://x.com/thsottiaux/status/2109812360596865224`. The original X page was not directly
verified for this spike; the claimed source URL, quote, and `2026-10-13T01:03:59Z` publication time
are retained as mirror-provided fields from the public tracker payloads. Because the source phrase
uses an ambiguous timezone label and
two tracker schemas expose a null or absent window, Redline does not promote a tracker's inferred
deadline into an independently verified source deadline. For a promised, scheduled, or intended
record, Reset Beacon's interpreted deadline is kept as a tracker-attributed window end only to
expire the record: once that deadline has passed, the record is historical even if the tracker
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
