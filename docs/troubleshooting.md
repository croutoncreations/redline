# Troubleshooting Redline

## A provider says credentials expired

Redline pauses the affected provider after a confirmed CLI authentication failure so the rest of
that queue does not fail the same way.

- Claude Code: run `claude auth login`
- Codex CLI: run `codex login`

After the CLI succeeds interactively, use **Retry after login** on the failed job. Redline resumes
the provider as part of that recovery action.

## Usage is missing or stale

Open the provider details and inspect **Usage source**.

- If OpenUsage is already running, confirm `http://127.0.0.1:6736/v1/limits` responds.
- Redline automatically falls back to its native collector after repeated OpenUsage failures.
- Start one normal interactive Codex or Claude session if the provider has not produced a fresh
  subscription snapshot recently.
- A stale or unavailable snapshot fails closed: Redline waits instead of guessing and spending
  capacity.

## A job has not run

Open the provider card and the latest scheduler decision. Common `WAIT` reasons include:

- the task's **Run when** tier is not unlocked yet;
- its minimum interval has not elapsed;
- **Only rerun after the repository changes** is enabled and the base revision is unchanged;
- another run holds the provider, model-pool, runtime-connection, or agent-context concurrency slot;
- the provider or scheduler is paused;
- the execution profile or repository is unavailable.

Priority does not make a task eligible sooner; it orders jobs within the currently unlocked tiers.

## A job failed

Open **Activity** or the failure card and choose **View logs**. Redline stores the terminal result,
artifacts, lifecycle events, and bounded stdout/stderr even if you missed the notification. URLs
such as pull requests are clickable.

Redline preserves isolated workspaces by default. Agent instructions and optional finalize hooks
own commit, push, PR, and cleanup behavior.

## The app reports a dispatch error

A dispatch error means Redline could not complete a scheduler check or launch operation; it does not
mean an agent merely failed its task. Open **Recent errors** for the provider, operation, and safe
error detail. Authentication failures receive a dedicated reconnect action.

If the same operational error repeats, pause that provider before changing configuration.

## Notifications are not appearing

Choose **Notifications…** in the menu-bar app. If macOS previously denied access, Redline links to
the correct System Settings pane. Notifications cover job starts, completions, and failures; the
Activity inbox remains the durable source of truth.

## Redline appears to be running twice

Current releases claim the loopback listener before opening the database or starting scheduler
workers, so a duplicate process exits immediately. If you migrated an older launchd installation,
use **App Setup…** to let the menu-bar app take ownership, then confirm:

```bash
lsof -nP -iTCP:7436 -sTCP:LISTEN
```

There should be one Redline listener.

## Get more detail from the CLI

The bundled executable lives at:

```text
/Applications/Redline.app/Contents/Resources/bin/redline
```

Useful read-only commands include:

```bash
/Applications/Redline.app/Contents/Resources/bin/redline status --provider claude-main
/Applications/Redline.app/Contents/Resources/bin/redline scheduler status
/Applications/Redline.app/Contents/Resources/bin/redline health
/Applications/Redline.app/Contents/Resources/bin/redline run list
```

For source installs and custom service locations, see the main [README](../README.md).
