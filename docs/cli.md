# CLI reference

Every `redline` command except `version`, `serve`, `demo`, `token rotate`, and the
standalone read-only `reset preview` is a client of the local HTTP API.
The default API is `http://127.0.0.1:7436`; override it with `--api` before the subcommand:

```bash
redline --api http://127.0.0.1:8000 status --provider claude-main
```

When running from source, substitute `go run ./cmd/redline` for `redline`.

## Service

```bash
redline version                                           # release version, commit, build date
redline --config redline.yaml serve                       # run the service
redline --config redline.yaml serve --listen 127.0.0.1:17436
redline dashboard                                         # open the authenticated local dashboard
redline --config redline.yaml --api http://127.0.0.1:17436 dashboard
redline mcp                                               # stdio MCP server (see mcp.md)
redline health
redline health --window 24h
```

`redline dashboard` reuses the protected local API credential and opens your default browser.
Use the same `--config` and `--api` values as the running service. It checks that the service is
reachable before opening the browser, and never prints the credential or authenticated URL.
The command requires a local desktop browser and an HTTP loopback API URL. For another device,
use `redline pair` instead. Opening the bare dashboard URL in a browser shows these sign-in
instructions; API requests continue to require authentication.

## Managed accounts

`redline account add <id> --provider claude`, `redline account add-profile <id> --dir <path>`,
and `redline account edit <id>` accept
`--weekly-policy session_only` and `--session-reserve 0.50` for Claude seats without
a reported weekly cap. The reserve protects that fraction of every 5-hour window
(`[0,1)`, default 0.50); it requires `session_only`. Other policy values are rejected;
`--weekly-policy ''` restores the default hold and clears a saved session reserve.
Changes require a service restart. Main-config accounts must be edited in YAML.
See [session-only scheduling](scheduling.md#claude-team-seats-without-a-reported-weekly-cap)
for the hidden-weekly-cap risk and model-budget gates.

## Device sessions (owner only)

```bash
redline --config redline.yaml devices list
redline --config redline.yaml devices revoke DEVICE_ID
```

Lists show session IDs, names, kind, creation/last-use times and optional revocation time, never
credentials or hashes. Revocation takes effect on the device's next API request. Paired devices
retain job/run/provider/firesale control; banked reset spending requires
`api.paired_devices.allow_reset_redemption: true` (default false). They cannot create pairings,
manage devices or test notifications. See the [API permission table](api.md#principals-and-permissions).

`token rotate` is a local owner action, requires `--yes`, opens the configured database to revoke
**all** device sessions, and replaces the master token. Restart Redline to invalidate the old
master token and legacy pairings, then pair devices again. Re-pair legacy phones to replace their
old master-token bearer credential with a properly scoped device token; existing browser cookies
upgrade transparently without interrupting access.

## Usage and decisions

```bash
redline usage refresh --provider codex-main --json
redline status --provider codex-main
redline decision --provider codex-main
redline calibration --provider claude-main
redline token sync --provider claude-main
redline --config redline.yaml token rotate --yes          # replaces the master token; after a restart every device session and legacy pairing is signed out
redline capacity --provider claude-main
redline metrics launch --days 21
```

### Public reset-signal preview

```bash
redline reset preview
redline reset preview --timeout 8s
```

This performs one bounded GET of each documented public tracker and prints a JSON likelihood
report. It does not read Redline account state, poll, notify, select jobs, change scheduler state,
or start anything. Tracker percentages remain separately attributed; Redline does not combine
them into its own probability. See [Public reset-signal preview](reset-signal-preview.md).

### Cached reset signals and firesales

```bash
redline reset signals
redline reset firesale start
redline reset firesale stop
redline reset firesale start --announcement announcement:123
redline reset firesale stop --announcement announcement:123
```

These authenticated service commands print JSON. `signals` reads cached evidence and firesale
state without a live fetch. Start defaults to the current viable announcement; stop defaults to
the active sale's key (and succeeds as a no-op if there is none). Start requires a future announced
deadline and `reset_signals.firesale: notify|auto`. Stop works during tracker outages and does not
require firesale mode to remain enabled. Both use the owner/device firesale API and refuse demo
mode. See [modes and lifecycle](reset-signal-preview.md#announcement-firesales).

### Manual firesales (any provider)

```bash
redline firesale start --provider claude-main --for 15m
redline firesale start --provider codex-main --until-reset
redline firesale stop --provider claude-main
redline firesale status
```

Start requires exactly one of `--for DURATION` (5m–168h) or `--until-reset` (latest cached weekly
reset required). Starting an active sale replaces its deadline. Start and status print JSON;
stop succeeds silently. These owner/device API commands work for any configured account,
independently of announcement monitoring. They override pacing only, never safety gates.
See [manual firesales](scheduling.md#manual-firesales).

To send a synthetic Start-capable notification to a phone (owner only):

```bash
redline notification test --event reset.announced --provider claude-main
```

The sample reads "Test: Start a claude-main firesale?" and includes `manual_account` and
`manual_for: 15m` and a promised window ending in two hours. No sale starts until the action is used. Without `--provider`, the announcement sample
remains a completed reset with no manual Start action.

### Redeem a banked reset

```bash
redline reset redeem --provider claude-main --dry-run
redline reset redeem --provider codex-main --dry-run --json
redline reset redeem --provider claude-main
redline reset redeem --provider codex-main --yes --json
```

A dry run sends a nonexistent ID, never a real grant or credit. Run it first, then make the first
real redemption manually. Real redemption prompts on a TTY with the provider, reset ID, expiry,
and windows cleared: `This spends a one-time reset. Continue? [y/N]`. Non-interactive use requires
`--yes`; this explicitly waives the prompt. If the available reset changes after confirmation,
Redline refuses rather than spending another reset: re-run to confirm.

`--json` prints the API result, including `outcome`, `code`, `reason`, `reset_id`, `request_id`,
`verified`, and any `cleared` / `cooldown_until` fields. Exit codes: **0** redeemed or dry_run_ok,
**3** nothing_to_reset, **4** cooldown or unavailable, **5** gone, **1** failed, unknown, or error.
An `unknown` result is shown as "outcome unknown"; check the provider before trying another reset.
`verified: false` does not make an acknowledged redemption fail; inspect usage before relying on
new allowance. See [redemption caveats](scheduling.md#redeeming-banked-resets).

## Profiles and tasks

```bash
redline profile add --file examples/codex-devx-profile.yaml --json
redline profile list
redline task add --file examples/add-tests-task.yaml --json
redline task add --name "Parser tests" --prompt "Add table tests for the parser." \
  --profile claude-worktree --tier behind --priority 50 --json
echo "Add table tests for the parser." | redline task add --name "Parser tests" --prompt - --profile auto --json
redline task list
redline task enable add-tests
redline task disable add-tests
redline task retry add-tests
redline task dispatch add-tests
redline candidates --provider codex-main
```

`task add` without `--file` builds the task from flags. `--prompt -` reads the prompt from stdin
(up to 256 KiB). `--profile auto` picks the Claude Code profile whose `repository` is the git top
level of the current directory (`--cwd` overrides the directory and `--harness` the harness type).
A non-matching `--default-profile` requires `--allow-default-profile`. Other flags: `--prompt-file`, `--type`
(`one_off` default), `--tier` (`behind` default; also `well_behind`, `expiring`, `firesale`),
`--priority` (50 default), `--min-interval`, `--firesale-interval` (optional positive provider-firesale
cooldown), and `--disabled` to save a draft. YAML uses `firesale_min_interval` for that cooldown.
Task updates are available through the API, MCP, or dashboard, not a CLI update command.

### Queue work for later

```bash
redline later "finish the parser refactor and run go test ./..."
echo "write the migration and its test" | redline later -
redline later --tier expiring --json "sweep the docs for stale flags"
# For hook-supplied text, pass it through stdin: redline later --prompt - --json
```

`later` accepts `--tier firesale` and the optional `--firesale-interval` flag, like `task add`.
It queues free text as a one-off task using `--profile auto` resolution. It never dispatches
and never bypasses limits: the task starts in a fresh harness session only when the provider is
behind pace (or near expiry for `--tier expiring`) and above its reserve. A task created with
`--tier firesale` waits for the policy's firesale trigger, not the standard surplus threshold.
If no profile matches, it fails closed unless `--default-profile ID --allow-default-profile` is
passed explicitly; the text output warns that the fallback may target another repository. Hooks should pass untrusted
text via `--prompt -` and stdin, never as positional flags. For positional text starting with a
hyphen, use `redline later -- "-something"`. With `--json` it prints the created task, how the
profile was chosen, and the matched repository. `task add --json`, in both YAML and flag mode,
prints the created task object directly.

## Scheduler

```bash
redline scheduler evaluate --provider codex-main --json
redline scheduler evaluate --provider codex-main --revision "$(git rev-parse HEAD)"
redline scheduler execute --provider codex-main --json
redline scheduler status
redline scheduler history --provider codex-main
redline scheduler attempts --provider codex-main
redline pause --provider codex-main
redline resume --provider codex-main
```

## Runs

```bash
redline run list
redline run show <run-id>
redline run events <run-id>
redline run events <run-id> --limit 500   # --limit defaults to 100
redline run logs <run-id>
redline run logs <run-id> --stream stderr --tail-bytes 8192
redline run logs <run-id> --stream prepare_stderr
redline run logs <run-id> --stream finalize_stdout
redline run logs <run-id> --json
redline run watch --jsonl
redline run watch --jsonl --interval 30s --count 1
```

`run watch --jsonl` prints one JSON line per run that finishes after the watch starts:
`{"task_id", "task", "run_id", "status", "summary", "pr_url", "completed_at"}`. It polls the
loopback API every 10 seconds by default and pages through completions with a durable sequence
cursor, including runs that started before the watch began but finished afterward. Cursor state is
in memory, so restarting a watcher begins at the latest completion, not the previous position.

## Machine-readable output

`task list`, `profile list`, `run list`, `run show`, `run events`, `health`, `candidates`,
`calibration`, `capacity`, `metrics launch`, `token sync`, `notification list`, `reset preview`,
`reset signals`, `reset firesale start|stop`, and the
`scheduler status|history|attempts` commands print JSON on success. `status`, `decision`,
`usage refresh`, `task add`, `later`, `reset redeem`, `notification test`, `scheduler evaluate|execute`, and `run logs` print JSON with
`--json`. Usage, validation, and API errors print a plain-text error to stderr and exit non-zero.
Some results are reported as JSON instead: `decision --json` exits 2 when the decision is unknown,
`reset redeem --json` exits 3, 4, 5, or 1 for outcomes other than `redeemed` and `dry_run_ok`, and
`reset preview` exits 0 and reports source fetch errors under `uncertainty`.

## Notifications

```bash
redline notification list
redline notification test [--event TYPE] [--provider ID] [--push] [--json]
redline notification test --event all
```

The test command sends synthetic data only; it never contacts providers or redeems a reset.
Defaults: `reset.expiring`, first Codex account or otherwise first configured account (sorted by
ID). Supported types: `run.started`, `run.completed`, `run.failed`, `scheduler.error`,
`reset.expiring`, `reset.redeemed`, `reset.redeem_failed`, `firesale.started`, `firesale.ended`.
`all` sends one of each about a second apart. Output includes the title/message and delivery
status; `--json` returns the delivery record (an array for `all`). Failed deliveries exit 1.
Explicit tests bypass `notifications.events`, but require `notifications.enabled: true` and the
local bearer API token. An empty `command` records deliveries for native Mac alerts without an
external hook. See [notifications](runs-and-notifications.md#notifications).

Add `--push` to await content-free test wakes through the configured transport. It still
records one delivery and runs the normal hook, respects the global enabled flag, and reports
`Push: N devices woken` or `push not configured`. JSON adds `push: {configured, woken}`.
The desktop has no built-in push provider. See [push notifications](push-notifications.md).

## Mobile pairing

```bash
redline --config redline.yaml pair --qr
redline --config redline.yaml pair --qr --host macbook-pro.example.ts.net --port 8443
```

`--qr` is required. Without `--host`, the Tailscale MagicDNS name is discovered automatically; either
way the host must be listed in `api.trusted_hosts`. `--port` defaults to 443.

See [Mobile dashboard setup](mobile.md).

## Related

- [HTTP API](api.md)
- [Scheduling](scheduling.md)
- [Execution profiles and tasks](profiles-and-tasks.md)
