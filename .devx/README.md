# Redline DevX sessions

Create a session from any directory after registering the project:

```bash
devx session create my-change --project redline
```

Each session runs on the macOS host so Go and native Swift builds use the same
toolchains as the installed app. DevX assigns an isolated API port, while
`.devx/redline.dev.yaml` keeps the session's SQLite database and run artifacts
inside that worktree's ignored `.redline/` directory. The development scheduler
and background usage monitor are disabled.

The `service` window starts the API automatically. From its shell pane:

```bash
.devx/open_dashboard.sh
go run ./cmd/redline \
  --config .devx/redline.dev.yaml \
  --api "$REDLINE_DEV_API" \
  health
```

The installed Redline app continues to use `127.0.0.1:7436` and its Application
Support database. DevX sessions do not reuse either one, and Caddy is disabled
because Redline intentionally rejects non-loopback hosts.
