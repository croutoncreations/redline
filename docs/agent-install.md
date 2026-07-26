# Install Redline with an agent

Redline can be installed conventionally from its signed macOS release, or an interactive agent can
walk through the same steps and help configure the first useful jobs. Agent-assisted setup is not a
separate installer and should never require sharing an Apple, Anthropic, or OpenAI password.

Paste this into a trusted local coding agent:

```text
Help me install and configure Redline from the official Crouton Creations release:
https://github.com/croutoncreations/redline/releases/latest

Before changing anything, confirm this Mac meets the published requirements and show me the release
version and signing identity you intend to install. Download the notarized DMG and its SHA-256 file,
verify the checksum, copy Redline.app into /Applications, and launch it. Do not bypass Gatekeeper,
disable quarantine, install an unsigned build, or replace an existing configuration without asking.

In Redline, leave automatic dispatch disabled while we configure it. Check which of Codex CLI,
Claude Code, Pi, DevX, and Hermes are already installed; do not install or authenticate another
tool without asking. Reuse OpenUsage if its loopback API is healthy, otherwise let Redline use its
native collectors.

Walk me through creating one isolated execution profile for a repository I choose. Then suggest two
small, reviewable starter jobs based on that repository and my available harnesses. Show me each
editable prompt, provider, model, dispatch tier, recurrence, workspace behavior, and any command it
could execute. Ask before saving jobs and again before enabling automatic dispatch. Finish by
showing current allowance status, why the scheduler is waiting or running, where Redline stores its
configuration/database/logs, and how to pause all providers.
```

The agent should treat a starter prompt as editable input, not as a policy that Redline will update
later. A created job is an independent copy.

## What the agent must not do

- Do not request, print, or store account passwords or app-specific passwords.
- Do not weaken macOS security checks to make installation succeed.
- Do not expose the loopback service beyond localhost.
- Do not enable automatic dispatch until the user has reviewed the profiles and queued jobs.
- Do not select an existing-directory workspace for code-changing tasks unless the user explicitly
  accepts direct edits to that checkout.
- Do not make publishing, deployment, merge, or destructive cleanup part of a starter job without
  explicit approval.
