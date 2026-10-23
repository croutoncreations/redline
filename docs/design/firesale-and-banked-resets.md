# Design: firesale mode and banked resets

Status: accepted, milestones 1–5 implemented (2026-10-22). Rollout: dry run both providers,
then a manual first redemption, then enable `on_expiry: redeem`.

## Problem

Redline only dispatches work when allowance would otherwise go unused, but two cases still waste
capacity:

1. **Large surpluses near a reset.** When far more allowance is left than ordinary cooldowns and
   one-at-a-time dispatch can consume, the `expiring` tier unlocks but each task still waits out
   its normal `min_interval`, so most of the surplus lapses anyway. Run history shows cooldowns
   are the dominant reason RUN decisions find nothing to dispatch.
2. **Banked resets that lapse.** Claude (`cedar_ember` grants) and Codex (rate-limit reset
   credits) can hold one-time resets that refill usage windows. Redline displays the count and
   soonest expiry but never acts on them. A reset that expires unused is pure loss; redeeming it
   shortly before it expires, after the current allowance has been burned down, is always at least
   as good.

## Goals

- A `firesale` dispatch tier and state for "a lot of allowance is about to be lost; pull out all
  the stops", with per-job faster intervals and higher provider concurrency while it lasts.
- Pace and overflow calculations measured against the **effective end of window**: the earliest
  expected reset, whether the real weekly boundary or a reset Redline will itself apply.
- Reminders before a banked reset expires (`reset.expiring`), configurable lead times.
- At expiry time: `off`, `notify`, or automatically `redeem`.
- An architecture that also accepts future *predicted* global resets (for example a high-confidence
  announced Codex reset) without another rewrite.

## Non-goals

- Counting banked resets as extra capacity. Resets that will not be applied by Redline before the
  current weekly reset are held for the user and never change scheduling.
- Building the predicted-global-reset source. Only the seam is built now.
- Automatically redeeming resets for capacity the user wants interactively. Automatic redemption
  waits until the configured lead before expiry; manual CLI/API redemption can spend a selected
  reset at any time.

## Concepts

### Reset events and the effective horizon

A `ResetEvent` describes a moment at which an allowance window is expected to refill:

```go
type ResetEvent struct {
    Source     ResetSource // weekly | banked_reset | predicted_global
    At         time.Time
    Clears     []string    // "five_hour", "weekly"
    Confidence string      // high | medium | low
    Detail     string      // human-readable, e.g. grant id or announcement link
}
```

The **effective horizon** is the earliest event, at or before the real weekly reset, that clears
the weekly window. Sources:

| Source | Included when |
|---|---|
| `weekly` | Always (the real weekly reset). |
| `banked_reset` | The next auto-redeemable reset clears the weekly window and can be attempted before the real weekly reset. Requires `banked_resets.on_expiry: redeem`, usage monitoring enabled, one account of that provider type, and not demo mode. Timing respects redeem lead, provider throttle, retry deadline, cooldown, and provider lockout. |
| `predicted_global` | Future. A reset-signal source above a configured confidence. |

A banked reset that will only be *notified about* never shortens the horizon: if the user does not
redeem it, nothing refills. A reset that clears only the five-hour window does not move the weekly
horizon.

`decision.Input` gains `ResetEvents []ResetEvent`. `Evaluate` computes the horizon and uses it in
place of `Snapshot.Weekly.ResetsAt` for slot construction, weekly pace gap, pace thresholds, and
overflow. `ProjectTriggerAt` uses the horizon as its loop bound. The result reports
`effective_reset_at` and `effective_reset_source` so every decision says which deadline it used.
Model-scoped pools (`model:*:weekly`) keep their own reset; banked resets only shorten the
account-level weekly horizon.

### Firesale

Firesale is a computed state plus a fourth dispatch tier, ranked above `expiring`:

```
behind (1) < well_behind (2) < expiring (3) < firesale (4)
```

A policy may set `firesale_trigger` (fraction of weekly allowance). Firesale is active when

```
max(pace_gap, overflow) >= firesale_trigger
```

both measured against the effective horizon. `overflow` is only defined when a five-hour window
exists (the allowance that cannot be consumed even if every remaining window is used fully).
Omitting `firesale_trigger` disables firesale for that policy. Bundled defaults: `early` 0.35,
`standard` 0.45, `late` omitted.

Firesale never bypasses safety: the five-hour rolling reserve, UNKNOWN fail-closed, pause state,
pool limits, repository-change gates, and enable state all still apply.

While firesale is active for a provider:

- **Tier.** `UnlockedTier = firesale`; tasks tiered `firesale` become eligible.
- **Per-job interval.** A task's optional `firesale_min_interval` replaces `min_interval` as its
  cooldown. Unset means the normal `min_interval` applies. `require_repo_change` is never relaxed.
  The faster interval only applies when the provider-level (base) decision is firesale.
