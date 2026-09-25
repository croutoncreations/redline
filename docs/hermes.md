# Hermes integration

Redline can dispatch deferred work to a remote Hermes Gateway instead of a local harness. The
Gateway owns the remote session and filesystem; Redline owns admission, allowance accounting, and
run history.

## Connect

Choose **Hermes** as the harness in an execution profile and import the current Hermes Desktop
connection. Redline reuses the Desktop-authenticated remote Gateway session, discovers its
profiles, projects, authenticated model catalogs, and existing scheduled jobs, and persists the
selected execution context.

Connection and agent-context records can be created, edited, and removed through the dashboard,
HTTP API, or MCP. Referenced records cannot be deleted.

## Run a task

A task can either start a new isolated Hermes session from its prompt or select an existing Hermes
job from the dashboard. Existing jobs should remain paused in Hermes while Redline owns admission;
this avoids Hermes' native schedule racing Redline's allowance-aware scheduler.

After triggering either form, Redline follows the remote session to a terminal state rather than
treating the initial Gateway response as completion. The external job and session IDs, actual
provider/model, final assistant output, lifecycle events, and reported input/output/cache token
totals are retained with the Redline run. Those observations are attributed to the provider and
model allowance selected by the execution profile, including separate model pools such as Fable.

Because the Gateway owns the remote filesystem, local prepare/finalize commands are disabled for
runtime-owned workspaces.

## Concurrency

Remote Hermes jobs must satisfy the selected runtime connection and agent context limits in
addition to provider and allowance-pool limits. The effective concurrency is the strictest
applicable provider, pool, connection, or context limit.

## Standalone credentials

Hermes connections can also reference standalone credentials without storing their contents in
Redline. Choose an environment-variable name or a protected JSON file (`0600`) containing one of:

```json
{"session_token":"the Hermes dashboard session token"}
```

```json
{"provider":"basic","username":"redline","password":"a strong password"}
```

Session-token connections use Hermes' `X-Hermes-Session-Token` HTTP contract and authenticated
WebSocket query. Basic credentials are exchanged through `/auth/password-login`; Redline retains
only the resulting in-memory cookie jar for that operation. Environment variables must be present
in the service process - launchd installations will usually find a protected credential file
simpler.

## Related

- [Execution profiles and tasks](profiles-and-tasks.md)
- [Scheduling](scheduling.md#automatic-dispatch)
