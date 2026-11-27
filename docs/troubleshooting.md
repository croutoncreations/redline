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
- On macOS, Redline reads but never refreshes Claude Code's shared keychain credential. Claude Code
  or OpenUsage must refresh it; if authentication has expired, run `claude auth login`.
- Start one normal interactive Codex or Claude session if the provider has not produced a fresh
  subscription snapshot recently.
- A stale or unavailable snapshot fails closed: Redline waits instead of guessing and spending
  capacity. The UI labels old measurements as last-known data rather than showing them as current.

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

## An API credits account says "Session key needed"

OpenAI and Claude API credits accounts read their balance with a browser session key, which
expires. Until a fresh one is saved, work on the account holds. Copy a new key and save it on the
machine running Redline:

- OpenAI: the `Authorization` value (`sess-…`) of the `credit_grants` request on
  platform.openai.com › Settings › Billing › Credit grants.
- Claude: the `sessionKey` cookie (`sk-ant-sid…`) for platform.claude.com.

```bash
pbpaste | redline account set-session-key <account>
```

No restart is needed. Opt in to the `account.session_key_needed` notification to hear about this
when it happens. See [OpenAI keys](scheduling.md#keys) and [Claude keys](scheduling.md#claude-keys).

## API credits jobs stay queued

An API credits account says why it is holding on its card:

- **Holding credit** is the default `firesale` pacing working as intended: the promotional
  balance is kept until it is within `firesale_window` (default 72 hours) of expiring. Set
  `pacing: even` on the account to spend it steadily through the cycle instead, or start a
  manual firesale with `redline firesale start --provider <account> --for 1h`.
- **Held · at the floor** means the balance is at or below `min_promo_balance`.
- **Held · run in flight** means the balance, less `run_cost_estimate` for each run already
  running, would fall below the floor. Another job starts when one finishes.
- **Held · no promotional credit** means there is no subscription grant in effect. Purchased
  and other credit is shown as **Other credit** but is never spent.
- With no saved API key, work holds rather than run with another key:
  `pbpaste | redline account set-api-key <account>`.

## A Claude API credits pi job exits with status 78

A pi run on a Claude API credits account loads a small Redline extension that keeps every
request on the account's API key. If a model can't be moved onto the key (for example, an
`anthropic-cli` model with no API equivalent), or a request would go out on another provider,
the extension ends the run with status 78 rather than spend a subscription login. Use an
`anthropic/…` model the API offers, and don't add `--model`, `--provider` or `-e` to the
profile's `harness_args`. See [Claude API credits](scheduling.md#claude-anthropic-api-credits).

## The app reports a dispatch error

A dispatch error means Redline could not complete a scheduler check or launch operation; it does not
mean an agent merely failed its task. Open **Recent errors** for the provider, operation, and safe
error detail. Authentication failures receive a dedicated reconnect action.

If the same operational error repeats, pause that provider before changing configuration.

## A banked reset redemption failed

Check the reported reason. Common causes are a provider cooldown, multiple accounts of the same
provider configured, a Claude login that needs refreshing (`claude auth login`), or a Codex
API-key login instead of a ChatGPT subscription login. If Redline reports **outcome unknown**,
check the provider before trying another reset: the request may have succeeded even though its
response was lost.

See [Redeeming banked resets](scheduling.md#redeeming-banked-resets) for setup and safe retries.

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

For source installs and custom service locations, see [CONTRIBUTING.md](../CONTRIBUTING.md) and
the [CLI reference](cli.md).

## Uninstall completely

If you may want the task history later, first copy
`~/Library/Application Support/Redline/` to a safe location. Then:

1. Open **App Setup…**, turn off **Launch Redline at login**, and quit Redline.
2. Move `/Applications/Redline.app` to the Trash.
3. Remove `~/Library/Application Support/Redline/` to delete the configuration, local API
   credential, SQLite history, and run artifacts.
4. Remove `~/Library/Logs/Redline/` to delete service logs.

CLI-only installs on Linux and Windows keep the same files under `~/.config/redline/` and
`%AppData%\redline\` respectively; remove that directory and the `redline` binary.

People who used the pre-app LaunchAgent should also remove its retired files (the label was
`com.jfox.redline` before the public release; run both lines if you are unsure which you had):

```bash
for label in com.croutoncreations.redline com.jfox.redline; do
  launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/$label.plist" 2>/dev/null || true
  rm -f "$HOME/Library/LaunchAgents/$label.plist"
done
```

The LaunchAgent command only targets Redline's legacy user service. It is harmless when that file
is already absent. Redline does not install system extensions, privileged helpers, or files under
`/Library`.
