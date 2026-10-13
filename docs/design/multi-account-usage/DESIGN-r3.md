# Multi-account usage: selected design (r3) and implementation plan

**Status:** DESIGN SELECTED (Option A, as modified by the owner), awaiting approval before any production code.
**Decision source:** the owner's message: *"have all the detail cards swipable from the
summary card instead of below it … show the summary card on open, then swipe it to scroll through the detail cards of the accounts."*
**Supersedes:** `DESIGN.md` (r1/r2), which stays published unchanged. This file is the current plan.
**Session:** worktree `redline-multi-account-opus55-oct10`, base `main` @ `4174078`, model `anthropic-cli/claude-opus-5-5`.
**Mockups:** `selected-a-r3.html` (built by `build-r3.cjs` from shared components) and the r3 artifacts listed in `DELIVERY-r3.md`.

## 0. Selected design: one carousel in the Claude tile

The Claude tile becomes a single horizontal pager. **There is no separate detail stack below it.**

| Page | Content |
|---|---|
| **0 · Overview** (default) | One row per account: alias (wraps in full), reported plan chip, 5-hour and weekly as separate mini meters with "% left" and reset, a routing line, and **"Details ›"**, which jumps to that account's page. |
| **1…N · Account** | One account's full detail in the same tile area: alias heading, plan, `ID …xxxx` hint, dispatch badge, source and freshness, 5-hour, weekly and model-weekly meters (with "% left" and "% used", reset countdown and time), the scheduler status line, banked resets, and View queue / Pause / Refresh. |

**Fixed on every page:** the Claude header (`Claude · N accounts · k of N dispatching`) and a pager strip:
- the Overview control: an "Overview" pill on page 0, a tappable "‹ Overview" on account pages
- segment dots, with a wider first segment for Overview, the current page in accent red, and used-up accounts in dark red
- ‹ › buttons, each 40×40 dp or larger, disabled at the ends
- a text position label ("Overview · swipe left for each account" / "Account 2 of 3")

**Navigation:**
- swipe left or right
- ‹ ›
- "Details ›" on an Overview row
- "‹ Overview" or the Overview segment
- system Back on an account page returns to Overview first

