# GitHub issue sources

Issue sources turn a labeled GitHub backlog into ordinary one-off Redline jobs. Polling uses no
model calls. Jobs use the same allowance checks, priorities, dispatch tiers, and provider pause
controls as manually created jobs. Adding a source does not enable automatic scheduling.

Open **Issue sources** beside the dashboard's **Profiles** button. Create a source, preview its
matching issues, then use **Sync now** to import eligible work. Sources poll every five minutes
with authentication, or every fifteen minutes in public mode. A failed sync records its error and
backs off for an hour; a manual sync can retry sooner.

## Setup

Use a local `git-worktree` execution profile with:

- An existing clone whose `origin` matches the source's host and `owner/repository`.
- An explicit base branch such as `main` (not `HEAD`). Redline fetches that branch before admission
  and creates the worktree at the fetched commit.
- A local harness. Runtime-owned remote contexts are not supported for issue delivery yet.

The profile's prepare hook still runs. Issue jobs retain their worktrees and use Redline's built-in
delivery instead of the profile's finalize hook and cleanup policy.

A source definition for the CLI:

```yaml
id: project-backlog
host: github.com
repository: owner/repository
auth: gh
# account: your-github-login  # optional explicit gh account
execution_profile_id: local-project
labels: [redline]
exclude_labels: ["redline:hold"]
include_assigned: false
goal: draft_pr
instructions: Keep the fix focused and document verification.
verify_command: go test ./...
priority: 50
dispatch_tier: behind
max_queued: 10
enabled: true
```

```bash
redline issue-source add --file source.yaml
redline issue-source preview project-backlog
redline issue-source sync project-backlog
redline issue-source list
redline issue list
redline issue-source disable project-backlog
redline issue-source enable project-backlog
```

The dashboard can edit filters, goals, instructions, credentials, verification, priority, and queue
limits. Source ID, host, repository, and execution profile are immutable; create another source to
change them. Configuration changes affect newly imported attempts. Existing attempts retain their
prompt, issue snapshot, verification command, and execution profile. Credential changes take effect
for pending work and delivery retries, and pausing a source holds admission and publication.

## Authentication

- `gh`: resolve the selected host/account's credential using `gh auth token`. Run `gh auth login`
  as the same OS user as the service. The service must be able to find `gh` on its own PATH.
- `public`: use unauthenticated REST requests. This can read public issues and produce local work
  or investigations. Publishing a PR needs authenticated access; edit the source's authentication
  and retry delivery if code was already produced.
- `environment`: set `credential_ref` to an environment variable name, such as
  `REDLINE_GITHUB_TOKEN`. Set that variable in the service's environment. The definition stores the
  name, never the token value. HTTPS Git operations receive the credential only in the publisher
  subprocess environment. SSH origins use the user's existing SSH configuration.

GitHub Enterprise hosts use HTTPS and the `/api/v3` REST path. Hosted GitHub App login, fork creation,
GitHub Projects filters, Linear, and Jira are not included in this release.

## Selection and attempt history

Required labels all have to match. Excluded labels and, by default, any assignee make an issue
ineligible. Eligible issues are imported oldest first into a bounded queue. Source previews explain
local history and filter exclusions. Before dispatch, Redline refreshes the issue and checks for an
open PR cross-reference. A closed issue, removed label, changed title/body, or linked open PR
withdraws the queued attempt. A read failure holds it and allows unrelated jobs to proceed.

GitHub issue IDs plus the host deduplicate work across overlapping sources in one local database.
Each issue has a durable record and separate attempt records linked to ordinary tasks and runs.
Polling never retries an issue automatically, including when its requirements change or a previous
attempt failed. Disabling a source retains its history. GitHub labels and comments are not modified.
Separate Redline installations do not share claims.

```bash
redline issue retry ITEM_ID              # a fresh coding attempt after inspection
redline issue retry-delivery TASK_ID     # publish retained work, without another agent run
```

A running, queued, or publication-pending attempt cannot be retried as new coding work. A service
restart marks interrupted coding work for inspection. An explicit retry checks its preserved
branch for a PR before allowing another attempt. Interrupted publication is reconciled against
the exact recorded branch, base, and commit. If no PR is found, retry delivery explicitly.

## Goals and delivery

- `draft_pr`: the agent commits a focused change. Redline runs the configured verification command,
  checks the worktree and origin, records publication intent, pushes its run branch, and opens a
  draft PR. It verifies the returned PR's host, repository, branch, base, and head commit.
- `local_changes`: verify and retain committed changes for local inspection, without publishing.
- `investigation`: return findings without requiring a code change or PR.

Every goal can also return `needs_clarification`, `already_addressed`, or `no_change_needed`.
Issue jobs receive a versioned prompt containing a timestamped issue snapshot and the user-authored
instructions. Issue bodies are external evidence and cannot configure publication, credentials,
verification commands, or destinations. The first release includes the issue title and body;
comment threads and linked documents are not automatically imported.

The prompt requires a JSON result at the absolute path supplied for the run:

```json
{
  "outcome": "changes_ready",
  "summary": "What changed, verification evidence, and remaining limitations.",
  "warnings": []
}
```

Supported outcomes are `changes_ready`, `investigation_complete`, `needs_clarification`,
`already_addressed`, and `no_change_needed`. A missing/invalid result needs attention. Execution
success alone does not establish delivery. A publication failure preserves the workspace and records
`delivery_failed`; retrying checks for a PR before attempting another create. A configured verification
failure needs inspection rather than automatic publication. Without `verify_command`, Redline checks
Git consistency and records the agent's reported evidence; it does not independently run a test suite.

The service does not merge PRs, close issues, enable auto-merge, or deploy as part of issue delivery.
Git worktrees and prompt instructions are not a security sandbox for the agent; the existing harness
and local OS permissions still determine what that process can access.

## HTTP API

All endpoints use the existing local Redline API authentication:

| Method | Path | Purpose |
|---|---|---|
| GET / POST | `/v1/issue-sources` | List / create sources |
| PATCH | `/v1/issue-sources/{id}` | Replace editable source configuration |
| GET | `/v1/issue-sources/{id}/preview` | Preview matches without importing jobs |
| POST | `/v1/issue-sources/{id}/sync` | Import eligible issues |
| POST | `/v1/issue-sources/{id}/enable` or `/disable` | Control polling and admission |
| GET | `/v1/issues?source={id}` | Issue and attempt history |
| POST | `/v1/issues/{item}/retry` | Explicit new coding attempt |
| POST | `/v1/issue-delivery/{task}/retry` | Retry publication only |

API responses contain issue snapshots and credential references, never resolved tokens. The REST
adapter paginates issue lists and issue timelines and reports an error when its 2,000-entry bound is
reached; narrow source labels for larger backlogs. PR detection covers GitHub cross-references and
Redline's recorded attempt branches; unrelated PRs that never reference the issue may still overlap.
