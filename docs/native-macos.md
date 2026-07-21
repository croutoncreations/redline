# Native macOS app

Redline's first native shell is a menu-bar app built with AppKit. It keeps the existing local HTTP
service as the process boundary, so the web dashboard, CLI, future MCP interface, and native UI all
observe the same durable state.

## Service ownership

At launch the app decodes `http://127.0.0.1:7436/v1/dashboard`, which verifies both process
availability and API compatibility:

1. If a compatible Redline service is already healthy, the app adopts it and does not start a
   second process. This supports existing launchd installs and prevents duplicate OpenUsage polling.
2. Otherwise, the app starts the Go binary embedded at `Contents/Resources/bin/redline` using the
   persisted configuration selection or `~/Library/Application Support/Redline/redline.yaml`.
3. A bundled service is owned by the app and stopped when the app quits. An externally managed
   service is left running.

App-owned service output is retained under `~/Library/Logs/Redline/app-service.*.log`.

The app never exposes the service beyond its configured loopback address. On a fresh install it
copies the bundled starter configuration into Application Support with mode `0600`. The starter
enables read-only monitoring but leaves automatic dispatch disabled, so merely installing the app
cannot spend subscription capacity.

## First run and legacy migration

The first-run setup resolves configuration in this order: an explicitly persisted path, an
existing launchd configuration path, the standard Application Support path, then a new starter
configuration. This preserves a legacy service's exact storage locations and never overwrites an
existing file.

When `~/Library/LaunchAgents/com.jfox.redline.plist` exists, **App Setup…** offers an explicit
migration. Redline first refuses migration while a task is active, enables the main app as a macOS
login item, and only then stops the legacy service. The old plist is moved into
`~/Library/Application Support/Redline/Legacy LaunchAgents/`; the config, SQLite database, run
history, queue, and artifacts remain in their existing locations. If macOS requires login-item
approval, Redline opens System Settings and leaves the legacy service untouched until approval.

Choosing **Keep Existing Service** preserves the current adoption behavior. App Setup remains
available from the quick panel for migration later.

## Current menu

- Gauge-style status icon with a red limit segment and a state indicator.
- A state-colored gauge with a usage-sensitive needle plus compact provider logos and weekly
  availability; the full `WAIT`, `RUN`, `ATTN`, or `OFFLINE` description remains in accessibility
  text and the quick panel.
- Current weekly and five-hour availability for Codex and Claude.
- Model-specific allowances such as Fable in a provider submenu.
- Active-run, scheduler, operational-health, and service-ownership summaries.
- A native dashboard window backed by `WKWebView`, with connection status, refresh, and an optional
  Open in Browser action.
- A native quick-status popover with provider capacity, scheduler and run state, the next queued
  tasks, the latest dispatch decision, and a link to the full dashboard.
- Per-provider pause/resume controls and direct native access to recent stdout/stderr and lifecycle
  hook logs.
- User-enabled macOS notifications when a run completes or fails. Existing run history is treated
  as a baseline, so enabling notifications does not replay stale alerts.
- Show Dashboard, notification setup, refresh, and quit actions.
- App Setup for launch-at-login status and recoverable legacy-service migration.
- Automatic refresh every 20 seconds.

The native window intentionally reuses the live web dashboard for full task/profile CRUD. This
keeps one implementation of the management interface while giving it normal app-window behavior
instead of sending the user to a browser tab.

## Build

```bash
./scripts/build-macos-app.sh
open dist/Redline.app
```

The build creates one ad-hoc-signed `Redline.app` containing the Swift menu process, Go service,
and safe starter configuration. Set `REDLINE_SIGN_IDENTITY` to a Developer ID identity for
distributable signing, and `REDLINE_APP_OUTPUT_DIR` to change the output directory.

Every build signs the menu process and nested Go service separately with Hardened Runtime enabled.
Local builds use an ad-hoc identity and no timestamp. Version metadata is configurable:

```bash
REDLINE_VERSION=0.2.0 REDLINE_BUILD_NUMBER=2 ./scripts/build-macos-app.sh
```

## Release DMG and notarization

The release packager creates an architecture-labelled drag-to-Applications DMG. A distributable
build fails closed unless both a Developer ID Application identity and a `notarytool` Keychain
profile are supplied. Create that profile interactively once:

```bash
xcrun notarytool store-credentials redline-notary \
  --apple-id "you@example.com" \
  --team-id YOUR_TEAM_ID \
  --password YOUR_APP_SPECIFIC_PASSWORD
```

Then build the release:

```bash
REDLINE_VERSION=0.2.0 \
REDLINE_BUILD_NUMBER=2 \
REDLINE_SIGN_IDENTITY="Developer ID Application: Example, Inc. (TEAMID)" \
REDLINE_NOTARY_PROFILE=redline-notary \
./scripts/package-macos-release.sh
```

The script signs with a secure timestamp, notarizes and staples the app, builds and signs the DMG,
notarizes and staples the DMG, validates it with Gatekeeper, and retains Apple’s result and issue
logs plus a SHA-256 checksum beside the release. For a local packaging test only, set `REDLINE_SIGN_IDENTITY=-` and
`REDLINE_ALLOW_UNNOTARIZED=1`; the resulting DMG is clearly reported as non-distributable.

## Next native phases

1. Choose and integrate an update mechanism such as Sparkle, including signed appcast generation.
2. Add a universal binary build when Intel distribution becomes a product requirement.
3. A richer native popover only if tray workflows outgrow the menu and local dashboard split.
