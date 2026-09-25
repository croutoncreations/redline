# Draft release notes (Unreleased)

## What changed

- **New: banked quota resets.** When Claude or Codex grants your account a one-time reset of an
  exhausted usage limit, Redline shows how many you hold and when the next one expires, on the
  dashboard, the mobile page, and the menu bar.
- Native usage collection now backs off after a provider rate-limits it, and remembers the
  lockout across restarts. Anthropic tightened its usage endpoint in late September and
  lengthens the penalty when asked again too soon.
- **Breaking for source builds:** Redline now requires Go 1.26 or later. Upgrade the local Go
  toolchain before building or contributing. Prebuilt CLI archives, the macOS app, and Homebrew
  installs need no migration.
- The local SQLite database and its sidecars are now owner-only on macOS, Linux, and Windows, and
  existing databases are tightened automatically at startup. Task `prompt_file` symlinks can no
  longer escape their workspace, and quickstart-created `redline.yaml` and `api-token` files are
  ignored by Git at the repository root.
- Stored timestamps now sort chronologically in SQLite. An automatic migration repairs existing
  data so scheduling, run and dispatch-attempt ordering, and launch-metrics ranges use the correct
  records.
- The MCP run tools now preserve the newest terminal events when truncating results and honor the
  requested run-list limit.
- Terminal pairing QR codes are scannable again, including their required blank quiet zone.
- Pi cache-token aliases are reconciled instead of added together, preventing inflated usage from
  unnecessarily throttling scheduling.
- The dashboards now keep unread-run badges accurate and use `1 hr` and `1 day` at relative-time
  boundaries. Activity summaries also fall back cleanly when a run output cannot be read.
- Hermes Gateway errors now include the failing operation, endpoint, status, response details, and
  invalid-URL reason where available. `task add` and `profile add` errors name the definition
  file that failed.
- Oversized custom day durations are rejected instead of being stored as ~292 years, unknown
  Codex models are no longer priced by partial name match, and `modernc.org/libc` moves off a
  retracted version that could crash when parsing `"nan"`.
- The onboarding wizard no longer lets you go back while a profile is saving.

## Install or update

Download the signed, notarized Universal DMG and matching SHA-256 file below. Redline supports
Apple Silicon and Intel Macs running macOS 13 or later. Existing installations can also choose
**Check for Updates…** from the Redline menu.

See the [getting-started guide](https://github.com/croutoncreations/redline/blob/main/docs/getting-started.md)
for first-run setup.

## Verification

- [ ] Go, dashboard, Swift, and native Intel CI pass.
- [ ] DMG checksum, Developer ID signature, Apple notarization, and stapling pass.
- [ ] Clean-install Tart rehearsal passes.
- [ ] Sparkle update rehearsal retains configuration, database, credential, tasks, and profiles.

---

Redline is built by
[Crouton Creations](https://www.croutoncreations.com/?utm_source=redline&utm_medium=release&utm_campaign=redline).
[Get new open-source tools and practical builder notes](https://buttondown.com/croutoncreations?utm_source=redline&utm_medium=release&utm_campaign=redline).
