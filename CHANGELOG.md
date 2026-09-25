# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

### Changed

- Bumped Go module dependencies (`go-sdk`, `modernc.org/sqlite`, `golang.org/x/*`, and their
  transitive pins). The `go` directive moves from 1.25 to 1.26 to match what `x/sys` and
  `modernc.org/libc` now require, raising the minimum Go toolchain needed to build Redline from
  source.

### Fixed

- The `redline task retry <id>` CLI confirmation no longer prints "retryd task" instead of
  "retried task". Enable and disable build their past tense by appending "d" to the verb, which
  doesn't work for retry.
- The dashboard and mobile PWA's relative-time labels no longer show "60 mins" or "24 hrs" for
  timestamps just under an hour or a day away; they now roll over to "1 hr" and "1 day" like every
  other bucket boundary.
- Hermes Gateway client errors (discover, list/trigger jobs, poll runs, read messages) now include
  the HTTP method, URL, and response body instead of a bare status code, and a rejected Gateway URL
  now says why (parse failure, missing host, wrong scheme) instead of just "invalid Hermes Gateway
  URL".
- Fixed a rare case where a run whose output or result file couldn't actually be read (for example
  it resolved to a directory) produced a summary made of NUL bytes instead of falling back to the
  default "Run completed successfully." message.
- The mobile dashboard's unread-run badge no longer under-counts after opening a run that was
  already marked read on a prior visit; it now applies the same already-read/terminal-state guard
  the desktop dashboard uses before decrementing.
- `redline_run_events` no longer drops the newest events when a response is truncated. Run events
  arrive oldest-first, so trimming the tail removed exactly the terminal `run.completed` or
  `run.failed` event an agent polling for completion is waiting on.
- `redline_runs_list` now forwards the requested limit to the API. `/v1/runs` defaults to 50 rows
  when no limit is sent, so asking for more silently returned 50 and reported them as untruncated.

- The SQLite database is now restricted to its owner. It was created under the process umask,
  commonly `0644`, leaving it world-readable along with its `-wal` and `-shm` sidecars — so any
  other local account could read task prompts, operator-authored prepare/finalize shell commands,
  and runtime credential references straight off disk, bypassing the HTTP bearer-token boundary.
  Existing databases are tightened on the next start. On Windows an owner-only DACL is applied,
  since the POSIX mode bits are ignored there, matching how the `api-token` file is protected.

- `prompt_file` is now confined to the workspace even when it is a symlink. The containment check
  was purely lexical, so a symlink inside the workspace pointing outside it passed validation and
  the harness read the link target — credentials, keys, any readable file — directly into the
  model prompt. Both the workspace root and the resolved file are now checked. Symlinks that stay
  inside the workspace keep working.
- The terminal pairing QR code is no longer rendered with inverted module polarity. `go-qrcode`
  sets a bitmap cell to true for a *dark* module, but the renderer negated it, so dark and light
  were swapped and the four-module quiet zone was drawn as solid ink. The resulting code could not
  be scanned, blocking mobile pairing from the CLI.
- Stored timestamps now use a fixed-width nanosecond field so that the byte-wise ordering SQLite
  applies to TEXT columns matches real chronological order. Previously `time.RFC3339Nano` trimmed
  trailing zeros and dropped the fraction entirely on a whole second, so `…T18:00:00Z` sorted after
  `…T18:00:00.5Z` and `…:00.1Z` after `…:00.12Z`. That corrupted `LatestSnapshot` (the scheduler
  could dispatch against a stale usage reading), run listings, dispatch-attempt ordering, and the
  `completed_at >= ?` range filter behind launch metrics. A new migration rewrites timestamps in
  existing databases, including legacy values stored with a numeric UTC offset; columns holding
  SQLite `CURRENT_TIMESTAMP` values are left untouched.
- Pi cache tokens are no longer double-counted when a session record carries more than one
  spelling of the same counter. Pi's JSONL schema expresses cache reads and writes as flat
  `cacheRead`/`cacheWrite`, a `cacheCreation` alias, and a nested `cache:{read,write}` object;
  these were being summed as if independent, so a record written across a schema migration could
  report two to three times its real cache usage. Redline now takes the largest alias, matching
  how Hermes records are already reconciled. Inflated usage made the scheduler under-dispatch,
  leaving paid subscription capacity unused.

