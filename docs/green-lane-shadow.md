# Green lane: small, reviewed PRs

The green lane is repository automation in GitHub Actions. It does not change
Redline's application, daemon, scheduler, dashboard or agent execution.

**Current mode: shadow only.** It reports `would_merge` or `would_not_merge`.
No workflow merges or approves PRs. Do not require `green-lane-shadow` in branch
protection: it is an informational report, not merge authorization.

## Eligibility

A PR qualifies only when all these checks pass:

1. **Allowed author.** The GitHub author login is explicitly allowed. Initially
   this is `jfox85`, including agents opening PRs using that account. Add separate
   agent logins explicitly to `AllowedAuthors` in trusted policy; bot status or
   an author's claim does not grant trust. An empty list admits nobody.
2. **Ready PR.** Open, non-draft, mergeable, targeting `main`.
3. **At least one completed review on the latest commit.** A submitted GitHub
   review with state `APPROVED`, or a nonempty human `COMMENTED` review, counts
   from a reviewer other than the PR author. Empty review publications (including
   inline replies) do not count. Bot `COMMENTED` publications must use the
   authenticated completion protocol below; reply prose is not completion.
   Automated reviews count; **manual approval is not required**.
   A review with zero findings counts. CodeRabbit and Codex can instead publish a
   completed review in their canonical summary comment: the known GitHub App and
   immutable bot user IDs must match, and GitHub must confirm that the comment
   was unedited or last edited by that same bot. The completion record must name
   the full current head SHA; running, skipped, rate-limited, ambiguous or unknown
   records do not count. Codex must explicitly report no unresolved findings.
   When its security-review state is present, that review must also be completed on the exact current head
   with no unresolved findings, even if it has no inline threads. An absent or
   null security-review state means that review was not requested.
   Only the latest summary per provider is considered. Ordinary issue comments,
   pending/dismissed reviews, reviews on older commits and a successful workflow
   without review evidence do not count. Pushing requires a new review.
4. **Feedback addressed.** Every inline review thread is resolved, including
   outdated threads. No effective `CHANGES_REQUESTED` review remains, including
   on older commits. A later comment does not clear requested changes; the
   reviewer must approve or the request must be dismissed.
5. **Required CI green.** All seven Redline CI jobs must complete successfully:
   `actionlint`, `go`, `dashboard`, `linux-smoke`, `windows-smoke`, `macos`, and
   `macos-intel`. The collector verifies the actual GitHub Actions App, workflow,
   run and job identities on the current head, rather than trusting check names.
6. **Small change.** Code: at most 150 regular changed lines and six files.
   Docs/dependencies: at most 100 regular changed lines and five files. Existing
   lockfile/generated caps and same-directory manifest requirements also apply.
7. **No critical paths.** Authentication secrets, credentials, CI workflows,
   release/signing controls, database schema, CODEOWNERS and green-lane policy
   tooling are excluded by `DefaultDeniedPaths`. Both paths of renames count.
   The complete policy is in `internal/greenlane/config.go`.
8. **Fresh evidence.** Incomplete API pagination, API failures or a changing
   snapshot block eligibility. Metadata, reviews, threads and CI are checked
   again immediately before publishing, and reports expire after five minutes
   for publication purposes. Existing posted reports are historical snapshots.

Docs/dependency changes retain their restrictive file/patch validation.
Lockfile changes remain manual until trusted resolver regeneration exists;
plausible syntax or URLs do not suffice.

There is **no** requirement for signed author receipts, model-provider
independence, an issuer service, a new GitHub App, a human approval, a new
fails-before/passes-after repro test or a `Root-Cause` trailer. Required CI is
the test gate. The older repro/provenance helpers remain available as legacy
tooling but do not participate in this policy. Duplicate scanning is no longer
an eligibility prerequisite.

## Workflows

`green-lane-shadow.yml` refreshes reports on PR changes, review submissions or
dismissals, and CI completion. Close events post a neutral invalidation without
checking out any code. A manual rerun accepts a `pr_number` input from trusted
`main`:

```bash
gh workflow run green-lane-shadow.yml --repo croutoncreations/redline --ref main -f pr_number=186
```

The workflow builds and runs trusted code only. It never checks out or executes
PR code and has read scopes plus `checks:write`, with no merge permission.
The `pull_request_target` handler is safe only while that boundary is preserved.
Manual dispatch on another branch is refused. Review-triggered runs use trusted
base code; fork token restrictions may require the hourly sweep to publish.

`green-lane-reconcile.yml` refreshes all open PRs hourly at minute 23 and by
manual dispatch. This catches resolved/reopened threads, which do not directly
trigger Actions. Bot summary edits also enter through this sweep (or manual
dispatch), without introducing an issue-comment write workflow. A planning job paginates the open list and splits it into
five-PR shards; up to four shards run concurrently. No PR is silently omitted.
Repositories exceeding 256 shards fail explicitly. Each shard fetches current
evidence independently; there is no all-repository file-list/duplicate scan.
PRs opened after planning enter the next sweep or their normal event handler;
PRs closed after planning are omitted from the sweep and invalidated on close.

```bash
gh workflow run green-lane-reconcile.yml --repo croutoncreations/redline --ref main
```

The evaluator reads review evidence twice. The publisher then independently
re-fetches summaries, App/author/editor identities, submitted reviews and threads
and compares their canonical digest immediately before posting. An edited,
deleted, stale or restarted summary invalidates the earlier evaluation, even
when the PR timestamp does not change. Protocol changes fail closed until the
trusted parser is updated. No new App installation or credentials are required.

