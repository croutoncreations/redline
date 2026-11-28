# Draft release notes (Unreleased)

## What changed

- **Claude Code team seats stay signed in.** Redline now asks Claude Code to renew each profile
  account's login shortly before it expires, preventing an unused seat from going out of date and
  holding its work after 8 hours. Redline still never writes a login. If renewal fails, the Mac and
  paired phones send an `account.needs_login` notification with the sign-in command. Disable the
  behavior with `usage_monitor.keep_profile_logins_fresh: false`.
- **Android now breaks banked resets down by kind.** A balance such as one full reset and three
  Fable resets reads "Full 1 · Fable 3" and expands to show each kind's deadlines and why any
  reset is held. Older desktops and out-of-date account data keep the previous count-and-deadlines
  display.
- Expanded banked-reset details on the mobile dashboard stay open across live usage updates,
  including a refresh that lands immediately after the list is opened.
- **Check for Updates stays visible.** A user-initiated check now brings Redline forward and raises
  Sparkle's prompt above the dashboard instead of letting it appear behind the app. Background
  update checks still avoid stealing focus.

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