## [0.1.7] - 2026-09-18

First public release. Redline is now open source under the Apache 2.0 license at
`github.com/croutoncreations/redline`, with a rewritten README, reorganized documentation, and
Homebrew distribution.

### Added

- Homebrew distribution through `croutoncreations/homebrew-tap`: `brew install --cask
  croutoncreations/tap/redline` installs the signed macOS app and
  `brew install croutoncreations/tap/redline` installs the standalone CLI on macOS or Linux.
  Tagged releases now publish CLI archives for darwin/linux/windows on amd64/arm64 with
  `checksums.txt` via GoReleaser. See `docs/releasing.md`.
- `redline version` and `redline --version` report the release version, commit, and build date,
  both in the standalone CLI and inside the macOS app bundle.
- Redline now builds and runs on Linux and Windows in addition to macOS: the CLI resolves a
  platform-appropriate default data directory (`~/.config/redline` on Linux,
  `%AppData%\redline` on Windows, unchanged `~/Library/Application Support/Redline` on macOS),
  workspace/notification hooks and the `command` harness type run through `cmd.exe` on Windows
  instead of assuming `/bin/sh`, and CI now cross-compiles for linux/windows on amd64/arm64 and
  runs a Linux and Windows smoke test of `serve`, `health`, `scheduler status`, and `task list`.
  On Windows the `api-token` file is created with an explicit owner-only ACL (current user and
  SYSTEM) passed to `CreateFile`, since the POSIX `0600` mode is ignored there and the file
  would otherwise inherit its directory's permissions; the Windows smoke test asserts this.
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
- Added a guided four-step first-run setup on the dashboard: it confirms each provider's CLI
  installation, agent sign-in, and subscription-usage access; helps create a workspace and
  execution profile with the account, harness, and model preselected where possible; and finishes
  by creating a first job that's saved enabled (the global scheduler stays off until you turn it
  on deliberately). It's safe to skip or interrupt — a resumable "Getting started" checklist stays
  on the dashboard and picks up at the first incomplete step. New jobs created from the dashboard
  now default to "Enabled after creation," with an explicit opt-out for saving a disabled draft.
  Stock-Mac provider discovery and native app handoff around first launch are also more reliable.

### Changed

- The README is rewritten for first-time visitors (install, how it works, MCP setup); reference
  material moved to `docs/` (`architecture`, `scheduling`, `profiles-and-tasks`, `hermes`,
  `api`, `cli`, `runs-and-notifications`, `releasing`) and contributor instructions to
  `CONTRIBUTING.md`.
- Unknown keys in `redline.yaml` are now reported as warnings on startup instead of preventing
  the service from starting, so a configuration written for another Redline build still loads.
  Type errors, malformed YAML, and invalid values remain fatal.
- When the menu-bar app's embedded service fails to start, the popover now shows the service's
  own diagnostic (typically the configuration error) with a **Show log** button, and the menu-bar
  tooltip reads "Redline could not start" rather than a generic offline state.
- `usage_monitor.gatepost_database` is now optional. Gatepost is an unreleased, private tool; the
  bundled `config.example.yaml` no longer requires it, and enabling `usage_monitor` without it
  simply skips the Gatepost/Pi import (still recording Redline's own run token counts) instead of
  producing a recurring error on every monitor cycle. A path that is explicitly configured but
  points at a missing file is skipped the same way; a configured path that exists but fails to
  open or query still surfaces an error.
- Renamed the LaunchAgent label from `com.jfox.redline` to `com.croutoncreations.redline` ahead of
  public release under the `croutoncreations` GitHub org. Existing installs with the old
  `com.jfox.redline` LaunchAgent are still detected and offered the same in-app migration (stop,
  back up, and hand ownership to the app) so upgrading users are not left with two competing
  agents. The app's bundle identifier (`ai.redline.mac`) is unchanged.
- The Go module path is now `github.com/croutoncreations/redline`, so
  `go install github.com/croutoncreations/redline/cmd/redline@latest` works.
- User-facing strings use hyphens rather than em dashes.

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
