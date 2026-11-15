# Execution profiles and tasks

An **execution profile** describes *how* an agent runs: the provider account whose allowance it
spends, the harness (Codex CLI, Claude Code, Pi, Hermes, or a custom command), the model, the
repository, and how the workspace is prepared and cleaned up. A **task** (shown as a *job* in the
dashboard) is a prompt plus a profile, a dispatch tier, a priority, and optional recurrence.

Both can be managed from the dashboard, the CLI, the HTTP API, or MCP. This guide covers the
CLI/YAML route and the details behind the dashboard's controls.

## Importing from YAML

```bash
redline profile add --file examples/codex-devx-profile.yaml --json
redline profile add --file examples/claude-worktree-profile.yaml --json
redline profile add --file examples/claude-fable-devx-profile.yaml --json
redline task add --file examples/add-tests-task.yaml --json
redline profile list
redline task list
redline task disable add-tests
redline task enable add-tests
redline candidates --provider codex-main
redline task dispatch add-tests
redline task delete add-tests
```

See the [`examples/`](../examples) directory for the full set of starter profiles and tasks.
`redline task delete <id>` permanently removes a task. Running tasks and tasks with run or
scheduler history cannot be deleted; disable tasks with history instead.

## Task prompts

A task needs either an inline `prompt` or a `prompt_file` naming a file the harness reads from the
prepared workspace at run time:

```yaml
prompt: Add table-driven tests for the parser package.
# ...or, instead:
prompt_file: prompts/add-tests.md
```

`prompt_file` is resolved relative to the workspace directory and is confined to it. Redline
rejects an absolute path, one that traverses upward out of the workspace, and one that resolves
outside it through a symlink.

Because the file is read when the task runs rather than when it is created, editing it changes
what the next run receives — useful for prompts kept under version control alongside the code they
operate on.

## Dispatch tier and priority

```yaml
dispatch_tier: behind # behind, well_behind, expiring, or firesale
priority: 60
```

