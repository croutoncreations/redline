# Running Redline with launchd

Redline can run as a per-user macOS LaunchAgent. The example at
`deploy/com.jfox.redline.plist.example` deliberately uses placeholders so credentials, account
paths, and local tool locations are not committed.

The native app now supersedes this manual setup for new installations. Its **App Setup…** flow can
adopt an existing configuration, register the app at login, stop this LaunchAgent, and retain the
plist as a recoverable backup. Keep using the instructions below for headless installations or when
you deliberately want launchd—not the menu-bar app—to own the service.

The service needs an absolute binary path, config path, working directory, log paths, and a `PATH`
that includes every configured workspace and harness executable (`devx`, `codex`, `claude`, `pi`,
and `hermes`). If Gatepost captures harness traffic, put its shim directory first and retain the directory
containing each real binary later in `PATH`; discovery and execution then follow the same routing.
After rendering the template to `~/Library/LaunchAgents/com.jfox.redline.plist`:

```bash
plutil -lint ~/Library/LaunchAgents/com.jfox.redline.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.jfox.redline.plist
launchctl kickstart -k gui/$(id -u)/com.jfox.redline
```

Inspect the service and API:

```bash
launchctl print gui/$(id -u)/com.jfox.redline
curl -fsS http://127.0.0.1:7436/v1/health
redline scheduler status
redline health
```

To reload a changed plist, first use `launchctl bootout gui/$(id -u)/com.jfox.redline`, then
bootstrap it again. A normal process exit is restarted because the agent uses `KeepAlive`; graceful
SIGTERM still lets Redline finish active workers and close SQLite.

Usage monitoring depends on OpenUsage and a fresh Gatepost viewer database. Their processes or
LaunchAgents must remain active independently. Keep Gatepost's sync interval at or below Redline's
`usage_monitor.poll_interval` so each Redline cycle can observe newly completed agent calls.

Automatic dispatch remains a separate opt-in:

```yaml
scheduler:
  enabled: true
  poll_interval: 5m
```

Before enabling it, review the live profiles and queue with `redline profile list` and
`redline task list`. An automatic `WAIT` is a successful cycle and remains visible through
`redline scheduler attempts --provider <account>`.
