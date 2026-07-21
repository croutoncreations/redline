# Native macOS app

Redline's first native shell is a menu-bar app built with AppKit. It keeps the existing local HTTP
service as the process boundary, so the web dashboard, CLI, future MCP interface, and native UI all
observe the same durable state.

## Service ownership

At launch the app decodes `http://127.0.0.1:7436/v1/dashboard`, which verifies both process
availability and API compatibility:

1. If a compatible Redline service is already healthy, the app adopts it and does not start a
   second process. This supports existing launchd installs and prevents duplicate OpenUsage polling.
2. Otherwise, the app starts the Go binary embedded at `Contents/Resources/bin/redline` using
   `~/Library/Application Support/Redline/redline.yaml`.
3. A bundled service is owned by the app and stopped when the app quits. An externally managed
   service is left running.

App-owned service output is retained under `~/Library/Logs/Redline/app-service.*.log`.

The app never exposes the service beyond its configured loopback address. A missing configuration
is shown as an unavailable state rather than silently creating an unsafe or incomplete setup.

## Current menu

- Gauge-style status icon with a red limit segment and a state indicator.
- Compact menu-bar text for `WAIT`, `RUN`, `ATTN`, or `OFFLINE` plus each provider's weekly
  availability.
- Current weekly and five-hour availability for Codex and Claude.
- Model-specific allowances such as Fable in a provider submenu.
- Active-run, scheduler, operational-health, and service-ownership summaries.
- A native dashboard window backed by `WKWebView`, with connection status, refresh, and an optional
  Open in Browser action.
- Show Dashboard, refresh, and quit actions.
- Automatic refresh every 20 seconds.

The native window intentionally reuses the live web dashboard for full task/profile CRUD. This
keeps one implementation of the management interface while giving it normal app-window behavior
instead of sending the user to a browser tab.

## Build

```bash
./scripts/build-macos-app.sh
open dist/Redline.app
```

The build creates one ad-hoc-signed `Redline.app` containing both the Swift menu process and Go
service. Set `REDLINE_SIGN_IDENTITY` to a Developer ID identity for distributable signing, and
`REDLINE_APP_OUTPUT_DIR` to change the output directory.

## Next native phases

1. First-run configuration and migration from an existing launchd installation.
2. Launch-at-login using `SMAppService`, with the app as the only user-facing install.
3. Native notifications and quick controls such as pause/resume and run-log access.
4. Hardened Runtime, Developer ID signing, notarization, Sparkle or another update path, and a DMG.
5. A richer native popover only if tray workflows outgrow the menu and local dashboard split.
