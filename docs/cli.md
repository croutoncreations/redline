# CLI reference

Every `redline` command except `serve`, `mcp`, and `demo` is a client of the local HTTP API.
The default API is `http://127.0.0.1:7436`; override it with `--api` before the subcommand:

```bash
redline --api http://127.0.0.1:8000 status --provider claude-main
```

When running from source, substitute `go run ./cmd/redline` for `redline`.

## Service

```bash
redline version                                           # release version, commit, build date
redline --config redline.yaml serve                       # run the service
redline --config redline.yaml serve --listen 127.0.0.1:17436
redline mcp                                               # stdio MCP server (see mcp.md)
redline health
redline health --window 24h
```

## Usage and decisions

```bash
redline usage refresh --provider codex-main --json
redline status --provider codex-main
redline decision --provider codex-main
redline calibration --provider claude-main
redline token sync --provider claude-main
redline capacity --provider claude-main
redline metrics launch --days 21
```

## Profiles and tasks

```bash
redline profile add --file examples/codex-devx-profile.yaml --json
redline profile list
redline task add --file examples/add-tests-task.yaml --json
redline task list
redline task enable add-tests
redline task disable add-tests
redline task dispatch add-tests
redline candidates --provider codex-main
```

## Scheduler

```bash
redline scheduler evaluate --provider codex-main --json
redline scheduler evaluate --provider codex-main --revision "$(git rev-parse HEAD)"
redline scheduler execute --provider codex-main --json
redline scheduler status
redline scheduler history --provider codex-main
redline scheduler attempts --provider codex-main
redline pause --provider codex-main
redline resume --provider codex-main
```

## Runs

```bash
redline run list
redline run show <run-id>
redline run events <run-id>
redline run logs <run-id>
redline run logs <run-id> --stream stderr --tail-bytes 8192
redline run logs <run-id> --stream prepare_stderr
redline run logs <run-id> --stream finalize_stdout
redline run logs <run-id> --json
```

## Notifications

```bash
redline notification list
```

## Mobile pairing

```bash
redline pair
redline pair --port 8443
```

See [Mobile dashboard setup](mobile.md).

## Related

- [HTTP API](api.md)
- [Scheduling](scheduling.md)
- [Execution profiles and tasks](profiles-and-tasks.md)
