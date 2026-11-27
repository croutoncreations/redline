# Green lane: small, reviewed PRs

The green lane is repository automation in GitHub Actions. It does not change
Redline's application, daemon, scheduler, dashboard or agent execution.

**Shadow reports remain informational.** They report `would_merge` or
`would_not_merge` and perform no merges or approvals. A separate, opt-in
[limited live pilot](#limited-redline-live-pilot) independently evaluates and
merges eligible PRs. Do not require `green-lane-shadow` in branch protection;
a shadow report is not merge authorization.

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
5. **Required CI green.** All eight Redline CI jobs must complete successfully:
   `actionlint`, `go`, `homebrew`, `dashboard`, `linux-smoke`, `windows-smoke`, `macos`, and
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
   an issuer service or a new App. This is now the
   [limited live pilot](#limited-redline-live-pilot).
4. Port the policy to other repos after the Redline pilot is validated, changing
   the author list, required CI identity and critical paths for each repo.

This document supersedes the signed-provenance and cross-provider deployment
requirements in the older green-lane plan. No signing keys or issuer Apps need
to be provisioned for the simplified policy.

## Limited Redline live pilot

`Green Lane Live Pilot` is a separate hourly worker (`:47`) with a manual
workflow-dispatch option. It also runs when a same-repository PR's `CI` run
completes successfully (`workflow_run`), so an eligible PR doesn't wait up to an
hour. `workflow_run` always executes the workflow file from `main` with
`github.sha` set to the current main tip; the payload is used only as a trigger,
and the worker still scans and re-evaluates every open PR itself. It runs trusted
`main` code only, never checks out PR code, and independently runs the existing
evaluator.

**Transient GitHub errors defer.** A GitHub 5xx, dropped connection or timeout
*before* the merge call ends the run as `deferred_transient`: no merge, one
warning line naming the cause, recorded in `live-result.json`. The next run starts
from scratch. 4xx errors (expired audit token, missing permission) still fail the
run. Once the merge call starts, every error, including a 5xx or a timeout, still
fails loudly and is never retried, because GitHub may already have merged.

Shadow status, artifacts,
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
  The worker rechecks the contents-readable protection summary (protected branch, enforcement for everyone and all App-bound CI checks) before merging. GitHub reserves detailed administration settings for admin credentials, so strictness and conversation resolution are configured/verified by the owner and enforced natively; the worker receives no admin credential. This is not an approval rule.
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

### Merge-queue mode

Set the repository variable `GREEN_LANE_MERGE_QUEUE=true` once `main` requires
GitHub's merge queue. Then:

- **PRs may be behind `main`.** GitHub's queue tests each queued PR combined
  with the latest `main` (and any PRs ahead of it) on a temporary
  `gh-readonly-queue/main/*` branch, and merges only if the eight required
  checks pass there. CI runs on `merge_group` for this. PRs no longer need
  branch updates and re-reviews after other merges.
- **The review still has to be on the PR's current head**, against the PR's
  own base. One extra rule, **Main Drift**, closes the obvious gap: if `main`
  changed any denied path (auth, credentials, workflows, release, the lane
  itself) since the PR's base, the PR needs a branch update and a fresh review.
  If the worker can't list what `main` changed, the rule is inconclusive and
  blocks.
- **The worker enqueues instead of merging**: at most one
  `enqueuePullRequest` call per run, with `expectedHeadOid` pinned to the
  evaluated head, never retried. It also requires `green-lane-queue` to be a
  required check before it enqueues anything. The queue then merges as
  `github-merge-queue[bot]`, and that push to `main` runs CI normally, so there
  is no post-merge dispatch. PRs already in the queue are skipped. If `main`
  has no merge queue, the worker refuses to run.
- **It enqueues and dequeues as the Green Lane Merge App**
  (`green-lane-merge[bot]`, installed on redline only, with Contents,
  Pull requests and Merge queues write). Queue entries created with the
  workflow's `GITHUB_TOKEN` start no `merge_group` CI, so they would sit until
  the queue timed out. The job mints a short-lived installation token
  (`actions/create-github-app-token`, pinned) only in queue mode, from secrets
  `GREEN_LANE_APP_ID` and `GREEN_LANE_APP_KEY`, and uses it only for the two
  queue mutations; everything else stays on `GITHUB_TOKEN`. Without the token
  the worker refuses to run. The sweep only touches entries this App queued.
- **Authorization holds until the merge, not just the enqueue.** The required
  `green-lane-queue` CI job re-decides every queued PR on its merge-queue run.
  It waits for the other eight required jobs (so its verdict is as late as
  possible) and always runs. It is built from the merge group's base (trusted
  `main`), never from the merge group, so a queued PR can't change the code
  that judges it. It fails unless:
  - the queue is squash, only-merge-non-failing (ALLGREEN), one entry at a
    time;
  - the merge-group commit is this PR's current queue entry: one squash commit
    directly on the base;
  - `main`, as the PR lands on it, changed no denied path since the PR's base
    (Main Drift on the exact landing commit), and the merge commit itself
    touches no denied path;
  - for any PR not queued by Jon by hand: no hold label, the full Green Lane
    evaluation of the head passes every rule, and the merge commit changes
    exactly the evaluated PR's files.

  A PR Jon queued himself gets only the path and binding checks: his enqueue
  is the authorization. The remaining window is the few seconds between the
  gate's verdict and the queue's merge.
- **The worker also cleans up early.** Every run (hourly at :47, and after
  each successful same-repository `pull_request` CI run; not after
  merge-queue CI) re-evaluates each PR it queued and removes it from the
  queue (`dequeuePullRequest`) if it no longer qualifies, so a stale entry
  doesn't hold up the queue until its CI fails. This is housekeeping: the
  `green-lane-queue` gate, not the sweep, is what blocks the merge. Just
  before removing an entry the sweep re-reads it and skips it if it is no
  longer the same entry queued by Green Lane, so an entry Jon removed and
  re-queued by hand during the evaluation is left alone. GitHub's removal
  call names the PR, not the entry, so a hand re-queue in the instant between
  that re-read and the removal could still be removed. That is accepted: it
  fails safe (nothing merges), and Jon can re-queue. Removals are listed in
  `live-result.json` under `dequeued`.
- **Kill switch in queue mode:** disabling the workflow stops new enqueues. A PR
  already in the queue stays there. Remove it with **Remove from queue** on the
  PR or on the branch's merge-queue page (the `gh` CLI can't), or with the
  GraphQL `dequeuePullRequest` mutation.

