# Getting started with Redline

Redline watches the remaining allowance on your Codex and Claude subscriptions and releases
deferred jobs when your usage falls behind the selected policy. Automatic dispatch starts off, so
installing Redline cannot spend subscription capacity by itself.

## 1. Install the app

Redline requires macOS 13 or later and supports Apple Silicon and Intel Macs.

1. Download the latest `Redline-…-universal.dmg` and matching `.sha256` from
   [GitHub Releases](https://github.com/croutoncreations/redline/releases/latest).
2. Optionally verify the download:

   ```bash
   cd ~/Downloads
   shasum -a 256 -c Redline-*.dmg.sha256
   ```

3. Open the DMG, drag **Redline** to **Applications**, and launch it.
4. Choose whether Redline should launch when you log in. You can change this later under
   **App Setup…** in the menu-bar app.

Release builds are signed by Crouton Creations and notarized by Apple. Do not bypass Gatekeeper or
remove quarantine attributes to install Redline.

Prefer help from an agent? Paste the bounded prompt in [Agent-assisted install](agent-install.md)
into a trusted local coding agent.

## 2. Confirm usage monitoring

Open the Redline menu-bar item. Codex and Claude should show a weekly percentage and reset time.
When a five-hour limit exists, Redline shows that too.

- Redline reuses a healthy local OpenUsage API when one is already running.
- Otherwise it uses its built-in read-only collectors.
- A provider card explains which source is active and how close that account is to releasing work.
- If credentials have expired, Redline pauses that provider and offers the appropriate login
  command instead of failing every queued job.

Usage monitoring does not enable automatic dispatch.

## 3. Create an execution profile

Open **Redline**, choose **Profiles**, then create a profile:

- **Provider account** selects the allowance to protect.
- **Harness** selects Codex CLI, Claude Code, Pi, Hermes, or a custom command.
- **Model** is discovered from the selected harness when possible.
- **Workspace** should normally be an isolated DevX session or Git worktree for code-changing jobs.
- **Repository** identifies the project the agent should work in.

Redline does not install or authenticate harnesses. Confirm the selected CLI already works
interactively before asking it to run unattended.

## 4. Add a small first job

Choose **+ New job**. Start from an editable prompt preset or write your own. A good first job is
bounded, reviewable, and safe to repeat, such as closing one high-risk test gap.

The **Run when** setting controls how much spare capacity must accumulate:

- **Behind pace** runs earliest.
- **Well behind pace** waits for a larger weekly surplus.
- **Capacity likely to expire** is the most conservative tier.

Priority only orders jobs that are already eligible. For recurring jobs, set a minimum interval and
optionally require the repository to change before another run.

## 5. Enable dispatch deliberately

Review the queue and profiles, then enable scheduling for the providers you want Redline to manage.
Start with the **Standard** policy. The provider card explains the latest `WAIT` or `RUN` decision
and estimates when the next tier will become eligible if usage does not change.

Redline admits one job at a time by default. Once admitted, a task runs to completion; Redline does
not kill it at an arbitrary runtime limit.

Enable macOS alerts from **Notifications…** to see starts, completions, failures, and links to the
persisted Activity result. You can pause a provider at any time without interrupting a job that is
already running.

## Where data lives

The standard installation stores its configuration, API token, SQLite database, and run artifacts
under:

```text
~/Library/Application Support/Redline/
```

The HTTP API listens only on `127.0.0.1` and requires a local credential. Redline does not proxy it
to the network.

Next: [Troubleshooting](troubleshooting.md) · [MCP and agent access](mcp.md)