Failed collection/publication clears the current head's report to neutral where
GitHub check permissions remain available. Artifacts retain the evaluation JSON
for 30 days. `advisory_candidate` now means all policy rules passed, and remains
informational.

## Rollout

1. Merge the simplified policy and reconciliation fix. Infrastructure PRs touch
   excluded paths and need the normal merge process.
2. Rerun both workflows from `main`. Compare real eligible and rejected PRs,
   including a clean automated summary and a submitted `COMMENTED` review with all threads resolved. Exercise
   failed CI, unresolved feedback, stale reviews, unknown authors and large or
   critical-path changes. Observe the results before enabling merges.
3. Add a separate opt-in merge step that repeats the current checks, verifies
   the base is still current and merges the exact evaluated head SHA. Respect
   branch protections. This can use GitHub Actions permissions without deploying
   an issuer service or a new App. The live merge step is not implemented yet.
4. Port the policy to other repos after the Redline pilot is validated, changing
   the author list, required CI identity and critical paths for each repo.

This document supersedes the signed-provenance and cross-provider deployment
requirements in the older green-lane plan. No signing keys or issuer Apps need
to be provisioned for the simplified policy.

## Limited Redline live pilot

`Green Lane Live Pilot` is a separate hourly worker (`:47`) with a manual
workflow-dispatch option. It runs trusted `main` code only, never checks out PR
code, and independently runs the existing evaluator. Shadow status, artifacts,
PR comments, and Claude's claims are not merge authorization. Claude's hourly
maintenance task keeps its existing no-merge role. No manual approval is needed;
the existing current-head independent review requirement still applies.

Live merging starts only when repository Actions variable
`GREEN_LANE_LIVE_ENABLED` is exactly `true`. Missing/false means no merges.
The worker is serialized using `green-lane-live-merge` concurrency, never cancels
an active merge worker, and makes **at most one squash-merge API call per run**.
There is no retry after a timeout or rejected merge, since the result could be
ambiguous. Each merge request carries the exact evaluated PR head SHA.

Additional live safeguards:

- Require strict, admin-enforced `main` protection with all eight required
  GitHub Actions CI checks, resolved conversations, and no force-push/deletion.
  Protection is rechecked before the merge. This is not an approval rule.
- Pin the actual `refs/heads/main` tip to the workflow's trusted code SHA; abort
  if it changed since the run started or during evaluation. The PR-recorded
  base SHA is preserved as evidence, but is **not** used as the current main tip.
- Require the PR head to contain that actual main tip (zero commits behind).
  Otherwise defer for a normal branch refresh, CI, and automated re-review.
  After one merge, other PRs may need another refresh. The worker never refreshes
  branches or resolves conflicts itself.
- Revalidate review and CI snapshot hashes, freshness, PR state/head/base and
  metadata immediately before merging. Native strict branch protection also
  enforces up-to-date checks if another actor advances main in the final API
  interval; GitHub's merge API only provides a head-SHA precondition, not a
  base-SHA precondition. Workflow serialization covers this worker, not humans.
- Optional `green-lane-hold`, `maintenance:skip`, or `do-not-touch` PR label defers that PR. It is an opt-out, never an
  approval or opt-in requirement.

### Kill switch

**Disable `Green Lane Live Pilot` in GitHub Actions** (or run
`gh workflow disable green-lane-live.yml -R croutoncreations/redline`). The worker
re-reads that workflow's live enabled state immediately before its merge call,
so disabling also stops a running evaluation before that call. Cancel an active
run as well for an immediate operational stop. A merge request already accepted
by GitHub cannot be recalled. Set `GREEN_LANE_LIVE_ENABLED=false` to prevent new
runs from entering the pilot; that variable is captured at run start and is not
an in-flight kill switch. Re-enabling requires both an active workflow and the
variable set to `true`.

The job writes `live-result.json` (including exact main tip, evaluations, merge
attempt and confirmed merge SHA) to its run artifact. Routine deferrals only
appear in logs/artifacts; the worker posts no routine PR comments or push
notifications. GitHub's own workflow failure/merge notifications still follow
each user's GitHub settings.

After a confirmed merge, the worker explicitly dispatches `ci.yml` from `main`.
GitHub suppresses push-triggered workflows for `GITHUB_TOKEN` merges; explicit
workflow dispatch is supported. The worker has `actions:write` only for this
post-merge dispatch. If dispatch fails, its artifact still records the confirmed
merge, and the worker fails without retrying any merge. The dispatcher runs CI
on current main, which may have advanced further if another actor merged.

For live merges, the evaluator additionally requires an authenticated current-head
Codex/CodeRabbit completion summary; generic human COMMENTED/APPROVED reviews do
not substitute for this. The worker also enumerates all repository collaborators
at start and immediately before merging. Only `jfox85` may have push, maintain or
admin permission during this single-owner pilot. Additional read-only users are
fine; additional writers or unavailable permission evidence stop the pilot. This
binds same-repository branch control to the allowed principal rather than merely
trusting the original PR creator. Expanding the writer set requires a separately
reviewed change to the pilot. No new credential or branch administration service
is introduced.

The merged Homebrew CI check is also required for live/shadow eligibility and main protection. Release CI selection accepts only `push` or `workflow_dispatch` runs of the CI workflow on main for the exact tagged commit; PR-event runs remain excluded.
