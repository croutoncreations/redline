# Port plan: window primer (#156) on per-account usage (#168)

This is a plan only; nothing here has been implemented. It covers whichever of #156 and #168 lands
second. The intended order is #156 → main (v0.1.11), then #86 → #168. Under that order, #168
inherits the primer and does this port. Line references are to #156 @ `a6ecc4a` and #168 @
`d18149c`.

## What breaks today

I merged `jf-claude-scheduler` into `jf-multi-account-usage-pr` in a scratch worktree and resolved
the conflicts toward #168. `go build ./...` then fails with:

```
internal/api/primer.go:40:81: not enough arguments in call to s.store.LatestSnapshotFromSource
	have (context.Context, string, string)
	want (context.Context, string, string, string)
```

Textual conflicts (8 files): `CHANGELOG.md`, `internal/api/dashboard.go`, `dashboard.css`,
`internal/api/server.go` (2 hunks), `internal/cli/cli.go` (2), `internal/nativeusage/client.go`,
`client_test.go`, and `internal/store/sqlite.go`.

## Decisions

1. **Primer state is already per account.** `primer_settings.provider_account_id` and
   `primer_attempts(provider_account_id, slot_key)` (#156 `internal/store/primer.go:17-48`) are keyed
   by the account ID, the same key #168 uses for `usage_snapshots.provider_account_id`. No table change
   and no schema version are needed. Keep `primerSchema` idempotent as it is; it must not claim v32.
2. **Profile accounts (`claude_config_dir`) become primable, with the ping run on the profile's login.**
   With #168, two Claude accounts no longer necessarily share one login. The pinger must apply the same
   environment rules as the `claude-code` harness for that account (#168
   `internal/harness/harness.go:350-363`):
   - drop any inherited `CLAUDE_CONFIG_DIR`;
   - for a profile account, also drop `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`,
     `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL` and `CLAUDE_CODE_USE_*`, then set
     `CLAUDE_CONFIG_DIR=<dir>`.

   Extract that block into one exported helper, e.g. `harness.ClaudeAccountEnv(env []string,
   configDir string) []string`, and call it from both the harness and `primer.ClaudePinger`. That way
   the two can never drift.
3. **`supported()` changes from "at most one Claude account" to "this account has its own login".**
   Rule for #156 `internal/primer/service.go:81-95`:
   - **Supported:** a Claude account with `claude_config_dir` set; or the only Claude account that
     reads the default login (no `claude_config_dir` and no non-default `openusage_card`).
   - **Unsupported:** OpenUsage-card accounts (`openusage_card` = `claude@…`). The ping would go
     through whichever login the Mac's default Claude Code profile holds, which is not necessarily
     the card's account. Use the reason "this account is read from an OpenUsage card; the primer can
     only ping a login Redline runs (the default login or a Claude Code profile)".
   - **Default login:** keep the existing refusal when more than one account resolves to the default
     login. #168's identity checks already refuse that configuration, so it should be unreachable.

   `primer.Service` needs `Providers map[string]config.Provider` (or a small struct holding provider
   kind, config dir, and card) instead of `map[string]string`.

## Code changes

| File | Change |
|---|---|
| `internal/api/primer.go:36-42` `latestPrimerSnapshot` | Call `s.store.LatestSnapshotFromSource(ctx, provider, configured.Provider, s.usageSources.Status(provider).Active)`. `provider` here is already the account ID (the `{provider}` path value). |
| `internal/api/primer.go:15-33` `newPrimerService` | Pass the full `config.Provider` per account (decision 3). `Refresh` keeps calling `s.fetchAndStore(ctx, provider)`; #168's `fetchAndStore(ctx, providerID)` (`server.go:2985`) already stores under the account. |
| `internal/api/server.go` `Server` struct | Keep both `primer *primer.Service` (#156) and `cursorUsage *cursorusage.Tracker` (#168). Imports: keep `pairing`, `relay`, and `primer`. |
| `internal/api/server.go` `StartScheduler` (#168 `:1000-1024`) | After the `usageMonitor` goroutine, add #156's `s.primer.Recover(ctx)` and the `s.primer.Run` goroutine, then #168's `cursorUsage` block. Both loops are independent. |
| `internal/api/server.go` `NewDemoServer` | Keep #156's `server.primer.Pinger = disabledDemoPinger{}` next to #168's `server.cursorUsage = nil`. |
| `internal/primer/pinger.go` `ClaudePinger.run` | Add `ConfigDir string` to the ping request (or pass `settings.ProviderAccountID`, which the service maps to its dir). Replace `ScrubEnvironment(p.environ())` with `ScrubEnvironment(harness.ClaudeAccountEnv(p.environ(), dir))`. Keep #156's scrub list; it is a superset for the default login. |
| `internal/primer/service.go` `supported` | Apply the rule from decision 3. |
| `internal/store/primer.go` `ActiveRunCount` use | Already per account (`ActiveRunCount(ctx, providerAccountID)`); no change. |
| `internal/nativeusage/client.go` `parseClaude` | Take #168's version, which has `ShortWindowNotStarted` / `ShortWindowUnavailable`. #156's change (a null reset makes `Short == nil`, not an error) is a subset of it. |
| `internal/primer/service.go` `skipReason` and `PlanReset` | They read `snapshot.Short`. With #168, an untouched window arrives as `Short == nil && ShortWindowNotStarted`, and both treat that as "no window open", which is the right behaviour. Add a test that pins it. |
| `internal/nativeusage/client_test.go` | Keep both sides' tests. #156's "null reset is a valid snapshot" test must assert `ShortWindowNotStarted` (utilization 0) or `ShortWindowUnavailable` (utilization > 0). |
| `internal/store/sqlite.go` | Keep #168's v28–v31 chain, then #156's idempotent `primerSchema` exec right before `tx.Commit()`. Update #156's comment: "v28 is stamped by the unmerged mobile branch" is no longer true once #86 is merged. |
| `internal/cli/cli.go` usage/help | Union: `…scheduler|primer|run|notification|reset|candidates|pause|resume|pair|relay|account|version`. Include `reset` if #172 has merged. |
| `internal/api/dashboard.go`, `dashboard.js`, `dashboard.css` | #156 adds a primer button and dialog on the Claude usage card. #168 rewrites the usage cards into account tiles. Put the primer entry on each **primable** Claude tile (use `primer.status.supported` from `GET /v1/providers/{id}/primer`) instead of the single Claude card. Re-run both Playwright specs (`tests/dashboard/dashboard.spec.js` primer cases and `dashboard-accounts.spec.js`). |
| `CHANGELOG.md` | #156's entries will already sit under `[0.1.11]`. Add an `[Unreleased]` line under Changed: "The window primer now works per account: Claude Code profile accounts are primed on their own login; OpenUsage-card accounts are not primable." |

## Tests to add during the port

- `primer`: a profile account's ping runs with exactly one `CLAUDE_CONFIG_DIR=<dir>` and without
  `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_*`. Reuse the fake runner from `service_test.go`.
- `primer`: with two Claude accounts, a default-login account plus a profile account, both are
  supported. An OpenUsage-card account is unsupported, with the new reason.
- `api`: `GET /v1/providers/{id}/primer` for a profile account returns its own snapshot. Seed snapshots
  for two accounts of the same provider and assert verification reads the right one. This is the
  per-account isolation property #168 exists for.
- `primer`: `PlanReset` / `skipReason` with a `ShortWindowNotStarted` snapshot plan an idle ping rather
  than treating the window as open.
- `harness`: `ClaudeAccountEnv` is used by both the harness and the pinger. One table test against the
  helper is enough.

## Order of work

1. Merge #86 into main (merge commit). Retarget #168 to main.
2. On #168's branch, merge main, which by then contains #156. Resolve conflicts per the table above.
3. Extract `harness.ClaudeAccountEnv` first, with no behaviour change, and run the harness tests.
4. Port `api/primer.go` and `primer.Service` (decision 3). Then add the tests.
5. Dashboard: primer entry per tile, then run Playwright.
6. `go test -race ./...`, `swift test`, `./gradlew testDebugUnitTest`, Playwright.

Estimated size: about 150–250 lines of Go plus the dashboard tile wiring. The risk is concentrated in
the dashboard merge, not the Go.
