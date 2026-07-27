# MCP and agent interface

Redline exposes a local stdio MCP server for agents that need usage guidance or queue access. The
MCP process is a thin authenticated client of the existing loopback HTTP service:

```text
agent host -> redline mcp -> http://127.0.0.1:7436 -> service -> SQLite
```

Only `redline serve` owns SQLite, usage collection, scheduling, and execution. Closing an MCP
session cannot stop the service or an admitted run.

## Setup

The Redline service must already be running. For a source checkout, build a stable command path:

```bash
go build -o "$PWD/dist/redline" ./cmd/redline
```

Pass the same `--config` path used by the service when it is not in the standard macOS location.
The MCP client reads the protected sibling `api-token` automatically. `REDLINE_API_TOKEN` is
available for supervised environments that inject the token without a readable config path.

Register that command with Codex:

```bash
codex mcp add redline -- \
  /absolute/path/to/redline/dist/redline \
  --api http://127.0.0.1:7436 mcp
```

Register it for the current user in Claude Code:

```bash
claude mcp add --scope user redline -- \
  /absolute/path/to/redline/dist/redline \
  --api http://127.0.0.1:7436 mcp
```

After a release containing the MCP server is installed, the bundled executable can be used instead:

```text
/Applications/Redline.app/Contents/Resources/bin/redline
```

The current Pi CLI does not provide a built-in MCP registration command. Pi installations using an
MCP bridge extension can register the same stdio process with the bridge's generic server shape:

```json
{
  "redline": {
    "command": "/absolute/path/to/redline",
    "args": ["--api", "http://127.0.0.1:7436", "mcp"]
  }
}
```

Restart an already-open agent host after registering the server.

## Tools

Read-only tools:

| Tool | Purpose |
|---|---|
| `redline_overview` | Compact health, provider, queue, scheduler, and recent-run state |
| `redline_provider_status` | Latest usage windows and model allowances |
| `redline_provider_capacity` | Learned token-capacity evidence and confidence |
| `redline_tasks_list` / `redline_task_get` | Bounded queue and task inspection, including any selected Hermes job |
| `redline_profiles_list` / `redline_profile_get` | Available harness/model/workspace profiles |
| `redline_runtime_connections_list` / `redline_runtime_connection_get` | Remote runtime endpoints and credential references |
| `redline_agent_contexts_list` / `redline_agent_context_get` | Runtime profiles, projects, and working directories |
| `redline_runs_list` / `redline_run_get` | Bounded run history and one run |
| `redline_run_events` | Bounded lifecycle audit trail |
| `redline_run_logs` | At most 32 KiB from one supported log stream |

State-changing tools:

| Tool | Effect |
|---|---|
| `redline_task_create` | Queue a one-off or recurring task; `runtime_job_id` selects an existing Hermes job |
| `redline_task_update` | Change task instructions, eligibility, or the selected Hermes job |
| `redline_task_control` | Enable, disable, or retry a task |
| `redline_profile_create` | Create a harness/model/workspace execution profile |
| `redline_profile_update` | Change profile routing, commands, or lifecycle hooks |
| `redline_profile_delete` | Delete an unreferenced execution profile |
| `redline_runtime_connection_create` / `redline_runtime_connection_update` | Configure a Hermes endpoint using a credential reference |
| `redline_runtime_connection_delete` | Delete a runtime connection with no agent contexts |
| `redline_runtime_connection_discover` | Discover remote profiles, projects, and providers; optionally filter and page models |
| `redline_agent_context_create` / `redline_agent_context_update` | Configure a selected remote execution context |
| `redline_agent_context_delete` | Delete a context with no execution profiles |
| `redline_provider_control` | Pause or resume provider dispatch |
| `redline_provider_concurrency` | Set a provider parallel-run override or restore its configured default |
| `redline_scheduler_evaluate` | Record a fresh decision without launching work |
| `redline_scheduler_dispatch` | Evaluate and potentially launch one eligible task |

`redline_scheduler_dispatch` is intentionally separate and annotated as potentially destructive:
once a task is admitted, its harness is trusted to run to completion. Tool annotations are hints,
not an authorization boundary; use the approval controls of the MCP host.

Profile updates and deletion are also annotated as potentially destructive. Deletion is rejected
while any task references the profile. Profile commands and lifecycle hooks are trusted local code
that can execute when a task is later admitted, so agents should change them only at the user's
request.

Runtime credentials are references, not inline secrets. `credential_source` may be
`hermes_desktop`, `environment`, or `file`; `credential_ref` is the environment-variable name or
protected JSON file path. Never pass a password or session token directly in an MCP tool argument.

Runtime discovery is compact by default: it returns profiles, projects, providers, and each
provider's `model_count` without returning model identifiers or capability maps. Set
`include_models` and optionally `profile` or `provider` to request a model page. `model_limit`
defaults to 50 and is capped at 200; use `model_offset` for subsequent pages. The response marks
omitted model data with `truncated` and `models_truncated`.

For an existing Hermes scheduled job, discover the connection's jobs through the dashboard or
`GET /v1/runtime-connections/{connection}/jobs`, then pass that job ID as `runtime_job_id` when
creating or updating the Redline task. Keep the Hermes-native job paused so only Redline owns
admission. When Redline releases it, the run remains active until the resulting remote session
finishes; its final output, provider/model routing, and reported token classes are available through
the normal run, event, and log tools.

List results default to 20 items and cap at 100. Task prompts are omitted from lists and capped at
8 KiB on detailed responses. Event and log tools also enforce server-side response bounds.

## Suggested agent instruction

Add a project instruction like:

```markdown
Before beginning substantial optional work, call `redline_overview` and the relevant
`redline_provider_status` tool. When availability is constrained or Redline is waiting, defer
nonessential work. When Redline reports surplus capacity, useful deferred work is encouraged.

Use Redline queue and execution-profile mutation tools only when the user has asked to schedule or
manage background work. Do not call `redline_scheduler_dispatch` merely to inspect eligibility;
use `redline_scheduler_evaluate` instead.
```

This is guidance for interactive agents, not adaptive task depth. Redline still decides only
whether queued work may start; task instructions define what the admitted agent does.

## Security

The HTTP service accepts loopback hosts only and requires the random `api-token` stored beside the
active configuration. The stdio MCP process reads that protected credential and authenticates each
request; it does not expose a network listener of its own. Use an absolute trusted executable path
and do not forward the loopback API or stdio server through a remote transport without adding a
separate authorization boundary.
