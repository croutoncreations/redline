# Multi-account usage: Android design proposal (Claude first)

**Status:** design, mockups and plan only. Awaiting the owner’s review. No production code is changed.
**Session:** worktree `redline-multi-account-opus55-oct10`, branched from `main` @ `4174078`. Model: `anthropic-cli/claude-opus-5-5`.
**Mockups:** `prototype.html` (static HTML at Pixel 9 412×923 dp, rendered at 2.625×) and `render.cjs` (renders the PNGs). All data is synthetic.

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

## 2. Variants

Compare all three side by side in `ABC-comparison.png`: overview on top, switching to account 2 of 3 below. Every screenshot is stamped `OPTION X · MOCKUP · SYNTHETIC DATA`.

| | Layout | Strengths | Weaknesses |
|---|---|---|---|
| **A (recommended)** · `A1-overview`, `A2-switch-account` | The **summary tile** replaces the Claude card: one row per account showing alias, plan chip, 5-hour and weekly as *separate* mini-meters with "left" and the reset, and a routing line. A **detail carousel** sits below with a peeking next card, a `‹ n / N ›` button pager, dots and a TalkBack sentence. | Overview and detail on one screen, matching the owner’s concept. The buttons and row taps give full non-swipe access. | Tall. Once scrolled, the tile collapses to a one-line strip. |
| **B** · `B1-overview`, `B2-switch-account` | One Claude card with **account chips** (two mini bars plus "5h n% left / wk n% left"). The body shows the selected account. | Most compact, and closest to today's single card. | Weakest overview. Long aliases make chips tall, and it gets crowded past about 4 accounts. |
| **C** · `C1-overview`, `C2-switch-account` | The Usage tab shows **only the summary tile**. Tapping a row opens a full-screen pager. | Scales best and keeps the Usage tab short. | One extra navigation level, and the gauges are no longer visible at a glance. |

Recommendation: use **A** for 2–5 accounts. Above 5, switch the dots to `3 of 7 ▾` and add a chooser sheet (frames 07 and 08), which effectively borrows C's scaling behaviour.

### Rules applied in every variant
- **Never sum or average** across accounts. The tile header counts *accounts dispatching* ("1 of 3 dispatching"), not capacity. The explanatory sentence says so.
- **The 5-hour and weekly windows** each get their own meter, their own reset, and the label "5-hour" or "Weekly". Model pools such as Fable are labelled "· model weekly".
- **Used and remaining are both explicit.** The value is "64% left" (matching the existing app), followed by a muted "36% used". The summary rows always include the word "left". An exhausted window reads "0% left · Available again in 1h 12m".
- **The plan chip shows what the provider reports** (Max 20x, Team, Pro), or "Plan unknown". We never present Team and Max as equivalent capacity, and we never infer Team-seat limits.
- **Identity follows a privacy rule.** Show the user-chosen alias plus `Claude · ID …a91f`, a short suffix of Redline's own account id hash. **No email, org name or token** reaches the phone.
- **States** (frame 05) are each named in words, so colour is never the only signal:
  - *Stale*: amber, grey bars, "last known" and the age.
  - *Needs sign-in on the Mac*: no numbers, and the fix is on the Mac only.
  - *Usage unavailable / not metered*: dispatch may still be on.
  - *Paused*.
  - *Monitor only*: no profile routes to it.
- **Routing and metering are separate lines.** The routing line ("Dispatch on / held / paused / Monitor only") is a different line from the meters. The chooser sheet says "changes which account you are viewing, not where tasks run".
- **Exhausted while the others continue** (A2/B2/C2 and frame 09): "Its tasks wait for this account; none move to another."
- **One account** (frame 06): today's card, plus an optional plan chip and alias. No pager.
- **Many accounts** (frames 07 and 08): rows needing attention sort first, the list caps at 3 rows with "Show all 7", and there is a counter plus chooser.
- **Accessibility:** ‹ › targets are 44×40 dp, rows can be tapped, the pager has custom TalkBack actions (Previous, Next, Choose account), text has no fixed widths, and long names wrap (the automated clip check found 0 clipped elements).

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

### Phase 4: Android UI (Variant A)
- `UsageScreen`: `ProviderList` renders `AccountGroupCard` when a group has more than one account and the existing `ProviderCard` otherwise.
- Add `HorizontalPager` with the button pager, `semantics { customActions }` and a `ModalBottomSheet` chooser. The selected page persists through `rememberSaveable` keyed by account id.
- Queue: picker chips show the alias, and monitor-only accounts are marked "not routed".

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

## 4. Questions for the owner
1. **Variant:** A, B or C? Or A with C's full-screen behaviour beyond N accounts, and if so what should N be (proposed: 5)?
2. **Summary rows:** keep both 5-hour and weekly per row, or weekly only with 5-hour shown when it is ≤35% or exhausted?
3. **Value wording:** keep "64% left · 36% used" in detail, or show only "left" (today's style)?
4. **Identity:** is alias plus `ID …a91f` enough, or do you also want a masked email on the Mac only?
5. **Monitor-only accounts:** show them in the tile (proposed) or hide them behind "Show all"?
6. **Metering for a second Claude account:** link Redline accounts to upstream OpenUsage card ids (recommended; this needs the Redline adapter change and verification of the §1a unknowns, but no extra OpenUsage instances), or a native-profile adapter, or both?
7. **Branch:** land `cursor-grok-surfaces` on main before this work?