The tier controls *when* a task becomes eligible; priority only orders tasks whose tiers are
already unlocked. See [Scheduling](scheduling.md#dispatch-tiers-and-priority) for how tiers are
derived from allowance pace.

## Firesale recurrence

A recurring task may set a separate positive interval for provider-level firesales:

```yaml
min_interval: 7d
firesale_min_interval: 10m # optional; unset uses min_interval
```

There is no ordering constraint between the intervals. Repository-change checks still apply.
Both intervals apply only to recurring tasks: a one-off task never runs again after it succeeds,
so the API accepts but ignores intervals on one-off tasks and the dashboard hides both fields.
Use `--firesale-interval 10m` with `task add` or `later`, or edit the field in the dashboard,
HTTP API, or MCP. The `firesale` tier waits for a policy's firesale trigger; see
[effective horizon and firesale](scheduling.md#effective-horizon-and-firesale).

## Harness and model discovery

The dashboard discovers installed Codex CLI, Claude Code, Cursor CLI, Pi, and Hermes harnesses,
shows each CLI version, and builds model choices from local, refreshable catalogs rather than
hardcoded presets. The Cursor CLI's choices come from `agent models`, the signed-in account's own
list, so they follow what Cursor offers without a Redline update; the catalog is refreshed every
five minutes and on demand with the refresh button. A signed-out Cursor CLI is shown as such and is
not asked for models.
Codex uses its own model cache. Pi uses its offline model listing; those subscription-backed Pi
models also supply Claude Code's versioned choices because Claude Code accepts full model names but
does not provide a model-list command. Pi profiles retain provider-qualified model IDs such as
`openai-codex/gpt-5.6-sol` and `anthropic-cli/claude-opus-4-8`, keeping the model tied to the usage
pool Redline monitors. **Other model** and **Custom command** remain available when discovery is
incomplete or a local integration is not built in.

Repository paths previously used by profiles are remembered as suggestions and can always be typed
directly.

## Model pools and allowance routing

Some providers expose separate model-specific allowance pools. Claude's Fable pool is the current
example: Fable tasks require both shared Claude capacity and the Fable weekly pool, while Haiku,
Sonnet, and Opus use shared pools only.

Fable profiles may set `budget_model_group: fable`; Redline also recognizes `fable`,
`claude-fable-5`, and `claude-fable-latest` model aliases. Provider-qualified Pi Fable IDs are
inferred automatically.

### Cursor

A `provider: cursor` account is scheduled from Cursor's two monthly subscription pools, which
OpenUsage (or Redline's read-only native collector) reports. Every Cursor model draws from one:

| Model | Pool |
| --- | --- |
| `auto`, `default`, `composer-*` | Cursor Models (`cursor_models`) |
| anything else (Claude, GPT, Gemini, Grok, …) | Other Models (`other_models`) |

A job runs only when the pool its model uses is itself on pace against the month (not a week),
and still holds more than a reserve: the larger of the policy's `rolling_reserve` and the
account's `window_weekly_cost`. The account's overall monthly figure admitting work does not
override a pool that says wait. On-demand spend, credits and Grok Bot's weekly allowance are never
counted: when both pools are used up, Cursor jobs wait for the monthly reset rather than spend
money. Redline decides before a run starts and cannot stop one partway, so a long run started
near a pool's reserve can still go past it. Set Cursor's on-demand limit to $0 if you need that to
be impossible.

Jobs run through the Cursor CLI's headless mode, sandboxed:

```text
cursor-agent --print --output-format stream-json --trust --sandbox enabled \
  --workspace <dir> --model <id>
```

The prompt goes in on standard input. In the sandbox the agent edits files and runs commands in
its workspace, while writes elsewhere and network access are refused. `--force` is not passed: it
overrides the sandbox and approves every command, so add it to `harness_args` only if a job needs
that. `harness_args` may not set `--workspace`, `--add-dir`, `--api-key` or `--endpoint`. A
`default` model runs as `auto`, so the run draws on the pool it was scheduled against.

The CLI uses its own login (`agent login`). Redline stores no Cursor key, and removes
`CURSOR_API_KEY`, `CURSOR_AUTH_TOKEN` and `CURSOR_API_ENDPOINT` from the CLI's environment, so a
key elsewhere on the machine cannot bill runs to another account.

Usage is read from the Cursor app's session (directly, or through OpenUsage, which reads the same
session), while jobs run as the CLI's login. Before each usage read Redline confirms the two are
the same account: it compares the Cursor app's signed-in email with the one `cursor-agent status`
reports, as SHA-256 digests, and never logs or stores either address. If they differ, or either
cannot be confirmed, no usage is read, the account shows **Accounts differ**, and nothing is
scheduled on it until both are signed in to the same account. A confirmed match is reused for five
minutes; a mismatch is checked again on every read, so signing in again takes effect at the next
usage poll.

When the CLI is signed out the run fails with "Cursor CLI is signed out. Run `agent login`", and
the account is paused until you sign in and resume it.

```yaml
providers:
  cursor-main:
    provider: cursor
    usage_source: openusage
    openusage_url: http://127.0.0.1:6736
    window_weekly_cost: 0.10   # one run's share of a pool; also the pool reserve if larger
```

The advanced **Allowance routing override** in the dashboard corresponds to `budget_model_group`;
leave it automatic unless a provider exposes a separate model-specific pool that model-name
inference cannot identify.

Claude status output includes supplemental model pools when the usage source reports them:

```text
claude: 5-hour 100.0% remaining, 73.0% weekly remaining (...)
  Fable: 48.0% remaining (...)
```

## Workspaces

Redline supports these workspace providers:

- **Git worktree** - an isolated checkout per run under `.redline/worktrees` in the profile's
  repository. The recommended default for code-changing jobs. Redline creates
  `.redline/.gitignore` containing `*` if that file is absent, keeping the directory and run
  worktrees out of `git status`, including in linked worktrees and repository subdirectories.
  Existing ignore files are preserved; your checkout's tracked `.gitignore` is never changed.
- **DevX** - an isolated [DevX](https://github.com/jfox85/devx) session. Creation
  arguments are configurable, e.g. `workspace_args: [--target, host]`.
- **Existing directory** - runs directly in a checkout. Only use this when you explicitly accept
  direct edits to that directory.
- **Generic command** - a custom command that prepares the workspace.

Optional prepare/finalize hooks run before and after the harness. Cleanup defaults to `never`;
`on_success` and `always` are also supported. Execution-profile fields, including lifecycle
commands, are retained with each run for reproducibility, so pass secrets via the environment
rather than embedding them in profile command strings.

## When a job hits the usage limit

Claude Code, Codex CLI, and pi runs are checked for a usage-limit stop. The check reads the
CLI's own error events (Claude's rejected `rate_limit_event` or an `is_error` result saying the
limit was hit, Codex's `error`/`turn.failed` events, pi's `stopReason: "error"`). Tool output in
the transcript is not checked, so a task that only mentions a limit is not flagged. Claude Code
can exit 0 when it stops at the limit; that still counts as a stop.

On a usage-limit stop, Redline:

- records a `usage_limit.reached` run event with the harness session ID, the window that ran
  out, and its reset time when the CLI reported them;
- marks the run failed with outcome `usage_limited`. It does not pause the provider the way a
  sign-out does, because the scheduler already reads the limit from usage polling;
- keeps the workspace, whatever the cleanup policy, so the job can be resumed.

Set `on_usage_limit_command` to hand the job to something else at that point. It runs in the
kept workspace before finalize and cleanup. The task prompt arrives on stdin, along with this
environment:

| Variable | Meaning |
| --- | --- |
| `REDLINE_HARNESS`, `REDLINE_MODEL` | The harness and model that stopped |
| `REDLINE_HARNESS_SESSION_ID` | Its session, for `claude --resume`, `codex exec resume`, or `pi --session` |
| `REDLINE_CLAUDE_CONFIG_DIR` | The Claude Code profile directory a profile-bound account ran with; resume its sessions with `CLAUDE_CONFIG_DIR=$REDLINE_CLAUDE_CONFIG_DIR claude --resume ...`. Empty for the default login. `CLAUDE_CONFIG_DIR` itself is not set, so the hook does not spend the account that ran out. |
| `REDLINE_USAGE_LIMIT_TYPE`, `REDLINE_USAGE_LIMIT_RESETS_AT`, `REDLINE_USAGE_LIMIT_MESSAGE` | What ran out and when it resets (RFC 3339), when known |
| `REDLINE_WORKSPACE_DIR`, `REDLINE_WORKSPACE_BRANCH` | The kept workspace |
| `REDLINE_OUTPUT_FILE`, `REDLINE_ERROR_FILE` | The stopped harness's transcript and stderr |
| `REDLINE_RUN_ID`, `REDLINE_TASK_ID`, `REDLINE_TASK_NAME`, `REDLINE_PROVIDER_ACCOUNT_ID`, `REDLINE_REPO`, `REDLINE_BASE_BRANCH`, `REDLINE_RESULT_FILE` | Run context |

Exit 0 means the command finished the job. The run then completes with outcome `handed_off`,
and finalize and cleanup run as for a success. Any other exit leaves the run failed and the
workspace kept. Output goes to the `usage_limit_stdout` and `usage_limit_stderr` log streams
(`redline run logs <run-id> --stream usage_limit_stdout`). Finalize hooks also get
`REDLINE_RUN_OUTCOME` (`completed`, `failed`, `usage_limited`, or `handed_off`).

`examples/on-usage-limit-overflow.sh` continues the job in pi on another model, for example an
OpenAI-compatible endpoint:

```yaml
# Copy the example somewhere stable first; the command runs in the job's workspace.
on_usage_limit_command: OVERFLOW_MODEL=openrouter/qwen/qwen3-coder /path/to/on-usage-limit-overflow.sh
```

The run stays active until the command exits, and Redline sets no timeout on it. Until then it
keeps its account's concurrency slot, so give a long-running hook its own timeout (for example
`timeout 2h ...`). Whatever the other harness or endpoint costs is not counted against any
Redline account.

### Retrying after the reset

Set `retry_after_usage_limit: true` on the profile (in the dashboard: **Retry after the usage
limit resets**) to put a usage-limited job back in the queue. It is held until the reset time the
CLI reported plus two minutes, or for one hour when no reset time was reported. After that it
goes through the scheduler like any other queued job, so it also waits for usage polling to show
the allowance is back. While held, the scheduler lists it with "waiting for the usage limit to
reset at ...".

The retry starts a fresh run in a new workspace. The stopped run's workspace and session are
kept, and Redline does not resume that session itself. Have the prompt check the repository for
earlier progress, or use `on_usage_limit_command` to resume the session.

There is no retry when the usage limit command finished the job. After three retries in a row
that stop at the limit again, the task is marked failed instead. A completed run, or a manual
`redline task retry`, resets the count. A manual retry of a held job also starts it now instead
of waiting for the reset.

For report-only tasks such as release readiness, choose a profile whose prepare/finalize hooks
and custom harness commands do not change repository files, commit, push, open pull requests,
publish, deploy, or release. A task prompt does not disable these commands; prepare hooks run
before the agent receives the prompt.

## Minimal profiles

For small, self-contained tasks, the minimal example profiles suppress personal hooks, plugin
activation, MCP servers, rules, and session persistence. This reduces startup variability and
prevents a background one-word task from inheriting a large interactive environment:

```bash
redline profile add --file examples/codex-minimal-devx-profile.yaml
redline profile add --file examples/claude-minimal-devx-profile.yaml
```

Minimal profiles deliberately omit repository instructions and disable tools. Do not use them for
code changes, tests, reviews, or any task that needs `AGENTS.md`, `CLAUDE.md`, skills, MCP servers,
or filesystem tools. Use a normal profile with only the customizations that task requires.

## Hermes

Redline can dispatch work to a remote [Hermes](https://github.com/nousresearch/hermes-agent)
Gateway. See [Hermes integration](hermes.md).

## Related

- [Scheduling](scheduling.md)
- [Runs, logs, and notifications](runs-and-notifications.md)
- [CLI reference](cli.md)
