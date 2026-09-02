# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

### Added

- Added a `redline demo` staging mode that seeds fully isolated, synthetic usage, discovery,
  revision, Hermes, and execution state for screenshots, recordings, and release rehearsals, with
  synthetic data clearly labeled in both the web dashboard and native menu-bar UI.
- Added a mobile dashboard: a tailnet-only HTTPS `/m` PWA reachable over Tailscale, with strict
  `.ts.net` host and proxy/origin checks, secure cookies, and one-time QR pairing. It offers
  Usage, a provider-specific Queue, and Runs/detail views with an offline-capable service worker
  and a Pixel 9 layout, plus new CLI commands for candidate preview and task-specific dispatch.
- `redline pair` now accepts `--port` for Tailscale Serve setups where the dashboard can't run on
  443; the pairing QR and endpoint label include the non-default port.
- Added launch screenshots and a README gallery covering the CLI, dashboard, and native app,
  captured entirely from the new demo staging mode so no personal repositories, paths, or live
  data appear in the images.

### Fixed

- Corrected the app icon's redline arc, which was drawn from a different circle than the white
  track and only met it at one end; the icon now shares one center, radius, and stroke width with
  the live menu-bar gauge, and the needle pivots from the dial's actual center.
- Replaced inverted "behind pace" wording with surplus-first copy in the dashboard's scheduling
  labels and the native menu-bar queue, and top-aligned the Harness and Model controls in the
  execution-profile editor.

## [0.1.6] - 2026-08-26

### Added

- Added auditable launch metrics for completed-job allowance, capacity reclaimed before expiry,
  scheduler WAIT frequency, and reserve behavior through the API and CLI.
- The macOS app now explains agent-driven folder permission prompts and detects a stale legacy
  LaunchAgent before it can compete with the app-owned service.
- Local macOS builds automatically use an available Developer ID Application identity, and ARM64
  packaging validates every bundled executable and library slice.
- Added a concise end-user getting-started guide, troubleshooting guide, agent-assisted install
  path, release-note template, and attributed links to Crouton Creations tools and builder updates
  from the README and embedded dashboard.
- After the first successful run, the menu-bar app now shows one small dismissible builder-updates
  prompt. The action menu also provides persistent, attributed links to updates and related tools.
- `redline --help` now returns a concise successful help page with project and update links.
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
- Completed runs now have a durable Activity inbox with unread state, human-readable summaries,
  bounded formatted logs, and clickable PR and web artifacts in both the dashboard and menu-bar
  app.
- Release packaging now produces a Universal DMG containing native Apple Silicon and Intel slices
  for both the menu-bar app and bundled service. CI exercises the Intel build on native
  `macos-15-intel` hardware.

### Fixed

- Prevent Redline from refreshing or writing Claude Code's shared macOS keychain credential; native
  monitoring now fails closed and asks the user to authenticate with Claude Code when refresh is
  required.
- Mark expired usage snapshots as unavailable in the dashboard and menu bar instead of presenting
  their last-known percentages as current scheduling data.
- Usage progress bars now render their actual remaining percentage under the dashboard's strict
  content-security policy instead of appearing completely full.
- Fixed Hermes desktop job triggers not falling back correctly when Hermes responded with
  `405 Method Not Allowed` (previously only `404` triggered the desktop fallback).
- The "Enable Notifications" flow now checks existing macOS notification permission before
  prompting, so already-authorized users get a confirmation instead of a re-prompt, and denied
  users get a warning with a direct link to System Settings.
- Confirmed Claude Code or Codex CLI sign-outs now pause scheduling for the affected provider,
  preventing a queue of jobs from failing with the same expired session. The failure card opens
  the supported login command and resumes scheduling when the user retries.
- API client errors now include the HTTP method, path, and response status, making failed
  operations actionable without enabling debug logging.
- Subprocesses configured with an explicitly empty environment no longer inherit Redline's parent
  environment and its credentials.
- A second service process now claims the API listener before opening SQLite, recovering runs, or
  starting scheduler loops. Duplicate launches therefore fail immediately instead of leaving
  orphan schedulers sharing the live database.
