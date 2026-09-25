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
redline task add --name "Parser tests" --prompt "Add table tests for the parser." \
  --profile claude-worktree --tier behind --priority 50 --json
echo "Add table tests for the parser." | redline task add --name "Parser tests" --prompt - --profile auto --json
redline task list
redline task enable add-tests
redline task disable add-tests
redline task dispatch add-tests
redline candidates --provider codex-main
```

`task add` without `--file` builds the task from flags. `--prompt -` reads the prompt from stdin
(up to 256 KiB). `--profile auto` picks the Claude Code profile whose `repository` is the git top
level of the current directory (`--cwd` overrides the directory and `--harness` the harness type).
A non-matching `--default-profile` requires `--allow-default-profile`. Other flags: `--prompt-file`, `--type`
(`one_off` default), `--tier` (`behind` default), `--priority` (50 default), `--min-interval`, and
`--disabled` to save a draft.

### Queue work for later

```bash
redline later "finish the parser refactor and run go test ./..."
echo "write the migration and its test" | redline later -
redline later --tier expiring --json "sweep the docs for stale flags"
# For hook-supplied text, pass it through stdin: redline later --prompt - --json
```

`later` queues free text as a one-off task using `--profile auto` resolution. It never dispatches
and never bypasses limits: the task starts in a fresh harness session only when the provider is
behind pace (or near expiry for `--tier expiring`) and above its reserve. If no profile matches,
it fails closed unless `--default-profile ID --allow-default-profile` is passed explicitly; the
text output warns that the fallback may target another repository. Hooks should pass untrusted
text via `--prompt -` and stdin, never as positional flags. For positional text starting with a
hyphen, use `redline later -- "-something"`. With `--json` it prints the created task, how the
profile was chosen, and the matched repository. `task add --json`, in both YAML and flag mode,
prints the created task object directly.

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
redline run watch --jsonl
redline run watch --jsonl --interval 30s --count 1
```

`run watch --jsonl` prints one JSON line per run that finishes after the watch starts:
`{"task_id", "task", "run_id", "status", "summary", "pr_url", "completed_at"}`. It polls the
loopback API every 10 seconds by default and pages through completions with a durable sequence
cursor, including runs that started before the watch began but finished afterward. Cursor state is
in memory, so restarting a watcher begins at the latest completion, not the previous position.

## Machine-readable output

`task list`, `profile list`, `run list`, `run show`, `run events`, `health`, `candidates`, and the
`scheduler status|history|attempts` commands always print JSON. `status`, `decision`,
`usage refresh`, `task add`, `later`, `scheduler evaluate|execute`, and `run logs` print JSON with
`--json`.

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
