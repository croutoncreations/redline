# Runs, logs, and notifications

## Run output

Completed runs retain a concise result, delivery artifacts, and bounded formatted output so useful
work does not disappear just because it ran unattended. Output can be inspected without reading
artifact paths directly. Responses are tail-bounded to 64 KiB and may only resolve regular files
beneath the configured `run_artifacts_dir`; paths and symlinks that escape that root are rejected.

```bash
redline run logs <run-id>
redline run logs <run-id> --stream stderr --tail-bytes 8192
redline run logs <run-id> --json
```

## Lifecycle events

The lifecycle timeline records workspace preparation, harness execution, finalization, cleanup,
and the terminal run result. Task prompts are intentionally omitted. Execution-profile fields,
including lifecycle commands, are retained for reproducibility, so secrets should be passed via
the environment rather than embedded in profile command strings.

```bash
redline run events <run-id>
redline run logs <run-id> --stream prepare_stderr
redline run logs <run-id> --stream finalize_stdout
```

See [Architecture](architecture.md#execution-lifecycle) for the lifecycle state machine.

## Notifications

Native macOS alerts can be enabled from the Redline app and cover job starts, completions, and
failures.

Command notifications are disabled by default. When enabled, Redline invokes a trusted local
command with a versioned event document on stdin. Supported events are `run.started`,
`run.completed`, `run.failed`, and `scheduler.error`.

```yaml
notifications:
  enabled: true
  command: ./scripts/redline-notify
  timeout: 30s
  events: [run.started, run.completed, run.failed, scheduler.error]
```

The hook also receives `REDLINE_EVENT_TYPE`, `REDLINE_PROVIDER_ACCOUNT_ID`, `REDLINE_TASK_ID`, and
`REDLINE_RUN_ID`. Delivery failures are persisted but never alter the associated run or scheduler
outcome. A service restart marks an indeterminate pending delivery failed instead of retrying a
possibly non-idempotent hook.

Notification commands, workspace prepare/finalize hooks, and the **Custom command** harness are
run through `/bin/sh -lc` on macOS and Linux and through `cmd /C` on Windows. Windows hooks that
need a POSIX shell should invoke one explicitly (for example `bash -lc "..."` from Git Bash or WSL).

```bash
redline notification list
```

## Health

```bash
redline health --window 24h
```

Detailed health reports active/recent run counts, dispatch errors, and notification failures.
The lightweight `/v1/health` probe remains independent of recent operational failures.

## Related

- [Scheduling](scheduling.md#operational-history)
- [CLI reference](cli.md)
