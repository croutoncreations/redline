# Demo and screenshot staging

Redline includes an opt-in, isolated demo service for repeatable screenshots,
videos, and release rehearsals. Demo mode uses deterministic synthetic providers,
jobs, decisions, runs, logs, and artifacts. It does not read the normal Redline
configuration or database, discover local credentials, contact a provider, or run
an agent command.

## Start a scene

Build Redline, list the available scenes, then serve one on the dedicated demo
port:

```bash
go build -o ./redline ./cmd/redline
./redline demo list
./redline demo serve --scenario overview --open
```

The default address is `http://127.0.0.1:7446`. Demo mode refuses non-loopback
addresses and the production service port (`7436`). The header always displays a
red `DEMO` badge and the active scenario so synthetic data cannot be mistaken for
real work.

Available scenes:

- `overview`: providers, a populated dispatch queue, completed work, and a safe
  example artifact.
- `running`: the overview plus an active job.
- `attention`: the overview plus a failed job and recovery affordances.
- `empty`: configured providers before any jobs have been added.
- `decision-wait`: one bounded task with no actionable capacity surplus.
- `decision-run`: the same task with capacity surplus above the pace trigger.
- `decision-run-near-expiry`: the same task with substantial capacity remaining
  six hours before the weekly reset.
- `decision-unknown`: the same task held because its synthetic sample is stale.

Decision scenes default to Claude. Use `--provider codex-main` to stage the
same condition without a five-hour window:

```bash
./redline demo serve \
  --scenario decision-run-near-expiry \
  --provider codex-main \
  --state-dir /tmp/redline-demo-codex-expiry \
  --keep
```

The decision itself is computed by the production evaluator before being
persisted. Demo-specific copy translates internal pace-gap terminology into
surplus-first wording, but does not change `RUN`, `WAIT`, or `UNKNOWN`.

By default the service creates and removes a temporary state directory. To keep
the database, logs, and artifacts after a take for inspection, provide a new or
empty directory:

```bash
./redline demo serve \
  --scenario overview \
  --state-dir /tmp/redline-demo-take-01 \
  --keep
```

Redline refuses a non-empty state directory and the normal Application Support
directory. Each launch starts from a known fixture, so use a different empty
directory for another take.
This avoids any command that deletes or overwrites an existing database.

## Stage the native macOS app

With the demo service running, launch the menu-bar executable against its URL:

```bash
REDLINE_API_URL=http://127.0.0.1:7446 \
  ./dist/Redline.app/Contents/MacOS/RedlineMenuBar --show-dashboard
```

The native popover also displays `DEMO` and the scenario name. Quit that staged
process when the take is complete; the normal installed Redline app and service
remain on port `7436`.

## Record a safe run transition

Use the staged UI's run controls, or ask the demo API to admit one job:

```bash
./redline --api http://127.0.0.1:7446 \
  scheduler execute --provider claude-main --json
```

The demo executor briefly changes the job to running, writes a small synthetic
log below the demo state directory, and completes it. It never starts Codex,
Claude Code, Pi, Hermes, or a custom command. This is intended for recording the
activity inbox, run details, and live-update flow without consuming allowance.

Before publishing any media, manually review every frame. The fixtures avoid
owner-specific repositories and use `example.com` links, but any changes made
through the demo CRUD interface are preserved when `--state-dir` is used.
