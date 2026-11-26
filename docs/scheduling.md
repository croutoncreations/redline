# Scheduling and the allowance model

This guide explains how Redline decides whether to release deferred work: dispatch policies and
tiers, automatic scheduling, window-cost calibration, empirical token capacity, and the
operational history Redline keeps for every attempt.

## Decisions: RUN, WAIT, UNKNOWN

Redline records why each provider chose `WAIT`, `RUN`, or fail-closed `UNKNOWN`. Near-expiry is a
specific `RUN` case: unused allowance is approaching reset while the configured reserve remains
held. `UNKNOWN` is returned when telemetry is stale or unavailable; Redline never dispatches on an
`UNKNOWN` decision.

```bash
redline decision --provider codex-main
redline decision --provider claude-main --json
```

## Claude Team seats without a reported weekly cap

Such seats display weekly usage as **Not reported** and hold dispatch by default.
Opt in per account in the main config's provider entry or managed `accounts.yaml`:

```yaml
weekly_policy: session_only
session_reserve: 0.50
```

`weekly_policy` accepts only `""` (default hold) or `session_only` (Claude only).
`session_reserve` is the fraction of each 5-hour window Redline never spends: it
must be in `[0, 1)`, is valid only with `session_only`, and defaults to **0.50**.
An explicit zero is allowed but removes that protection. This policy applies only
when the weekly limit is not reported; normal weekly scheduling is unchanged.

Fresh session room above the reserve admits only `behind`-tier work. An unstarted
session is full (100% remaining); missing/stale telemetry still holds. Model budgets
such as Fable still gate their tasks. There is no weekly pace, overflow or automatic
pressure firesale: the real weekly cap is invisible and Anthropic may still stop runs.
Manual or announcement firesales can unlock the firesale tier but cannot spend the
session reserve. Use duration-based manual firesales; `--until-reset` requires a
reported weekly reset. Calibration never invents a weekly number for these seats.

## OpenAI API credits

