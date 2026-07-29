# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

### Added

- Native macOS alerts now fire when a run starts, in addition to completing or failing. The
  `notifications` command hook gained a matching `run.started` event, so custom notification
  scripts can subscribe to it via `events: [run.started, run.completed, run.failed,
  scheduler.error]`.
- The dashboard and menu bar app now surface failed runs as an actionable alert instead of a
  silent state change: a "Job needs attention" banner shows the failure reason, and a one-click
  "Retry job" (or "Resume & retry" if the provider account is paused) button retries it directly.
  Redline also recognizes when a harness failed because the CLI was signed out and shows the
  specific `claude auth login` / `codex login` remediation instead of a generic error.
- Policies can now set `pace_gap_trigger` to admit work as soon as weekly usage falls a configured
  number of percentage points behind an even burn pace, without waiting on a fixed time threshold.
  The bundled `standard` policy uses `0.30` and `early` uses `0.15`.
- Added a `prepare-upgrade-from-dmg` mode to the macOS release rehearsal script so a signed
  baseline DMG can be rehearsed as a prerelease upgrade, working around GitHub's
  `releases/latest` endpoint excluding prereleases.

### Fixed

- Fixed Hermes desktop job triggers not falling back correctly when Hermes responded with
  `405 Method Not Allowed` (previously only `404` triggered the desktop fallback).
- The "Enable Notifications" flow now checks existing macOS notification permission before
  prompting, so already-authorized users get a confirmation instead of a re-prompt, and denied
  users get a warning with a direct link to System Settings.