Owner setup, in this order:

1. Create the Green Lane Merge App and set `GREEN_LANE_APP_ID` and
   `GREEN_LANE_APP_KEY` (done Nov 27 2026), then merge the PR that adds
   `merge_group` to CI and queue mode to the worker.
2. In `main`'s branch protection, add `green-lane-queue` (GitHub Actions) to
   the required checks, then turn on **Require merge queue**: squash,
   build concurrency 1, minimum and maximum group size 1, **Only merge
   non-failing pull requests** on, status-check timeout 60 minutes. The
   worker and the gate both refuse to work with any other queue settings. The
   queue's tested branch is always current, so the existing strict
   ("up to date") setting can stay as it is.
3. `gh variable set GREEN_LANE_MERGE_QUEUE -R croutoncreations/redline --body true`.

To leave queue mode, delete the variable first, then remove the queue.

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
not substitute for this. A nonempty current-head review body from anyone other
than the PR author blocks the live evaluator, even if it is a bot `COMMENTED`
publication or an approval. Review-body prose has no resolution bit, so the
pilot does not guess whether a nitpick, outside-diff finding, caveat, or unknown
format is harmless. A positive completion summary from any provider cannot
bypass this block. Address the feedback in a new head and obtain fresh full
current-head automated completion; an empty reply or author response alone
never authorizes merging. Historical review-body prose only clears after that same authenticated provider
publishes a later full completion for the new head. A push or another provider
cannot silently clear it. Dismissing a review also does not resolve its body.
This deliberately also defers benign review prose.

Codex's security review often finishes with `has_any_findings` and
`has_unresolved_findings` both `null` (openai/codex#57326). That form counts as
clean only when all of these hold: the security review is `completed` on the
exact head with both keys present and literally `null`; the summary has exactly
one `codex-security-review:v1` marker, `completed`, for this repository, PR and
head; and the PR has no Codex security-finding comment
(`codex-security-review-finding:v1`) at all. Real findings set both flags `true`
and post such a comment, so they still block.

One narrow exception: `claude[bot]` (user ID 209825114, type Bot) posts a
`COMMENTED` review when it **did not review at all**, for example when the
organization has no Claude usage left or the review was requested by a bot. Two
such notices are listed in `internal/greenlane/review_notices.go` and are ignored
only when the review body matches byte for byte. Any other wording, an extra
character, another author or another review state still blocks. These notices
never count as a completed review. If Anthropic changes the wording, the new text
blocks again until it is added to that list in a reviewed PR.
All review bodies remain bound into the twice-collected and premerge-revalidated
review snapshot, so body edits invalidate old eligibility.

The worker also enumerates all repository collaborators
at start and immediately before merging. Only `jfox85` may have push, maintain or
admin permission during this single-owner pilot. Additional read-only users are
fine; additional writers or unavailable permission evidence stop the pilot. The
worker reads each listed user's explicit repository permission through the
metadata-readable permission endpoint, rather than requiring optional permission
flags in the collaborator listing. It saves public identity/permission snapshots
in the result artifact before rejecting a writer check, including checks just
before merging. Unknown permissions, mismatched identities and failed reads
remain blocking. This
binds same-repository branch control to the allowed principal rather than merely
trusting the original PR creator. Expanding the writer set requires a separately
reviewed change to the pilot.

The ordinary workflow token returned an empty collaborator list in the live
probe. Writer enumeration and explicit permission reads therefore use a separate
fine-grained personal access token stored as `GREEN_LANE_ACCESS_AUDIT_TOKEN` in
Redline Actions secrets. Create it as `jfox85`, an organization member with admin
access; this endpoint requires the token's authenticated user to be an organization
member with write, maintain or admin access (a read-only or outside collaborator
cannot perform this audit). Select resource owner `croutoncreations`, only the
`redline` repository, and only **Metadata: read**; use a 30-day expiration for the
pilot. No organization permissions, contents access or write permissions are
needed. The workflow uses this client only for the two access-audit APIs, while
the normal workflow token continues to evaluate and merge. The audit secret is
removed from the environment before the evaluator runs and is never written to
artifacts. Missing, expired or insufficient audit credentials stop merging;
renew the secret before expiration to keep the pilot running. No GitHub App or
branch administration service is introduced.

The merged Homebrew CI check is also required for live/shadow eligibility and main protection. Release CI selection accepts only `push` or `workflow_dispatch` runs of the CI workflow on main for the exact tagged commit; PR-event runs remain excluded.

Release-check and Homebrew publishing helpers (`scripts/brew-lint.sh`, `scripts/check-homebrew-versions.sh`, `scripts/update-homebrew-cask.sh`) are critical paths and excluded from automated merging.
