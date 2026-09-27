# Security policy

## Reporting a vulnerability

Please do not open a public issue, discussion, or pull request for a suspected
vulnerability.

Report it privately through GitHub:
[**Report a vulnerability**](https://github.com/croutoncreations/redline/security/advisories/new).
This lets us discuss the report and coordinate a fix privately before details
are published, and credit you in the resulting advisory.

If you cannot use GitHub, email **security@croutoncreations.com** instead.

Please include:

- the Redline version (`redline version`; the app bundles the CLI at
  `/Applications/Redline.app/Contents/Resources/bin/redline`) and your
  operating system;
- how Redline was installed (DMG, Homebrew, `go install`, or source);
- reproduction steps and the impact you observed; and
- any suggested mitigation.

Do not include real API tokens, provider or Hermes credentials, pairing links,
or private repository contents. Redact them from logs and screenshots.

We aim to acknowledge reports within three business days and will keep you
updated until the issue is resolved. Redline is maintained by a small team, so
fixes ship in the next release rather than as backports: only the
[latest release](https://github.com/croutoncreations/redline/releases/latest) is
supported.

## What Redline does and does not isolate

Redline decides **when** a queued job may start and prepares the workspace it
runs in. It does **not** sandbox the admitted agent. A Git worktree or DevX
session gives each run its own checkout, but a locally launched harness runs
with your user account's permissions and environment, and keeps its own tools
and capabilities; see [Agent permissions](docs/native-macos.md#agent-permissions).
Hermes jobs run under the account and environment of the Hermes runtime you
configured.

## In scope

Redline runs coding agents and local commands on your machine, so these areas
are especially security-sensitive:

- **Local service and API token.** The HTTP service accepts loopback hosts and
  the exact Tailscale `.ts.net` hostnames you list in `api.trusted_hosts`, and
  requires the random `api-token` stored beside the configuration. Host-check,
  origin-check, or token bypasses are in scope.
- **MCP server.** `redline mcp` authenticates to the local service and has no
  network listener. Ways to reach the service or its state-changing tools
  without the token are in scope.
- **Remote dashboard and phone pairing.** Remote access is supported only
  through Tailscale Serve on your own tailnet; see
  [Mobile dashboard](docs/mobile.md). The pairing page and redeem endpoint are
  reachable without the token by design, so weaknesses in the short-lived,
  single-use pairing credential are in scope.
- **Scheduling controls.** Automatic dispatch starting work while
  `scheduler.enabled` is `false`, or any new run being admitted for a paused
  provider. (A manual dispatch you request while automatic scheduling is off is
  expected behavior.)
- **Redline's own path and workspace handling.** Defects in how Redline selects,
  prepares, or cleans up a workspace, resolves a task's `prompt_file`, or stores
  run artifacts and logs, such as path traversal outside the intended location.
- **Credential handling.** Leaking Codex or Claude credentials, the API token,
  or Hermes runtime and Gateway credentials (passwords, cookies, and session
  tokens) through logs, the dashboard, the API, or run artifacts, and flaws in
  how Redline reads those credentials or authenticates to Hermes.
- **Releases and updates.** Anything that could make an official distribution
  channel deliver unsigned, mismatched, or tampered code: the signed macOS app
  and its Sparkle update feed, the Homebrew cask and formula, the CLI release
  archives and `checksums.txt`, or the release workflow that publishes them.

## Out of scope

- Commands and lifecycle hooks in an execution profile you configured. These are
  trusted local code by design; see [MCP and agent access](docs/mcp.md#security).
- What an admitted coding agent does with the access it has. Redline does not
  sandbox agents (see above); the harness and your task instructions decide what
  it does.
- Exposing the service beyond loopback yourself, for example with Tailscale
  Funnel or a public reverse proxy, without adding a separate authorization
  boundary. The documented Tailscale Serve setup is supported and in scope.
- Vulnerabilities that exist only in Codex CLI, Claude Code, Pi, Hermes, or
  OpenUsage themselves; please report those to their maintainers. Flaws in
  Redline's integration with them remain in scope.