Eligible ChatGPT Pro and Business subscriptions grant monthly
[API credits](https://developers.openai.com/api/docs/guides/chatgpt-api-credits) to one linked
OpenAI API organization. They expire at the end of each billing cycle and do not roll over.
An `openai-api-credits` account lets Redline show that balance and spend it on queued work
before it lapses.

- **What counts.** Only *promotional* grants (the subscription benefit) that are in effect and
  have money left. Purchased and auto-recharge credit is shown on every dashboard but is never
  scheduling capacity.
- **Floor.** No job starts while the promotional balance is at or below `min_promo_balance`
  (default $5). The floor is checked when a job starts, and a running job is not stopped, so
  each run already in flight reserves `run_cost_estimate` (default $1): another run starts only
  while the balance minus those reservations stays above the floor. Raise the estimate for
  expensive jobs (a run can still spend more than it reserves), or set it to 0 to turn the
  reservation off. For a hard cap, give Redline a key from a dedicated OpenAI project with an
  enforced [spend limit](https://developers.openai.com/api/docs/guides/spend-limits).
- **Pacing.** `firesale` (default) holds the balance until a single grant expiring within
  `firesale_window` (default 72h) still holds at least `min_promo_balance` (and at least $0.01),
  then runs queued work at the firesale tier. Smaller grants about to lapse do not trigger it on
  their own: with a $5 floor, $1 expiring tomorrow and $100 expiring next month, work holds.
  `even` runs work whenever a grant has more left than the share of its cycle still to go.
  A manual firesale overrides pacing, never the floor. Stale or unreadable balances are UNKNOWN
  and hold.
- **Which jobs.** Only `pi` and `command` profiles can use the account: each run is given the
  account's own API key as `OPENAI_API_KEY` (and its organization), after any inherited OpenAI
  key, organization, project or base URL is removed. OpenAI's terms exclude Codex usage
  through API keys, and Claude Code and Cursor bill elsewhere, so those profiles are refused.
  With no saved API key, work holds rather than inherit whatever key the service has.
  Such runs also get `REDLINE_OPENAI_API_CREDITS=1`, so tools inside them can tell they must
  bill the API key: for example, a pi extension that moves `openai/*` models to a ChatGPT
  subscription should leave them alone when it is set. Every run gets
  `REDLINE_PROVIDER_ACCOUNT_ID`. Neither variable, nor the session key, is ever passed through from the
  service to a run or a workspace prepare or finalize hook.

```yaml
providers:
  api-credits:
    provider: openai-api-credits
    label: Linked org
    window_weekly_cost: 0.05
    api_credits:
      organization_id: org-…        # empty reads the session's default organization; required on every credits account when there is more than one
      min_promo_balance: 5          # USD
      run_cost_estimate: 1          # USD each run in flight reserves against the floor
      pacing: firesale              # or even
      firesale_window: 72h
      # session_key_file: /abs/path # optional: read the session key from a private file
      # api_key_file: /abs/path     # optional: read the run API key from a private file
```

### Keys

OpenAI exposes the grant list only to the platform dashboard, so Redline needs the browser
**session key** (`sess-…`): on platform.openai.com › Settings › Billing › Credit grants, open
the browser's developer tools, select the `credit_grants` request and copy its `Authorization`
value. API keys and Codex sign-ins are rejected by that endpoint. The session key carries your
full dashboard access; Redline sends it only to the credit grants endpoint (no redirects), never
logs or displays it, and never sends it to phones or the relay. When it expires the account
shows "Session key needed"; paste a fresh one.

Keys are entered only on the machine running Redline, from standard input:

```bash
redline account add-openai-credits api-credits --org org-… --label "Linked org"
pbpaste | redline account set-session-key api-credits
pbpaste | redline account set-api-key api-credits      # an sk-… key from that organization
```

To remove both saved keys later (work on the account then holds until new ones are saved):

```bash
redline account clear-keys api-credits
```

They are stored in the login Keychain on macOS (a locked or unreachable Keychain is reported,
in the service log at start and on the account, rather than treated as "no key") and in
owner-only files beside the config elsewhere (`REDLINE_SECRETS_DIR` overrides the directory). A headless service can instead read
`session_key_file`/`api_key_file` (owner-only, not a symlink, re-read every poll, so a secret
manager can rotate them) or take `REDLINE_OPENAI_SESSION_KEY`, which the service removes from
its environment at start so no job inherits it. A configured key file takes precedence over a
saved key: `set-session-key` and `set-api-key` still save, but warn that the file stays in use.
On Windows, Redline does not verify a configured key file's ACL (it still refuses symlinks):
protect the file yourself so only your user can read it. Keys Redline saves itself get an
owner-only ACL.

When the session key is missing or OpenAI rejects it, work on the account holds and an
`account.session_key_needed` notification is sent once (again only after a successful read in
between; a service restart may repeat it once). Like `account.needs_login` it is opt-in: add it to
`notifications.events`. The alert names the fix and never contains key material.

## Claude (Anthropic) API credits

Claude subscriptions can grant API credit to a linked
[Claude Console](https://platform.claude.com) organization, as a promotional tranche that
expires. An `anthropic-api-credits` account is the same feature as
[OpenAI API credits](#openai-api-credits), with the same `api_credits:` settings, floor,
in-flight reservation, pacing, firesale window, notifications and dashboards (labelled
"Claude API credits"). The differences:

- **What counts.** Only `subscription_api_credit` tranches that have started, have not
  expired and have money left. Purchased tranches and other promotional programs are shown
  but never scheduling capacity. A response Redline does not fully understand (another
  currency, a tranche without a program, an unreadable date or amount) makes the balance
  UNKNOWN, and work holds.
- **Organization.** `organization_id` is the Console organization's UUID. Left empty,
  Redline uses the one organization with API access; when several have it, set it. With
  more than one Claude credits account, every one of them must set `organization_id`, even
  if each session sees only one organization. Two accounts cannot name the same
  organization.
- **Which jobs.** `claude-code`, `pi` and `command` profiles. Codex, Cursor Agent and Hermes
  profiles are refused. Each run first loses every inherited Anthropic credential, routing
  and cloud switch: every `ANTHROPIC_*` variable, every `CLAUDE_CODE_USE_*`,
  `CLAUDE_CODE_OAUTH_*`, `CLAUDE_CODE_API_*` and `CLAUDE_CODE_GATEWAY_*` variable, and
  `AWS_BEARER_TOKEN_BEDROCK` (case-insensitively on Windows). It is then given the account's
  API key as `ANTHROPIC_API_KEY` and `REDLINE_ANTHROPIC_API_CREDITS=1`. The key is never put
  in a command line, and a profile's `harness_args` may not add pi's `--api-key`,
  `--provider`, `-e`, `--extension`, `--model` or `--models`, or Claude Code's `--settings`
  or `--setting-sources` (on an OpenAI credits account, pi's `--api-key`, `--model` and
  `--models` are refused too).
  - **Claude Code can spend these credits**, unlike Codex on OpenAI. Each run starts with a
    fresh, empty, private `CLAUDE_CONFIG_DIR` that is removed afterwards, so a stored Claude
    subscription login can't take precedence over the key. A Claude credits account never
    has a Claude Code profile directory. Each run also passes `--setting-sources=`, so no
    user, project or local settings file is loaded: a repository's `.claude/settings.json`
    could otherwise set `ANTHROPIC_BASE_URL` and send the key to another host.
  - **pi** profiles need an `anthropic/…` model (not empty or `default`). pi prefers a
    stored Anthropic login over `ANTHROPIC_API_KEY`, so Redline loads a small extension of its
    own with `-e` (written to a private run directory, never containing a key, removed
    afterwards). It moves `anthropic` and `anthropic-cli` models onto a provider that reads
    `$ANTHROPIC_API_KEY`, and ends the run (exit status 78) rather than let it continue on
    any other model, or send a request on any other provider. A pi extension of your own that
    reroutes Anthropic models should leave them alone while `REDLINE_ANTHROPIC_API_CREDITS`
    is set.
  - **command** jobs get the environment and, like Claude Code runs, a fresh, empty, private
    `CLAUDE_CONFIG_DIR`, so a `claude` they start spends the key. Redline does not control
    that command line, so a command job that starts `claude` should pass
    `--setting-sources=` itself; without it, a `.claude/settings.json` in the workspace can
    set `ANTHROPIC_BASE_URL` and send the key to another host. They do not get the pi
    extension: a command job that starts pi must pass `-e` with an equivalent extension
    itself, or use a `pi` profile instead.
  - A run's private directories are removed when it ends; any a crashed service left behind
    are removed at the next start once they are a day old.
- **Hard cap.** The floor gates job starts; it does not stop a running job. For a hard
  limit, create a dedicated `redline` workspace in the Console organization, give it a spend
  limit, and give Redline an API key from that workspace.

```yaml
providers:
  claude-credits:
    provider: anthropic-api-credits
    label: Console org
    window_weekly_cost: 0.05
    api_credits:
      organization_id: 00000000-0000-0000-0000-000000000000   # the Console org UUID; empty uses the only org with API access
      min_promo_balance: 5
      run_cost_estimate: 1
      pacing: firesale
      firesale_window: 72h
```

### Claude keys

The Console's credit balance is only available to a signed-in browser, so Redline needs the
**session key**: the `sessionKey` cookie (`sk-ant-sid…`) for platform.claude.com, from the
browser's developer tools (Application or Storage › Cookies). It carries your full Console
access. Redline sends it only to the Console's organizations and prepaid credits endpoints on
platform.claude.com (no redirects), never logs or displays it, and never sends it to phones
or the relay. The run **API key** is an `sk-ant-api…` key from the same linked organization.
Each command checks the key's prefix, and an OpenAI key given to a Claude account (or the
reverse) is refused with a message saying so.

```bash
redline account add-claude-credits claude-credits --organization-id <uuid> --label "Console org"
pbpaste | redline account set-session-key claude-credits   # the sessionKey cookie
pbpaste | redline account set-api-key claude-credits       # sk-ant-api… from the redline workspace
redline account clear-keys claude-credits                  # later, to remove both
```

Storage (Keychain service `ai.redline.api-credits`, or owner-only files), the
`session_key_file`/`api_key_file` options and the `account.session_key_needed` notification
work as for OpenAI. The alert names platform.claude.com. `REDLINE_OPENAI_SESSION_KEY` never
serves a Claude account, and there is no Claude equivalent: use a saved key or
`session_key_file`.

## Manual firesales

Start an on-demand firesale for **any configured provider account**, not just Codex:

```bash
redline firesale start --provider claude-main --for 15m
redline firesale start --provider codex-main --until-reset
redline firesale status
redline firesale stop --provider claude-main
```

A duration must be 5 minutes to 7 days (Go duration syntax, e.g. `15m`, `2h`, `168h`).
`--until-reset` uses the latest cached weekly reset and refuses an unknown or past reset.
Starting again replaces the deadline; one manual sale can be active per account.
Manual sales are independent of public announcements and `reset_signals.firesale` mode.
They persist across restarts and end at the deadline, when stopped, or when the account's
weekly reset moves later by at least one hour. Remaining allowance increasing alone does not
end a manual sale; without usage at start, only stop or the deadline ends it.

Manual forcing overrides **pacing only** and unlocks the `firesale` tier. It does not change the
effective reset horizon or overflow math, create allowance, spend a banked reset, bypass the
five-hour reserve, stale/missing telemetry, provider pause, exhausted model budgets, concurrency,
or job eligibility gates. Missing weekly telemetry stays `UNKNOWN` unless the account explicitly
uses the session-only policy above; zero reported weekly allowance stays held.

A paused account may start a sale but cannot dispatch until resumed. The status API warns when
starting is pointless without refusing it; demo mode refuses starts.

The scheduler, usage monitor, startup reconciliation, and firesale API reconcile lifecycle changes.
Start/end notices use `firesale.started`/`firesale.ended` with `trigger: manual`, the account,
deadline, original actor, and end reason. Enable those events in `notifications.events` to receive
them. See the [API reference](api.md#manual-firesales-any-provider).

## Redeeming banked resets

Start with a dry run for each provider, then perform the first real redemption manually:

```bash
redline reset redeem --provider claude-main --dry-run
redline reset redeem --provider codex-main --dry-run
redline reset redeem --provider claude-main
```

Dry runs use nonexistent IDs, not a real reset: Claude expects `ineligible` / `unknown_grant`,
Codex expects `no_credit`, reported as `dry_run_ok`. Real redemption spends Claude's claimable
next grant or Codex's soonest-expiring itemized credit. A missing Codex detail list means to wait
for collection and try again shortly. Claude eligibility and cooldowns are respected. Multiple
configured accounts are handled by the login each one spends from:

- A Claude Code profile account (`claude_config_dir`) redeems from its own profile login, read
  without refreshing it. Each profile has its own rate-limit lockout.
- An account on the default login redeems only when it is the provider's only default-login
  account; with two, the login is ambiguous and redemption is refused.
- An account bound to a non-default `openusage_card` is refused: OpenUsage's card names no local
  login Redline can spend from. Bind it to its Claude Code profile instead, or use `/limit-reset`.

Refused accounts still get reminder notifications, and their automatic reset horizon is not
shortened.

### OpenUsage cards and run logins

Claude Code and Codex start as the default login on this Mac unless the account names a Claude
Code profile. An account bound to a non-default OpenUsage card therefore shows that card's usage
but never dispatches work: a run would spend the default login's allowance while Redline
scheduled against the card's numbers. Its decision reads `login_unknown`, the dashboard and
phone say "Dispatch held · login unknown", and the service logs a warning at start. For Claude
team seats, bind each person to their Claude Code profile with
`redline account add-profile <id> --dir ~/.claude-cc-<name>`; usage, runs, and reset
redemption then all use that profile's login.

### Keeping profile logins fresh

A Claude Code profile's access token lasts about 8 hours, and only Claude Code renews it, while
it runs with that profile. Without help, a seat nobody used for 8 hours would go out of date and
hold its work. So the usage monitor (`usage_monitor.enabled`) keeps each profile account's login
fresh. From 15 minutes before expiry it runs `CLAUDE_CONFIG_DIR=<dir> claude auth status`, which
spends no quota. If the expiry has not moved by the token's last 5 minutes, or the token has
already expired, it sends the smallest real request instead: one word from haiku, with no tools,
MCP servers, settings or saved session. That costs a few hundred tokens on that seat, and on an
idle seat it starts a 5-hour window. At most 6 such requests are sent per seat per day.

Redline still never writes a credential: it only reads the expiry and checks that it moved. The
renewal runs with a minimal environment (no API keys, base URLs, model overrides or other tools'
secrets) in a fresh private temporary directory, and its output is discarded. Renewals run one at a time,
skip an account while a Redline run is active on it (that run renews the login itself), and retry
after 5 minutes, then 30. The default login and Codex are never touched. `claude` must be on the
service's PATH; the Mac app adds `~/.local/bin`, Homebrew and `/usr/local/bin`. Not yet
available on Windows.

When a profile cannot be renewed (signed out, or three renewals in a row did not take, which
usually means its refresh token was revoked), an `account.needs_login` notification is sent once
per sign-out with the command to run (add the event to `notifications.events`; a service restart
repeats it once). A signed-out seat shows "Signed out"; a revoked one shows "Out of date" until
you sign in again.

Turn it off with `usage_monitor.keep_profile_logins_fresh: false`.

Redline reads credentials without refreshing or rewriting them. If Claude's login needs refreshing,
run `claude`, then retry or redeem in Claude Code with `/limit-reset`. Codex requires a ChatGPT
OAuth login, not an API key. Each operation persists an idempotency key before contacting the
provider, reuses pending keys on retry, allows only one redemption per provider, and shares usage
reads' rate-limit lockout. Inspect recent attempts through the API's `banked-resets/redemptions` route.

These are **undocumented provider endpoints** and may change. Codex has a known enforcement bug:
a redemption can be acknowledged while limits still enforce the old window, and single-window
accounts may fail. Redline refreshes real provider telemetry after success and reports `verified`
only when weekly remaining increases or the weekly reset moves later. `verified: false` includes a
warning; do not rely on new capacity until usage confirms it. Provider telemetry is still stored,
never replaced with an invented refill.

After dry runs and a manual first redemption, opt in per provider:

```yaml
banked_resets:
  on_expiry: redeem       # default: notify; off disables the expiry action
  redeem_lead: 1h
  remind_before: [24h]
```

The usage monitor attempts the next eligible reset no earlier than `expires_at - redeem_lead`,
even with the scheduler disabled. `on_expiry: redeem` requires `usage_monitor.enabled: true` and
native reset telemetry (not `usage_source: openusage`); other configurations fail to load.
Claude grants must be claimable, explicitly usable, out of cooldown, and not require hitting a
limit. Codex needs itemized credits with resets left. Demo mode and multiple accounts remain
blocked. **Provider pause is not respected for redemption**: pause controls job dispatch, not
whether an expiring reset is saved from being lost. Notifications may be disabled without
preventing redemption; reminders still require notifications.

In redeem mode, usable banked resets that clear the weekly window and will be applied before
the real weekly reset shorten the effective scheduling horizon to their redeem time. This can
trigger firesale earlier. `off` and `notify` never shorten it; model-specific pools keep their
own reset. A redeemed grant stays guarded until telemetry observed after the redemption shows a
lower, positive remaining count; then its next use can be selected. Codex credits are single-use.
A provider acknowledgement alone never invents capacity: only refreshed, verified usage demonstrates
a refill.

Automatic attempts are limited to one per provider per five minutes. Transient errors retry on
later successful monitor cycles before expiry, reusing pending request keys and respecting provider
cooldowns and 429 lockouts. `nothing_to_reset` waits at least 30 minutes before trying again;
`redeemed` and `gone` stop retries for that reset. Retry state survives daemon restarts. If a failed
reset expires unused, the next successful monitor cycle sends one `reset.redeem_failed` notice,
even if the credit has disappeared from telemetry. Redeem mode suppresses `redeem_now` only for
resets automatic redemption will attempt before they expire; those redemptions send their own
outcome events. Any other reset (demo mode, multiple accounts of the provider type, or a Claude
grant that is unclaimable, unusable, requires a limit, or stays in cooldown past expiry) still
gets `redeem_now`.

If a request's response is lost, automatic retries adopt that pending request and reuse its key
before considering another reset, even if it disappears from telemetry. At expiry an unresolved
request is reported as **outcome unknown**, not expired unused; check the provider. If no expiry can
be recovered, retries stop with that same unknown outcome 24 hours after the first attempt.

MCP deliberately has no redemption tool: spending resets from an agent is out of scope.

## Dispatch tiers and priority

Each task has a `dispatch_tier` that controls when it becomes eligible:

```yaml
dispatch_tier: behind # behind, well_behind, expiring, or firesale
priority: 60
```

The active provider policy first decides whether background work is safe. Redline then derives the
currently unlocked tier from the weekly pace gap, or from unavoidable throughput overflow when a
five-hour window exists. `priority` only orders tasks whose tiers are already unlocked. Recurrence
intervals, repository-change checks, and enable/disable state remain independent eligibility gates.
Existing databases migrate tasks to `behind`, preserving the prior default behavior.

Tiers unlock in order: `behind` (1), `well_behind` (2), `expiring` (3), `firesale` (4).
In the dashboard they appear as **Standard surplus**, **High surplus**, **Near expiry**, and
**Firesale (lots expiring)**.

## Policies

Policies may set `pace_gap_trigger` to admit work whenever the fraction of weekly allowance
remaining exceeds the fraction of time remaining by that amount. For example, `0.30` means Redline
can run when an account is at least 30 percentage points behind an even weekly burn pace. The
standard bundled policy uses `0.30`, early uses `0.15`, and late omits the setting so that it
continues to require a configured time threshold or unavoidable overflow.

### Accounts without a five-hour window

An account that reports no five-hour window (a verified Claude Ultra account, or Codex while its
short limit is absent) never produces slot overflow, so the weekly reset is its only clock. Policies
may add `weekly_only_pace_thresholds`, using the same `{time_remaining, min_weekly_remaining}` rows
as `pace_thresholds`. They are checked after the regular rules, and only when the latest usage
snapshot has no five-hour window. They never change a decision for an account that reports one.

The bundled `late` policy uses them so leftover weekly quota is filled before it expires. Each row
asks for about 1.4 times the share of the week still to go:

| Time to weekly reset | Weekly remaining needed |
| --- | --- |
| 72h | 60% |
| 48h | 40% |
| 24h | 20% |
| 6h | 5% |

`standard` and `early` already admit weekly-only accounts through `pace_gap_trigger` and their own
thresholds, so they do not set them. Configs written before this release do not pick the rows up
automatically. Copy the block from `config.example.yaml` into your `late` policy to use it.

A policy with none of `pace_gap_trigger`, `pace_thresholds` or `weekly_only_pace_thresholds` can
only admit work through five-hour slot overflow. Redline still loads it but logs a warning at
startup, because a weekly-only account's decision is `UNKNOWN` under it except during a
reset-announcement firesale.

Each provider's usage detail in the dashboard includes a dispatch-policy selector. Selecting a named
policy stores an override in SQLite; selecting **Default** returns to the provider-level YAML policy
when present, or to `active_policy` otherwise.

See `config.example.yaml` for the bundled `late`, `standard`, and `early` policy definitions.

## Effective horizon and firesale

Decisions expose `effective_reset_at` and `effective_reset_source`. The effective horizon is the
earliest future reset event that clears the weekly allowance, no later than its real reset.
The service always supplies the real `weekly` event, plus a `banked_reset` event for the next
auto-redeemable reset that clears the weekly allowance before that boundary. This requires
`banked_resets.on_expiry: redeem` and automatic redemption to be able to run: usage monitoring
enabled, a single account of that provider type, and not demo mode. The event is timed at the
earliest permitted attempt, respecting redeem lead, provider throttling, retry deadlines,
cooldowns, and provider lockouts. An active announcement firesale also supplies a
`predicted_global` event with confidence `announced` at the announced deadline (not a probability).
Slots, overflow, pace thresholds, and projections use this horizon; pace gap still measures time
remaining as a fraction of a seven-day week. Model-specific weekly pools retain their own resets.

A policy's optional `firesale_trigger` (0–1) upgrades an admitted decision to `firesale: true`
and unlocks the highest tier when `max(pace_gap, overflow) >= firesale_trigger`. Overflow is used
only when a five-hour window exists. The bundled early policy uses `0.35`, standard uses `0.45`,
and late omits it (disabled). This pressure-triggered firesale never changes WAIT or UNKNOWN into
RUN. No firesale bypasses the rolling reserve, pause, enable, repository-change, or exhausted-pool
gates.

```yaml
policies:
  standard:
    firesale_trigger: 0.45 # alongside the normal policy fields
providers:
  claude-main:
    firesale_max_concurrent_runs: 3
```

During a provider-level firesale, a task's optional `firesale_min_interval` replaces its normal
`min_interval`. It must be positive when set; it may be shorter or longer than the normal interval.
Unset (zero) uses the normal interval. The provider's optional `firesale_max_concurrent_runs`
raises its effective cap only if larger; allowance-pool, runtime-connection, and agent-context
limits still apply. Finished runs promptly wake the scheduler while the provider's latest recorded
automatic usage decision is firesale. A model pool can unlock the firesale tier independently,
but cannot activate these provider-level cooldown or concurrency overrides.

Automatic dispatch emits `firesale.started` and `firesale.ended` once per provider state transition
when notifications are enabled and the event is listed in `notifications.events` (neither is in
the default list, so existing hooks do not receive new event types after upgrading). Events
include string-valued `pace_gap`, `overflow`, `effective_reset_at`, and `unlocked_tier` data.
Manual evaluations do not emit pressure transitions; decisions without a provider usage evaluation
do not change the recorded pressure state.

### Announcement-triggered Codex firesales

`reset_signals.firesale: off|notify|auto` is independent of `firesale_trigger`. It defaults to
`notify` when reset monitoring is enabled, otherwise `off`; explicit `notify` or `auto` requires
`reset_signals.enabled: true`. Only viable source-backed promises/schedules with a future
`WindowEnd` qualify. Promises without a deadline, odds/predictions, and already-completed resets
never start one. Notify mode needs `redline reset firesale start` (or the bearer API opt-in);
auto starts without a separate opt-in or notification delivery.

While active, the provider's firesale/pacing gate is forced even with zero/negative pace gap or
no overflow, unlocking the firesale tier and its task intervals. The deadline shortens the weekly
horizon, never extends it. There is no additional cap on backlog burning. Provider pauses, auth
circuit breaker, five-hour window/reserve, all concurrency limits, model budgets, and normal job
eligibility rules remain in force. Unknown or stale usage is not treated as capacity.

The sale ends at its deadline, explicit retraction, completion, stop, or disable. Each provider's
latest usage at start is persisted as a baseline. Fresh telemetry observed after that baseline also
ends its sale when weekly remaining rises more than 0.02 (two percentage points) above its stored
low-water mark, or weekly `resets_at` moves forward (including the regular weekly rollover).
This reports `completed` with `completion_source: usage_refill` and returns to normal pacing.
Without a snapshot at start, that sale skips usage-based completion. Active sales
persist across restart and tracker outages. A same-key fresh deadline revision is stored/logged;
a different key waits rather than replacing an existing opt-in. Start/end notifications are
claimed independently of scheduler decision flips, so the scheduler cannot double-announce them.
See [reset-signal modes and lifecycle](reset-signal-preview.md#announcement-firesales) for details.

## Automatic dispatch

Automatic execution is disabled by default. Enable it in the service configuration only after
profiles and tasks have been reviewed:

```yaml
scheduler:
  enabled: true
  poll_interval: 5m
```

The service evaluates every configured provider once at startup and then at the configured
interval. Each cycle skips paused providers, then fills available capacity up to
`max_concurrent_runs` (default `1`). Optional `pool_concurrency` entries independently cap
overlap within allowance pools such as `model:fable:weekly`. Each candidate task's Git revision
is resolved independently, and automatic decisions are recorded with `"trigger":"automatic"`.
The dashboard can persist a provider-level override without rewriting the YAML configuration;
resetting the override restores the configured default. Remote Hermes jobs must also satisfy the
selected runtime connection and agent context limits, so the effective concurrency is the
strictest applicable provider, allowance-pool, connection, or context limit.

Inspect the live loop with:

```bash
redline scheduler status
```

### Manual evaluation and execution

Simulated evaluation records the decision and selected task but does not change task state.
Execution atomically claims the task and returns a preparing run while work continues in the
service:

```bash
redline scheduler evaluate --provider codex-main --revision "$(git rev-parse HEAD)"
redline scheduler execute --provider codex-main --revision "$(git rev-parse HEAD)"
redline run show <run-id>
redline scheduler history --provider codex-main
redline pause --provider codex-main
redline resume --provider codex-main
```

## Window-cost calibration

`window_weekly_cost` is the bootstrap estimate of how much weekly allowance one completely
consumed five-hour window represents. Provider usage feeds report the two percentages but do not
report this conversion directly. Redline learns it by grouping snapshots that share the same
five-hour and weekly reset boundaries and aggregating:

```text
weekly usage increase / five-hour usage increase
```

The configured value remains authoritative while evidence is insufficient or low-confidence.
An observed value becomes effective after at least two informative five-hour windows totaling at
least one full window of consumption; four windows and two full windows of consumption are marked
high-confidence. Sub-second reset timestamp jitter is normalized during grouping. Inspect both the
evidence and the value currently used by the scheduler with:

```bash
redline calibration --provider claude-main
redline decision --provider claude-main --json
```

Decisions expose `window_weekly_cost`, `window_weekly_cost_source`, and
`calibration_confidence`. Providers without a five-hour window, such as Codex while that limit is
temporarily absent or a verified Claude Ultra account (no five-hour limit since 2026-10-30), cannot
produce paired calibration evidence and continue using pace rules on the weekly window alone.
Redline decides this from the windows the provider reports, not the plan name: an unverified Ultra
account that still reports a five-hour window is scheduled with slots like Max and Pro. See
[Accounts without a five-hour window](#accounts-without-a-five-hour-window) for the pace rules
that apply.

## Empirical token capacity

The read-only usage monitor refreshes usage snapshots independently from automatic scheduling and
correlates them with locally observed processed-token counts to estimate how large each allowance
window actually is.

Token observations come from two sources:

- **Redline's own runs.** Every completed run's token usage is attributed to the provider and model
  allowance selected by its execution profile.
- **An optional local token log.** When `usage_monitor.gatepost_database` points at a Gatepost
  index, Redline also imports Codex and Claude Code assistant-call records and Pi session files.
  Only explicit subscription transports are included:

  ```text
  Pi anthropic-cli  -> Claude subscription allowance
  Pi openai-codex   -> Codex subscription allowance
  ```

  Other Pi providers are excluded rather than inferred from model names. Gatepost is not yet
  publicly released; the key is not present in `config.example.yaml`, and there is nothing to
  configure unless you have Gatepost installed.

```yaml
usage_monitor:
  enabled: true
  poll_interval: 5m
  # Optional, advanced: only if Gatepost is installed.
  # gatepost_database: ~/.gatepost/viewer.db
```

Redline accumulates local processed tokens until the provider's quantized percentage moves, then
closes a correlation span without crossing a 5-hour or weekly reset. It reports estimated input,
output, cache-read, cache-creation, and total capacity where the source preserves those classes.

```bash
redline token sync --provider claude-main
redline capacity --provider claude-main
```

The capacity report includes direct 5-hour and weekly estimates and a second weekly estimate derived
from `estimated_5h_tokens / window_weekly_cost`. These are explicitly empirical processed-token
equivalents, not provider billing ledgers or guaranteed fixed caps. Model choice, cache accounting,
long-context multipliers, service-side policy, partial local-log coverage, and percentage rounding
can all change the observed relationship. Redline therefore exposes evidence counts, observed
percentage movement, token classes, source, and confidence rather than presenting a precise quota.

Each window also reports attribution coverage: total provider-reported drain, the fraction with
matching local token observations, unattributed spans, and evidence composition by harness source
and model. Incomplete attribution caps confidence even when many spans exist. Overall report
confidence is the weaker available window, and `ratio_derived_difference` quantifies disagreement
between the direct weekly estimate and the 5-hour-derived cross-check. The dashboard loads this
evidence on demand from a provider's expanded usage card so routine live updates remain inexpensive.

Weighted accounting is reported alongside raw processed tokens. Codex uses OpenAI's token-based
subscription credit card; Claude uses current API pricing as an explicit proxy because Anthropic
does not publish an equivalent subscription rate card. Unknown models remain unpriced and reduce
`pricing_coverage`. Exact Pi token classes produce a narrow quote; collapsed direct-session context
and unknown Claude cache-write duration produce low/high bounds. Raw observations are never mutated
when a rate card changes.

## Operational history

Scheduler decisions answer "what did the budget model conclude?" Dispatch attempts answer "what
happened operationally when Redline tried to release work?" Attempts persist `admitted`, `wait`,
`no_task`, and `error` outcomes for both manual and automatic execution:

```bash
redline scheduler attempts --provider codex-main
```

When budget permits work but no job can be admitted, `task_selection_reason` and bounded
`candidate_rejections` explain cooldown deadlines, unchanged or unreadable repositories,
model-pool exhaustion, saturated concurrency pools, and locked dispatch tiers. Concurrent
scheduler requests are serialized at admission; requests exceeding a configured provider or pool
limit record an `active_run` WAIT rather than degrading operational health. Different providers
always have independent capacity.

## Outcome metrics

Redline can report exact automatic RUN/WAIT/UNKNOWN decisions and job outcomes, alongside
coverage- and confidence-labeled estimates of allowance converted to completed work:

```bash
redline metrics launch --days 21
```

See [the metrics methodology](launch-metrics.md) for definitions and caveats.

## Related

- [Profiles and tasks](profiles-and-tasks.md)
- [Runs, logs, and notifications](runs-and-notifications.md)
- [Architecture](architecture.md)
- [Firesale and banked-reset design](design/firesale-and-banked-resets.md)