- **Concurrency.** Provider `firesale_max_concurrent_runs`, when greater than the effective
  concurrency, raises the provider cap. Pool and runtime limits still apply.
- **Prompt re-dispatch.** When a run on a firesale provider finishes, the scheduler loop is
  triggered immediately instead of waiting for the next poll.
- **Notifications.** `firesale.started` and `firesale.ended` on state transitions (debounced per
  provider; transitions are detected from consecutive automatic decisions).

### Banked reset details

The snapshot keeps its aggregate fields (`banked_resets`, `banked_resets_expire_at`) and gains an
itemized list:

```go
type BankedReset struct {
    ID              string     // Claude grant id / Codex credit id
    ExpiresAt       *time.Time
    Clears          []string   // normalized: "five_hour", "weekly"; empty = unknown
    UsableNow       *bool
    RequiresLimit   *bool
    CooldownUntil   *time.Time
    ResetsLeft      int
}
```

- **Claude:** parsed from the `cedar_ember` block already fetched (`grants[]`, `next_grant_id`,
  `cooldown_until`). `seven_day` and `seven_day_overage_included` normalize to `weekly`. Only
  `next_grant_id` is claimable.
- **Codex:** the `wham/usage` count is free. Per-credit details come from
  `GET https://chatgpt.com/backend-api/wham/rate-limit-reset-credits`, which is rate-limited per IP,
  so it is fetched only when the count is non-zero, cached for at least 30 minutes, and backed off
  on 429. That backoff applies only to further detail lookups; it never locks out `wham/usage`
  collection. Codex credits do not report `clears`; Redline treats them as clearing the weekly and
  five-hour windows. Only the weekly refill is observed (redeeming starts a new weekly window);
  the five-hour refill is assumed.

Details are persisted with the usage snapshot (JSON column) so stored-snapshot evaluation and the
dashboard see the same data.

## Configuration

```yaml
policies:
  standard:
    firesale_trigger: 0.45

providers:
  claude-main:
    firesale_max_concurrent_runs: 3
    banked_resets:
      remind_before: [24h]   # reset.expiring reminders; [] disables
      on_expiry: notify      # off | notify | redeem
      redeem_lead: 1h        # when to act before expiry
```

Defaults: `remind_before: [24h]`, `on_expiry: notify`, `redeem_lead: 1h`. Automatic redemption is
always opt-in. Notifications still require `notifications.enabled`, and the new `reset.*` and
`firesale.*` events must be listed in `notifications.events`. The default list stays the
pre-firesale four events, so upgrading never sends new event types to existing hooks.
`on_expiry: redeem` was added by the milestone that implements it, so configuration never
accepted behavior that did not exist yet.

Tasks gain `firesale_min_interval` (API, CLI `--firesale-interval`, MCP, dashboard) and accept
`dispatch_tier: firesale`.

## Reminders and expiry actions

The usage monitor (which runs whether or not automatic dispatch is enabled) evaluates banked reset
deadlines on each cycle:

- For each unexpired reset and each `remind_before` lead, emit `reset.expiring` once when
  `now >= expires_at − lead`. If multiple thresholds have passed, emit only the shortest-lead
  applicable reminder. Once the expiry action is due, ordinary reminders for that deadline are
  suppressed. Resets sharing an expiry produce one notification with a count. Without itemized
  details, or when cached details disagree with the authoritative count, use the aggregate expiry.
  Deduplicated before delivery in `banked_reset_notices`, keyed by
  `(provider_account_id, expires_at, kind)` so aggregate and itemized views of the same deadline
  never repeat. Disabled or filtered notifications do not claim notices; delivery failures are not
  retried.
- At `expires_at − redeem_lead`:
  - `notify`: emit `reset.expiring` with `action: redeem_now` (deduplicated the same way).
  - `redeem`: attempt redemption (below), retrying until expiry on transient failures with
    persisted backoff (at least five minutes per provider; 30 minutes for `nothing_to_reset`).
    Respect cooldowns and 429 lockouts; `redeemed` and `gone` stop retries. Do not send
    `redeem_now` for a deadline only when automatic redemption is eligible for every reset at that
    deadline (it will attempt them before expiry); otherwise fall back to `notify` for it.
    Failed resets that expire unused notify once via `expired_unredeemed`.
    A pending real request of any trigger is retried first with its stored key, even if telemetry
    no longer lists it. An unresolved request at expiry is finalized as `unknown` and notifies
    "redemption request outcome unknown at expiry; check the provider", never "expired unused".
    If its expiry cannot be recovered, the same finalization occurs 24 hours after its first attempt.
    Dispatch pause does not block redemption: an expiring reset should be used rather than lost.

