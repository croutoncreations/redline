# Public reset-signal preview

`redline reset preview` is a first-pass, read-only report about the likelihood of a broad Codex
reset. It fetches three separate tracker services, but it does not assume their evidence is
independent: when all three reference the same public post, the report shows three mirrors of one
unique original-announcement reference. `direct_source_verified` remains false because the
original post is not fetched by this command.

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

Requests identify Redline with a fixed user agent, accept JSON, time out, cap response size, and
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
deadline into an independently verified source deadline.

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
