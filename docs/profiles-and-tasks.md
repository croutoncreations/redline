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
```

See the [`examples/`](../examples) directory for the full set of starter profiles and tasks.

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

## PR maintenance jobs

A task's `kind` says what it does; `type` still says only whether it recurs. `kind` defaults to
`standard`. Set `kind: pr_maintenance` to create a recurring job that keeps the pull requests
Redline jobs opened in that project mergeable:

```yaml
name: Maintain Redline pull requests
kind: pr_maintenance
type: recurring          # required for pr_maintenance
min_interval: 12h        # required and positive
execution_profile_id: claude-worktree
priority: 65
prompt: Run `make lint` before pushing.   # optional project notes
```

- The procedure is built into Redline and versioned with it, so a fix reaches every project at
  once. Any `prompt` or `prompt_file` is appended as "Additional project instructions"; it never
  replaces the built-in safety rules.
- The profile must use the `git-worktree` or `devx` workspace provider and a local harness (not
  `hermes`). A profile used by a maintenance task cannot be changed to an incompatible one.
- Each run works on at most one pull request and never merges. It considers only pull requests
  that `redline run attribute` proves came from a finished standard run in the same repository
  (see [Runs](runs-and-notifications.md)). It leases a branch with `redline lease acquire` before
  changing anything, and marks a draft ready for review once conflicts are resolved, local tests
  pass, and non-review CI checks are not failing. It does not wait for review bots, which only run
  after a pull request is ready.
- Only one maintenance run per repository is active at a time. The scheduler skips a second one
  with the reason "pr_maintenance run … is already active for this repository".
- The `pr-maintenance` template in the dashboard sets this up with a 12-hour interval.

Attribution trusts what Redline recorded when the run started: the run's workspace directory must
lie inside the repository's `.redline/worktrees/` or `.worktrees/` (DevX) directory, and its
recorded branch must equal the pull request's head branch. Runs do not snapshot their profile, so
the profile's repository is also checked as it is now. Reassigning a profile to another repository
cannot make old runs attributable there, because their recorded workspaces still point at the
original repository.

## Dispatch tier and priority

```yaml
dispatch_tier: behind # behind, well_behind, or expiring
priority: 60
```

The tier controls *when* a task becomes eligible; priority only orders tasks whose tiers are
already unlocked. See [Scheduling](scheduling.md#dispatch-tiers-and-priority) for how tiers are
derived from allowance pace.

## Harness and model discovery

The dashboard discovers installed Codex CLI, Claude Code, Pi, and Hermes harnesses, shows each CLI
version, and builds model choices from local, refreshable catalogs rather than hardcoded presets.
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

- **Git worktree** - an isolated checkout per run. The recommended default for code-changing jobs.
- **DevX** - an isolated [DevX](https://github.com/jfox85/devx) session. Creation
  arguments are configurable, e.g. `workspace_args: [--target, host]`.
- **Existing directory** - runs directly in a checkout. Only use this when you explicitly accept
  direct edits to that directory.
- **Generic command** - a custom command that prepares the workspace.

Git worktree and DevX workspaces start from the current remote base. When the repository has an
`origin` remote, Redline fetches the profile's base branch and creates the run from
`origin/<base>` (an `origin/` or `refs/...` prefix in `base_branch` is accepted). An empty
`base_branch` means origin's default branch. The fetch only updates remote-tracking refs: the main
checkout's branch and working tree are never touched. If `origin` exists but cannot be fetched,
or `origin/<base>` cannot be verified, preparation fails rather than running from a stale local
branch. Only a repository with no `origin` at all uses the local base. Each run records
`base_ref`, `base_sha`, and `base_sync` (`origin` or `no-origin`) in its workspace metadata.
DevX creates its session from the main checkout's HEAD, so Redline fast-forwards the new session
onto `origin/<base>`. It refuses if the session has uncommitted changes or commits of its own.
The revision used by `require_repo_change` is read the same way, from the fetched
`origin/<base>`. If the fetch fails, the task is skipped with a reason instead of being treated
as unchanged.

Optional prepare/finalize hooks run before and after the harness. Cleanup defaults to `never`;
`on_success` and `always` are also supported. Execution-profile fields, including lifecycle
commands, are retained with each run for reproducibility, so pass secrets via the environment
rather than embedding them in profile command strings.

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
