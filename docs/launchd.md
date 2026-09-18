# Running Redline with launchd

Redline can run as a per-user macOS LaunchAgent. The example at
`deploy/com.croutoncreations.redline.plist.example` deliberately uses placeholders so credentials,
account paths, and local tool locations are not committed.

The native app now supersedes this manual setup for new installations. Its **App Setup…** flow can
adopt an existing configuration, register the app at login, stop this LaunchAgent, and retain the
plist as a recoverable backup. Keep using the instructions below for headless installations or when
you deliberately want launchd - not the menu-bar app - to own the service.

The service needs an absolute binary path, config path, working directory, log paths, and a `PATH`
that includes every configured workspace and harness executable (`devx`, `codex`, `claude`, `pi`,
and `hermes`). After rendering the template to
`~/Library/LaunchAgents/com.croutoncreations.redline.plist`:

```bash
plutil -lint ~/Library/LaunchAgents/com.croutoncreations.redline.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.croutoncreations.redline.plist
launchctl kickstart -k gui/$(id -u)/com.croutoncreations.redline
```

Inspect the service and API:

```bash
launchctl print gui/$(id -u)/com.croutoncreations.redline
curl -fsS http://127.0.0.1:7436/v1/health
redline scheduler status
redline health
```

To reload a changed plist, first use `launchctl bootout gui/$(id -u)/com.croutoncreations.redline`,
then bootstrap it again. A normal process exit is restarted because the agent uses `KeepAlive`;
graceful SIGTERM still lets Redline finish active workers and close SQLite.

If usage monitoring reuses a local OpenUsage API, that process or LaunchAgent must remain active
independently; otherwise Redline falls back to its native collectors.

Existing installs that used the earlier `com.jfox.redline` label are migrated by the menu-bar
app's **App Setup…** flow, or can be replaced manually by booting out the old label and
bootstrapping the new plist.

Automatic dispatch remains a separate opt-in:

```yaml
scheduler:
  enabled: true
  poll_interval: 5m
```

Before enabling it, review the live profiles and queue with `redline profile list` and
`redline task list`. An automatic `WAIT` is a successful cycle and remains visible through
`redline scheduler attempts --provider <account>`.