## Redemption

Prior art: OpenUsage #1413 (Claude, live-verified claim), OpenUsage `CodexResetClaimService`,
agent-usage-bar #46, ClaudeUsageWidget #69.

### Claude

```
POST https://api.anthropic.com/api/organizations/{orgUUID}/reset_rate_limits
Authorization: Bearer <OAuth access token>
User-Agent: <same Claude Code UA as the grants read>
{"program":"cedar_ember","grant_id":"<next_grant_id>","request_id":"<idempotency key>"}
```

- `orgUUID` from `GET /api/oauth/profile`, cached.
- `grant_id` must match `^[a-z0-9_-]{1,40}$`, `request_id` `^[A-Za-z0-9_-]{1,64}$`; both checked
  before sending.
- Results: `reset` and `already_used` → redeemed; `not_limited` → nothing to reset;
  `cooldown` / `paused` → retry after `cooldown_until`; `ineligible`, `unavailable` → failed.
  `reset_unconfirmed`, `stamp_indeterminate`, and unrecognized values are indeterminate: never
  assumed successful, but the request stays pending and retries reuse the same `request_id`, so a
  response that followed an applied redemption cannot lead to a second spend. Unrecognized Codex
  codes are handled the same way.
- Uses the read-only credential path. If the token needs refreshing, redemption fails and falls
  back to a notification asking the user to redeem manually.
- Never redeems when more than one Claude account is configured.

### Codex

```
POST https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume
Authorization: Bearer <token>; Chatgpt-Account-Id: <account id when known>
OpenAI-Beta: codex-1
{"redeem_request_id":"<uuid>","credit_id":"<credit id>"}
```

- Credit chosen: the soonest-expiring credit.
- `redeem_request_id` is a UUIDv5 of `(account id, credit id)` so the same credit always gets the
  same key. If that key already finished without redeeming (e.g. `nothing_to_reset`), the retry
  derives the next deterministic generation of the key and records a new attempt, so earlier
  results are never overwritten and an answered key is never resent.
- Outcomes (`code`): `reset`, `already_redeemed` → redeemed; `nothing_to_reset` → not spent;
  `no_credit` → gone; anything else → indeterminate (kept pending, retried with the same key).
- ChatGPT logins only; API-key logins are refused.

### Safety

- A `banked_reset_redemptions` table records each attempt: provider, reset id, request id,
  trigger (`manual` | `automatic` | `dry_run`), outcome, raw result code, timestamps. The request id
  row is written **before** the request is sent; retries reuse it, so a crash cannot spend twice.
- Single flight per provider.
- After a successful redemption, usage is force-refreshed and stored like any other observation.
  Redline reports whether the refill is verified (weekly remaining increased or the weekly reset
  moved later). A redeemed reset counts as applied for the effective horizon only once verified.
  Each redemption sends one `reset.redeemed` event carrying `verified`; an unverified refill adds a
  note about the known Codex bugs (enforcement still on the old window, failures for single-window
  accounts) instead of being hidden or reported as success.
- Shares the provider's 429 lockout with usage reads.

### Manual and dry-run paths

- `redline reset redeem --provider <id> [--dry-run] [--json]` and
  `POST /v1/providers/{id}/banked-resets/redeem`.
- `--dry-run` sends a syntactically valid but nonexistent id (`redline-dryrun` grant / random
  credit id). Expected responses are Claude `ineligible/unknown_grant` and Codex `no_credit`; both
  spend nothing and prove the auth, URL, headers, and body shape end to end.
- Rollout: dry run both providers, then the first real redemption is manual (the user's next
  expiring reset), then `on_expiry: redeem` can be enabled.

## Milestones

1. **Reset details — implemented.** Itemized `BankedReset` parsing for Claude grants and Codex credits (detail
   endpoint, cached), persisted with snapshots, exposed in the API.
2. **Reminders — implemented.** `banked_resets` config, `reset.expiring` reminders and the
   expiry-time `notify` action, deduplicated. `redeem` is opt-in automatic redemption (milestone 5).
3. **Effective horizon and firesale — implemented.** `ResetEvent` seam, horizon-based
   evaluation, `firesale` tier/state, `firesale_trigger`, `firesale_min_interval`,
   `firesale_max_concurrent_runs`, immediate re-dispatch, firesale notifications, CLI/MCP/dashboard.
4. **Redemption engine — implemented.** Claude and Codex clients, attempts table, manual and dry-run CLI/API.
5. **Automatic redemption — implemented.** `on_expiry: redeem`, banked-reset events in the horizon, post-redeem
   verification.

## Open questions

- Which windows a Codex credit refills is not documented; assumed both until observed.
- Whether Anthropic counts `already_used` as success for a reused `request_id` (assumed yes, as
  OpenUsage does).
