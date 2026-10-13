# Multi-account Claude usage: implementation plan (approved scope)

Base: worktree `redline-multi-account-opus55-oct10`. Merged `cursor-grok-surfaces` (`8b70599`, Android app + mobile core),
merge commit `7d79097`. Model: `anthropic-cli/claude-opus-5-5` (runtime metadata).

## Key decisions (from code reading)

1. **Account identity = the existing `providers:` map key** (`claude-main`, …). Execution profiles, runs, pause controls,
   scheduler decisions and the queue already key on it (`provider_account_id`). Nothing about task routing changes: a task runs
   on the account its execution profile names. There is no rotation and no fallback to another account.
2. **Isolate usage per account in storage.** `usage_snapshots` and `token_observations` are keyed by provider *type* today.
   - Migration v30 adds `provider_account_id TEXT NOT NULL DEFAULT ''` to `usage_snapshots` and widens the identity index to
     `(provider_account_id, provider, observed_at, source)`.
   - Backfill: for each provider type with **exactly one** configured account, old rows (account `''`) are adopted by that
     account at startup (`AdoptLegacySnapshots`), which makes the single-account upgrade lossless. With several accounts,
     legacy rows stay unowned and are never shown on any account.
   - All snapshot reads take the account id (`LatestSnapshotFromSource`, `ListSnapshots`, queue candidates, capacity).
3. **OpenUsage multi-account (upstream-verified, public docs/code).** `GET /v1/limits/claude` returns every Claude card,
   keyed by card id (`claude`, `claude@<hash8>`).
   - New optional per-account setting `openusage_card`.
   - When it is set, the account reads `/v1/limits/<card>` (exact match) and converts resources `session`, `weekly`, `fable`
     and `rateLimitResets` (available count plus the `expiresAt[]` list).
   - `stale` / `errors[]` are honoured, and a missing card is an explicit error. `displayName` (which contains an email) is
     never read or stored.
   - When it is not set, the legacy path is unchanged (`/v1/usage/claude`, bare card).
4. **Native collector** reads the one local Claude Code login. It is allowed for at most one Claude account
   (validation error otherwise); additional accounts must use an `openusage_card`. The banked-reset supplement continues to run
   only when unambiguous.
5. **Reset credits** stay separate from window resets: `banked_resets` (count, nil = unknown) plus a new per-credit
   expiry list (`banked_resets_expiries`, from OpenUsage `expiresAt[]`, when provided) alongside the existing soonest expiry.
   Stale/unknown states are passed through and never invented.
6. **Account configuration in Redline.** Accounts live in the YAML `providers:` map, which Redline has never written.
   - Add a managed-accounts layer: `accounts.yaml` beside the config (`accounts_file:` override). It holds only non-secret
     fields: id, provider, label, usage_source, openusage_url, openusage_card, enabled, window_weekly_cost, policy.
   - It merges over `providers:` at load time: YAML-defined accounts are read-only "config file" accounts, and managed accounts
     are editable.
   - API: `GET/POST/PATCH/DELETE /v1/accounts`, plus `GET /v1/accounts/discover` (OpenUsage card ids + plan + freshness
     only). Changes are written atomically (temp file + rename, 0600) and applied by restart-required status (the service
     reports `restart_required`). Hot-reloading the scheduler loops is out of scope for the first cut.
   - Safety:
     - Removing an account that execution profiles reference is refused (409) unless it is disabled instead.
     - Duplicate ids, duplicate `openusage_card`, and two native-sourced Claude accounts are rejected (identity conflict).
     - No credential fields exist in the schema.
   - CLI: `redline account list|add|edit|disable|enable|remove|discover`. The dashboard and Android read account labels and
     status. The Android app shows account management read-only (status + "configure on the Mac" steps), using the existing
     authenticated daemon API.
7. **Android UI (A r3).** A `ClaudeAccountsCarousel` replaces the Claude `ProviderCard` when there are 2 or more Claude
   accounts.
   - Page 0 is Overview; pages 1..N are details, using `HorizontalPager`. The **pager height is fixed** to the tallest page,
     measured via `SubcomposeLayout`, so cards below never jump.
   - Navigation: Overview / ‹ › / Details› / Back. TalkBack custom actions and a live-region page label.
   - Reset credits row: "N available · next expires …" or "Not reported".
8. **Backward compatibility.** Every new JSON field is additive (`label`, `plan`, `openusage_card`, `account_status`,
   `banked_resets_expiries`). Mobile web, dashboard and tray keep working with unchanged code paths and show accounts as
   separate provider cards, as they do today for multiple providers. Follow-ups: dashboard/tray grouping, mobile web carousel.

## Test plan
- Go: config merge/validation (empty/single/multiple, conflicts), accounts file atomic write and permissions, migration
  adoption, per-account snapshot isolation (two Claude accounts never cross), OpenUsage card selection/stale/missing/email
  not stored, reset credits + expiry list, API account CRUD and 409s, dashboard JSON per account, scheduler reading its own
  account.
- Mobile core: grouping into a Claude account group, labels, reset credit fields.
- Android unit + Compose UI (Robolectric/instrumented as available): opens on Overview, Details› jump, Back to Overview,
  ‹ › disabled at the ends, fixed height across pages, large font, TalkBack actions.
- Emulator screenshots with fixture accounts against a local fixture daemon (synthetic data only).