**State:** the tile opens on Overview on every cold start. The page is kept across tab switches during a session (`rememberSaveable` keyed by the selected account id, so a reordered list doesn't land on the wrong account). The tile's height animates to the current page's height.

**Scroll and gesture safety:** a Compose `HorizontalPager` consumes only horizontal drags. Vertical scrolling of the screen and pull-to-refresh pass through, and buttons and meters don't start page changes. No auto-advance.

**Accessibility:**
- The tile exposes custom actions: Overview, Previous account, Next account.
- A page change announces, for example, *"Claude account 2 of 3, Client — Northwind long-running engagement, Team plan, dispatch held"*.
- Overview rows are buttons ("Open details for Personal").
- Text is never cut off by a fixed width.
- Position is never shown by colour alone.

**Edge cases:**
- **One account:** no pager; today's card plus an optional plan chip (unchanged from r1 frame 06).
- **More than 5 accounts:** the dots become "Account 3 of 7 ▾", which opens the account chooser sheet (r1 frame 08). The Overview sorts attention-first and caps at 3 rows plus "Show all".
- **States** (stale, needs sign-in, not metered, paused, monitor only) render on the Overview rows as in r1 frame 05, and on the account page as the badge plus status line.

**Copy correction (from r2 review):** the scheduler line must agree with the floors drawn on the bar. Under the standard policy the 50% floor arms 72h before the weekly reset and the 20% floor at 24h.
- Personal (41% left, reset in 76h) now reads *"Dispatching · no weekly floor armed yet · 50% floor arms in 4h"*, and both floor ticks are drawn faint (unarmed).
- An implementation rule follows: build this line from the same `Scheduling` object as the bar zones (the Android `schedulingLine()` already does this). Never write it as free text.

**Unchanged rules:**
- Never sum or average across accounts.
- The 5-hour and weekly windows stay separate, each with its own reset.
- The plan is shown as reported; never imply that Team and Max have equivalent capacity.
- No emails; show the alias plus Redline's own account-id hint.
- Dispatch state is a separate line from usage.
- No automatic account rotation.

## 1. What exists today (from code)

| Surface | Where | Notes |
|---|---|---|
| **Native Android app** (Jetpack Compose) | `mobile/android/` on branches `jf-mobile-app` → `cursor-grok-surfaces`, **not on `main`** | Dark-only (`Theme.kt`, `themes.xml`), so the mockups are dark only. Tabs: Usage / Queue / Runs. Each `ProviderUsage` gets one `ProviderCard`, and `id` already holds the account id (`claude-main`). |
| Go mobile core | `mobile/core/usage.go` (same branches) | Interprets the desktop `/dashboard` payload into `UsageView` JSON for the Android and iOS UIs. |
| Mobile web PWA | `internal/api/dashboard/mobile.*` at `/m`, on `main` | A separate implementation that shares the palette. It is **not** the target here. |
| Desktop dashboard and macOS tray | `internal/api/dashboard/{index,dashboard.js}`, `macos/Sources/RedlineMenuBar/StatusPopoverView.swift` | Both iterate `providers`. To be aligned later. |

The mockups copy the Android Compose card and meter exactly: `Panel #15191D`, 12 dp radius, a 6 dp bar showing **remaining** with `toneFor` thresholds (<15 danger, <35 warn), a white pace mark, red reserve/floor lines, "Resets in …" plus the absolute time, and the tab bar.

### Backend facts that constrain the design

- **Config already supports several accounts per provider.** `providers:` is a map keyed by account id (`claude-main`), and `provider: claude` is the type. Validation does not forbid two `claude` entries.
- **Dispatch is already per account.** `execution_profiles.provider_account_id`, `runs.provider_account_id`, `provider_controls` (pause), `scheduler_decisions` and the queue endpoint are all keyed by account id. A task is routed through its execution profile to exactly one account. **Nothing rotates.**
- **Metering is per provider type, and this breaks with two accounts.**
  - `usage_snapshots` has only a `provider` column. `LatestSnapshot(ctx, configured.Provider)` in `api/dashboard.go:277`, `server.go:664` and `server.go:1423` returns *the latest Claude snapshot of any account*, so two Claude accounts would overwrite each other's numbers.
  - **Redline's adapter is single-account by construction** (`internal/openusage/client.go`). `Fetch` calls `GET /v1/usage/claude`, accepts an object or an array, and keeps only the entry whose `providerId` case-insensitively equals `"claude"`. If upstream returns additional per-account cards (`claude@<hash8>`, see §1a), Redline **silently ignores them**. `Source.Fetch` passes `provider.Provider` (the type), never the Redline account id. This limitation is **Redline's**, not upstream OpenUsage's.
  - `nativeusage` reads the **single** local Claude Code login (macOS Keychain service `Claude Code-credentials` for `$USER`, otherwise `~/.claude/.credentials.json`). Banked resets are explicitly cached "per provider credential rather than per account" (`usage/manager.go`).
  - Per-account items today: source-health state (`Manager.states[accountID]`) and the native lockout map.
- **The plan type is partly available.** The native credential record has `subscriptionType`/`rateLimitTier` fields. They are not surfaced, and this design work did not read them.
- **CLIProxyAPI** appears only as a Hermes `billing_provider` string (`custom:cliproxyapi-plus`) in tests. Redline has no code that reads its account store, and we should not assume OpenUsage can see it.

### 1a. Upstream OpenUsage capability (public docs and code only)

The evidence below comes from public sources only: the GitHub docs and source at `main` and the `v0.7.12` tag. The only local check was the installed app's bundle version (`/Applications/OpenUsage.app` reports 0.7.14, the latest release). I called no OpenUsage endpoint and read no account, credential or log data.

| Fact | Evidence |
|---|---|
| One Claude card per account and organization; Claude Code and Desktop logins for the same pair merge into one card | `docs/providers/claude.md` ("Each account and organization gets its own Claude card") |
| Claude Swap saved accounts are discovered from `~/.claude-swap-backup/sequence.json` at launch; a restart is needed after adding or removing one | `claude.md` § Claude Swap accounts; v0.7.12 release notes, "Discover Claude Swap accounts and isolate their usage (#1226)" |
| **Card ids are stable.** The first account seen at the default home keeps the bare id `claude`; every later account gets `claude@<hash8>`, the first 8 hex characters of SHA-256 over the lowercased identity key. `family(of:)` strips the `@…` suffix. | `Sources/OpenUsage/Stores/ProviderAccountsStore.swift` (`ProviderAccountID`); present at both the `v0.7.12` tag and `main` |
| `GET /v1/usage/:id` and `GET /v1/limits/:id` match an **exact card id or a family id**. `claude` returns every Claude card. Both routes **always return an array** (`/v1/limits` is an envelope keyed by card id). | `docs/local-http-api.md`; `Services/LocalUsageAPI.swift` `matchingCardIDs` |
| `/v1/limits` (schema `openusage.limits.v1`) is the recommended route. It has stable resource keys (`session`, `weekly`, `fable`, `rateLimitResets`, …), plus `used`/`limit`/`remaining`/`resetsAt`/`windowSeconds`, `plan`, `fetchedAt`, `stale` and `errors[]`. `/v1/usage` is legacy UI-shaped but still supported. | `docs/local-http-api.md` |
| The plan badge comes from the live `oauth/profile` `rate_limit_tier` | `claude.md` § Under the hood (v0.7.12, #1262) |
| **Card display names contain the email.** Swap cards read `Claude: <org> (<email>)`, and assembled labels read `<email> (<org>)`. | `ClaudeSwapAccount.displayName`, `ProviderAccountAssembly.swift` |

**What this means for Redline:**
- One upstream OpenUsage instance can very likely serve every Claude account through `/v1/limits/claude`. Each Redline account would map to one OpenUsage **card id**.
- **Multiple OpenUsage instances are not needed** for that, and the earlier suggestion is withdrawn. I haven't verified running more than one instance at all: the API binds a fixed port 6736.
- **Privacy:** Redline must **not** forward `displayName` to the phone, because it contains the email. Use the Redline alias and an account hint derived from Redline's own id.

**Still unknown, to verify against a local OpenUsage with synthetic or consented data before building:**
1. Whether the bare `claude` card can be **re-assigned**. When a different account later occupies the default home, does the `claude` id stay with the original record (the code comment says records "never move or vanish")? This is what Redline's mapping depends on.
2. Whether `/v1/limits` resource keys for Claude are identical on `claude@…` cards. The table lists them per family, which suggests yes.
3. How Desktop-only organization cards appear when the default login has no organization id. The docs describe a separate "default card"; its id format needs confirming.
4. Whether `stale` and `errors[]` are reported per card for Swap vault fallbacks.
5. Behaviour on releases older than v0.7.12, where `/v1/usage/:id` returned a single object or 204. Redline already accepts both shapes.

**Conclusion:** dispatch routing is already per account. Metering supports one account per provider type, because of **Redline's** adapter (it selects `providerId == "claude"`) and **Redline's** storage (`usage_snapshots.provider`), not because of an upstream OpenUsage limit. The UI has to keep those two capabilities visibly separate.

## 3. Implementation plan (after the design is approved)

### Phase 0: prerequisite decision
The Android app is unmerged (`cursor-grok-surfaces`). Decide whether to land it on `main` first or build this feature on that branch.

### Phase 1: data model (Go, provider-agnostic)
1. **Config** (`config.Provider`), all optional so existing configs are unchanged:
   - `display_name` (the alias)
   - `plan_label` (manual override)
   - `metering:` block: `source` (`openusage` | `native` | `none`), `openusage_url`, `openusage_card` (an OpenUsage card id such as `claude` or `claude@ab12cd34`; an id, not a secret), `native_profile` (a *path or Keychain account name only*; Redline never copies secrets into its config). Omitting `openusage_card` means today's behaviour: the bare `claude` card.
   - Optional discovery helper (later): list `/v1/limits/claude` card ids and their plans so the Mac UI can offer "link this card to account X". This is a manual link chosen by the user; Redline never auto-matches by email.
   - `monitor_only: true` to show an account that no execution profile may target.
   - Validation must reject two accounts that resolve to the **same metering identity**. Today that means two `claude` accounts both on default OpenUsage/native. It must be an error, not silently shared numbers.
2. **Migration `usage_snapshots` → add `provider_account_id TEXT`** plus an index on `(provider_account_id, observed_at)`.
   - Backfill: if exactly one configured account has that provider type, assign it. Otherwise leave it NULL, and treat NULL as legacy and unreadable once several accounts share the type.
   - Change `LatestSnapshot*` and `SaveSnapshot` to key on the account id. `usage_allowance_windows` and `token_observations` follow the same approach.
3. **Snapshot metadata:** `plan_label`, `freshness` (`live` | `stale` | `unavailable` | `needs_sign_in`) and `source_identity_hint` (a hash). Never store an email.
4. **Manager:** key banked resets per *metering identity* rather than provider type. Lockouts stay per endpoint and credential.

### Phase 2: metering adapters (what works today and what needs work)
| Source | Today | Needs |
|---|---|---|
| OpenUsage, one Claude account | ✅ works today | Nothing. This is the backward-compatible path (bare `claude` card). |
| OpenUsage, N accounts | ⚠️ **Upstream supports it; Redline drops it.** Upstream returns per-account cards (`claude`, `claude@<hash8>`) in one array; Redline keeps only `providerId == "claude"`. | A Redline adapter change: select by configured `openusage_card` (exact id), preferably via `/v1/limits/<card>` with `/v1/usage/<card>` as fallback; parse `plan`, `stale` and `errors[]`; never read `displayName` into stored or mobile data. A single OpenUsage instance suffices; **multiple instances are not required**. Verify the unknowns in §1a first. |
| Native, one login | ✅ works | Surface `subscriptionType` as `plan_label` via a non-secret field accessor (a code change, reviewed). |
| Native, N logins | ❌ single Keychain item/file | A `native_profile` pointing at an alternate Claude config dir/Keychain account. Redline reads only the token it needs, as it does now. Requires a security review. |
| CLIProxyAPI accounts | ❌ no integration | A separate adapter that reads usage only if CLIProxyAPI exposes a usage/quota endpoint. Until then those accounts show "Usage unavailable · Tasks can still run on it". |

### Phase 3: API and mobile core
- `/dashboard` `providers[]`: add `provider_type`, `display_name`, `plan_label`, `freshness`, `account_hint`, `routable`, plus a top-level `groups[]` ({provider_type, account_ids, dispatching_count}). Old clients ignore the new fields (`ignoreUnknownKeys`).
- `mobile/core/usage.go`: group by `provider_type` and produce `AccountGroup` with the summary rows already worded. Per the core's rule, wording lives in the core.

### Phase 4: Android UI (selected A, r3 carousel)
- `UsageScreen.ProviderList`: render `AccountGroupCard` for a provider group with more than one account. Keep the existing `ProviderCard` for a single account, so behaviour is unchanged.
- `AccountGroupCard` = `Card { GroupHeader; PagerStrip; HorizontalPager(pageCount = N + 1) }`, where page 0 is `OverviewPage` and page k is `AccountPage(account[k-1])`. Reuse the existing `Meter`, `UnknownMeter`, `schedulingLine` and `CardAction` composables unchanged.
- State: `rememberPagerState(initialPage = 0)` plus `rememberSaveable` for the selected account id, and a `BackHandler(enabled = page > 0) { animateScrollToPage(0) }`.
- Height: wrap the pager in `animateContentSize()`; pages measure their own height.
- Semantics: `customActions` on the card; `liveRegion = Polite` on the position label; Overview rows are `Role.Button`.
- More than 5 accounts: the chooser `ModalBottomSheet` from the position label.
- Queue tab: picker chips show the alias; monitor-only accounts are marked "not routed" (r1 frame 09).
- **Compose UI tests:**
  - opens on Overview
  - "Details ›" jumps to the right page
  - "‹ Overview" and Back return to Overview
  - ‹ › are disabled at the ends
  - TalkBack custom actions work
  - vertical scroll is not captured by the pager
  - one account shows no pager
  - the label for 7 accounts and the chooser
  - large font scale wraps long aliases
  - the scheduler line and floor ticks agree, for the armed and unarmed cases

### Phase 5: later consistency
- The desktop dashboard groups provider cards the same way.
- The macOS tray `providerGrid` shows one gauge per group (worst account) with per-account rows in the popover.
- The web `/m` PWA follows the same pattern.

### Routing vs metering, kept separate
Task routing is **already per account today**: execution profile → `provider_account_id` → run, pause, queue and scheduler decision. Nothing in this plan changes routing semantics. In particular:
- Linking an OpenUsage card changes **which numbers an account shows**, never which account a task runs on.
- An account with no metering link stays routable and shows "Usage unavailable".
- No automatic rotation. A held account's tasks wait for that account.

Two Redline-side limitations must both be fixed before a second metered Claude account is safe:
- **Fetch identity.** Both accounts would call `/v1/usage/claude` and get the **same bare `claude` card**. The native collector likewise reads the one local login. As a result, the scheduler's per-cycle evaluation (`evaluateProvider` → `fetchAndStore`, fresh per account) would admit or hold account B using account A's numbers.
- **Storage.** `usage_snapshots` is keyed by provider type. The stored-read paths (dashboard `api/dashboard.go:277`, `/usage` `server.go:664`, queue candidates `server.go:1423`) return whichever Claude snapshot was saved last.

### Tests
- OpenUsage adapter: an array with `claude` and `claude@ab12cd34` selects only the configured card; a missing card gives an explicit "card not found" error, not a fallback to `claude`; `displayName` and email never persist; legacy object and 204 shapes still parse.
- Config: duplicate metering identity is rejected; a single-account config needs no changes.
- Migration: a single-account backfill; a multi-account case leaves rows NULL; running it twice is safe.
- Store: two Claude accounts' snapshots never cross over (regression for the bug above).
- Manager: per-identity banked resets; a lockout on one account does not block another identity.
- Core: grouping, sorting attention first, wording for each state, never summing.
- Android:
  - Unit tests on the models.
  - Compose UI tests: pager buttons, TalkBack custom actions, chooser, one-account path unchanged.
  - Screenshot tests at a large font scale for wrapping.
- Scheduler: exhausting account A never dispatches A's tasks to account B.

### Explicitly out of scope
- Automatic account rotation or failover to get around limits.
- Pooled or "total Claude capacity" numbers.
- Inferring Team-seat capacity.
- Connecting or signing in accounts from the phone.
- Reading CLIProxyAPI's credential store.

## 4. Open questions (non-blocking; defaults proposed)
1. **Opening page:** always Overview on a cold start (proposed), or remember the last account page across launches?
2. **Overview rows:** keep both 5-hour and weekly (proposed, matching the mockup), or weekly only?
3. **Detail value wording:** keep "64% left · 36% used" (proposed), or "left" only as today?
4. **Account order:** configured order (proposed), or attention-first on the Overview as well as in the chooser?
5. **Metering link:** link each Redline account to an upstream OpenUsage card id (proposed; see §1a and Phase 2), a native profile, or both?
6. **Branch:** land the Android app (`cursor-grok-surfaces`) on `main` before this work starts?

**Stopping point:** no production code until the owner approves this r3 design and plan.
