# Green Lane Shadow Mode

## Overview

The `green-lane-shadow` check is a **report-only** evaluation that runs on every pull request to simulate auto-merge decisions without actually merging anything. It evaluates PRs against eight rules and posts a clear "would merge" or "would not merge" verdict.

**Important:** This is shadow mode. The check will NEVER:
- Merge a PR
- Approve a PR
- Enable auto-merge
- Add labels that affect merging
- Change branch protection
- Touch repository settings

**Never make `green-lane-shadow` a required check.** It concludes `success`
when a PR *would* merge, and it is posted by `github-actions` rather than a
pinned App. Requiring it would turn a report into a merge gate that any
workflow run could satisfy. There is no human override: the verdict is a
report, and humans merge exactly as before.

## Lanes

Every PR is evaluated in one of two lanes. Both are shadow only.

| Lane | Selected when | Repro rule | Size caps |
|---|---|---|---|
| `code` | anything else | required (fails before, passes after) | ≤150 lines, ≤300 lockfile/generated, ≤6 files |
| `docs-deps` | every file is Markdown (`*.md`) documentation or a Go/npm manifest or lockfile (`go.mod`, `go.sum`, `package.json`, `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`) | replaced by **File Types**: `go.mod`/`package.json` edits must be version bumps only (no `replace`, scripts, git/URL deps), must change at least one version, and must remove and re-add the same dependency names in place (no added, dropped, renamed or section-moved dependencies). `package.json` entries must sit in a dependency section (`dependencies`, `devDependencies`, `optionalDependencies`, `peerDependencies`) visible in the diff context; the package's own `version` or other semver-shaped fields do not count. Bare `go.mod` module/version lines count only inside a visible `require (` block (not `exclude`, `retract`, ...) | ≤100 lines, ≤150 lockfile, ≤5 files |

What does **not** qualify, and so stays in the `code` lane with the repro rule:
- Non-Markdown files anywhere, including under `docs/` (`docs/build.sh`, `docs/example.go`). The lane is chosen by file type, not directory.
- Markdown, manifests or lockfiles under `cmd/`, `internal/` or any `testdata/` directory. Tests can read them and `go:embed` can ship them, so they are inputs, not documentation or dependencies.
- Renames whose source path does not qualify (for example `internal/x.go` → `docs/x.md`). Denied Paths also checks both paths of a rename, and File Types rejects any rename to or from a manifest or lockfile.
- Lockfiles whose manifest is not admitted: `Cargo.lock`, generic `*.lock`, `Package.resolved`, `gradle.lockfile`. Every lockfile the lane admits has a manifest mapping in the Size rule, so a lockfile-only PR always fails Size.

File Types screens lockfile contents but **never passes a lockfile change**
until trusted resolver validation exists. Obvious violations fail; a plausible
`go.sum` or `package-lock.json` change is **inconclusive**, with a message
requiring trusted resolver validation and manual review. Manifest-only version
bumps and docs can still pass File Types. This restriction is independent of the
Reviewer placeholder, so implementing reviewer provenance cannot accidentally
admit unverified lockfiles.

The defense-in-depth screen requires added npm `resolved` URLs to name the
exact enclosing package on `https://registry.npmjs.org/`, followed by one plain
filename. Dot segments, backslashes, percent escapes, extra path segments,
queries, fragments and empty filenames are rejected. `"link": true`, deleted
lockfiles, missing diffs and unimplemented pnpm/yarn validation also fail.
`go.sum` entries must have the expected syntax. Registry URL/integrity or
checksum validation does not establish that a lockfile matches its manifest:
install-script metadata, arbitrary transitive version edits, and other
hand-written changes still need resolver comparison in an isolated trusted
job. That job must not have merge credentials or execute install scripts.

The CI, Reviewer, Duplicates and Denied Paths rules are identical in both
lanes. The lockfile-needs-matching-manifest rule applies in both lanes with
no exception: a lockfile-only security bump (for example #179's
`marketing/video/package-lock.json` without `marketing/video/package.json`)
fails Size by decision.

## Rules Evaluated

The green-lane-shadow check evaluates eight rules:

### 1. Size (lane caps)
- `code` lane: ≤150 regular changed lines (additions + deletions), ≤300 lockfile/generated lines, ≤6 files
- `docs-deps` lane: ≤100 regular lines, ≤150 lockfile lines, ≤5 files
- Lockfile changes require a matching manifest change
- Generated file changes require a matching source change

**Lockfiles and generated patterns:**
- `go.sum`, `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `*.lock`
- `Package.resolved`, `gradle.lockfile`
- `*.pb.go`, `*_gen.go`, `*.generated.*`, `dist/**`

### 2. Test Repro (fails before, passes after) — `code` lane only
Tests that the same test IDs:
- Execute and fail on the base commit
- Execute and pass on the head commit
- In fresh workspaces with identical blobs and dependencies

**Implementation** (`cmd/green-lane-repro-analyze`, always built from the base commit):
- `plan` diffs merge-base..head from git objects and parses changed `_test.go`
  files with `go/parser`. Tests come in two kinds:
  - **Required:** top-level `TestXxx` functions that are new or whose own
    source (including `//go:` directives such as `go:embed`) changed. Each
    must fail on base and pass on head.
  - **Optional:** unchanged tests that are affected indirectly. These are
    tests that use a new, changed or removed top-level declaration in any of
    the package's test files (for example a case added to `var cases = ...`
    in `helpers_test.go`, used directly or through a helper or method),
    tests in a file whose imports, build constraints or package clause
    changed, every test when a blank/dot import, `init` or `TestMain`
    changed, and tests that read a changed `testdata/` fixture (including
    tests in other packages that reach it through a path like
    `../testdata/case.json`). Each must pass on head, and a base failure
    counts as a repro, but they need not all fail on base: a shared helper
    change touches many tests that keep passing there.

  A repro needs every required test to fail on base. A package with only
  optional tests needs at least one of them to fail on base. If no test in a
  fixture-only package visibly reads testdata, all its tests are optional.
  Tests that use nothing changed are not run. A test name defined in both
  the internal and external test package of a directory is ambiguous in
  `go test -json` and is scored inconclusive. Only `testdata/` is overlaid
  onto base. If a planned package's tests mention a changed file outside
  `testdata/` (for example `fixtures/case.json`, `../../fixtures/case.json`
  resolved from the package directory, or a Go source fixture named by its
  exact path), the result is inconclusive, because base would run against
  the old file.
- `run` creates fresh worktrees for the merge-base and the head, overlays the
  PR's `_test.go` files **and `testdata/` fixtures** onto the merge-base
  (deleting the ones the PR deletes or renames, and keeping each file's git
  mode, so executable fixtures and symlinks match the head), and
  runs `go test -count=1 -json -run '^(TestA|TestB)$' ./pkg` per package. The
  `./` matters: `go test internal/foo` resolves inside GOROOT and fails with
  "package internal/foo is not in std", which is what made every earlier
  shadow run report "Compile failed on base commit".
- `analyze` runs in a separate job from trusted code, recomputes the plan
  itself, and scores the raw results: each test must execute and fail on
  base, then execute and pass on head. Skips count as failures; a parent
  test's own status is used, not its subtests'. Compile failures on either
  side are inconclusive and report go's first error lines.
- Evidence artifacts: `repro-raw-<run>` (plan, go test JSON and stderr per
  side) and `repro-evidence-<PR>-<run>` (trusted plan, per-test statuses, and
  `base_failure_output.txt` with the base-side output of every test that
  failed there, or go's compile error; always written, with an explicit
  marker when nothing failed on base).
- The jobs that run PR code have a read-only token and no secrets.

**Trust boundary:** the `repro` job executes PR code, so its raw results are
evidence produced by untrusted code. A malicious test could always detect
base vs head itself, so this does not weaken the rule further, but the live
gate must not treat repro alone as sufficient.

### 3. CI Status
All seven required checks must pass on the current head SHA:
- `actionlint`
- `go`
- `dashboard`
- `linux-smoke`
- `windows-smoke`
- `macos`
- `macos-intel`

### 4. Review readiness and signed provenance

**Review Readiness** requires a complete, paginated GraphQL thread list with
zero unresolved threads (including outdated ones), no effective change requests
from any head, and a non-author approval on the current head. Reviews are
ordered by submission time and ID, not API array order. Later COMMENTED reviews
do not clear change requests; a later effective approval or GitHub dismissal
can. A dismissed approval no longer counts. A reply, PR-body assertion, or
outdated diff is not a disposition: the thread must actually be resolved.

**Reviewer** independently verifies author/reviewer provenance. The default
issuer allowlist is empty, so the repository remains blocked until trusted
issuers are provisioned. Once configured, each receipt must have:

- a valid Ed25519 signature from an allowed, role-scoped issuer key;
- the expected publishing GitHub App ID, read from GitHub's check-run API;
- the same repository, PR number, head and base SHA as the evaluation;
- actual provider/model, harness, run ID, schema version and issuance time;
- for a reviewer, an approved outcome and the exact effective GitHub review
  ID/login on the current head, matching the issuer's configured reviewer.

The author and reviewer providers must differ. Unknown issuers/providers,
conflicting author receipts, forged signatures, wrong publishing Apps, stale
receipts, dismissed/superseded reviews and missing execution metadata block.
CodeRabbit activity remains diagnostic: its service identity does not establish
which model provider reviewed relative to the author.

### PR State and snapshot binding

An additional PR State rule requires an open, non-draft, mergeable PR on an
allowed base branch (default `main`). Unknown mergeability blocks. The CLI
collects the target twice around complete duplicate discovery and rejects
changes to files, checks, receipts, reviews, threads or metadata between reads.
It also checks workflow-expected head/base SHAs and binds code repro evidence to
both SHAs. GitHub file counts and GraphQL total counts catch missing pages.

CI checks must match their configured App and workflow IDs, current head SHA,
and the actual Actions job's check-run URL, run ID, name, status and conclusion.
A display name or hand-written details URL alone cannot satisfy CI.

The trusted publisher re-fetches PR metadata and every review/thread page
immediately before posting; check/receipt and review-state fingerprints catch CI reruns, pending/failed provenance, resolution or
review changes even when `updated_at` does not change. A trusted closed-only `pull_request_target` job immediately publishes a neutral invalidation without checkout, build, PR execution or merge credentials. A mismatch, closed/draft
PR, unknown mergeability or evaluation older than five minutes clears the
candidate bit and keeps the report blocked. This is snapshot validation for a
report, not an atomic merge transaction: base freshness/merge-queue integration
and the final live merge recheck remain required before enabling automation.

### 5. No Duplicate PRs
Checks that no other open PR addresses the same root cause by:
- Requiring a `Root-Cause:` trailer in the PR body (format: `area/file-or-symbol#slug`)
- Failing if another PR has the exact same root cause
- Failing if another PR edits the same non-test file AND has ≥50% token overlap in root cause slugs

### 6. Denied Paths
Blocks changes to sensitive paths including:
- Workflows and actions: `.github/workflows/**`, `.github/actions/**`
- Secrets and credentials: `**/.env*`, `**/*secret*`, `**/*credential*`, `**/*.pem`, etc.
- Auth and billing code: `internal/apiauth/**`, `internal/nativeusage/credentials*`
- Release infrastructure: `.goreleaser.yaml`, signing scripts, entitlements
- Green-lane controls: `.github/green-lane.yml`, `.github/green-lane/**`, `.github/prompts/**`, test runner config
- Green-lane code: `cmd/green-lane-check/**`, `cmd/green-lane-repro-analyze/**`, `internal/greenlane/**`
- Schema migrations: `internal/store/sqlite.go`

The complete list is `DefaultDeniedPaths` in `internal/greenlane/config.go`.
`.github/green-lane.yml` does not exist yet; it is reserved (and denied) for
the live gate, which will read it from the base branch.

## Reading the Output

The check run summary shows:

1. **Verdict:** "would merge" or "would not merge", and the lane
2. **Per-rule status:** ✅ PASS, ❌ FAIL, or ⚠️ INCONCLUSIVE
3. **Messages:** Clear explanation of what passed, failed, or couldn't be determined
4. **Final summary:** Overall verdict with reasons

### Example Output

```
❌ WOULD NOT MERGE

Rule Results:
✅ PASS: Size
OK: 85 changed lines in 3 files (45 lockfile/generated)

✅ PASS: Test Repro
OK: tests [TestPagination] failed on base, passed on head

✅ PASS: CI
OK: all 7 required checks passed

❌ FAIL: Reviewer
Approval found, but cross-vendor provenance not verified (shadow mode: needs human or real Apps)

✅ PASS: Duplicates
OK: no duplicate found for Root-Cause: backend/routes/users#pagination-bug

✅ PASS: Denied Paths
OK: no denied paths touched

Summary:
Would not merge. Reasons:
- Reviewer: Approval found, but cross-vendor provenance not verified
```

## Artifacts

Each evaluation is saved as a JSON artifact with 30-day retention:
- Artifact name: `green-lane-evaluation-{PR#}-{run-id}`
- Contains: Full evaluation result, timestamp, all rule outcomes

Use these to review what the gate "would have done" over the shadow period.

## Pre-Enable Checklist

Before switching from shadow to live merges, the following must be built and verified:

### Infrastructure (Does Not Exist Yet)
1. ✅ Evaluator logic (this implementation)
2. ✅ Test repro job with Go test runner, fresh workspaces, `-count=1`, evidence preservation
3. ✅ CodeRabbit head-SHA detection with inline findings check
4. ❌ **Trusted dispatcher deployment** recording observed author/reviewer execution metadata (receipt signer/verifier implemented; issuer services/keys/Apps not provisioned)
5. ❌ **Read-only analyzer** with structured prompts (separate job, no secrets/tokens)
6. ❌ **Review-publishing service deployment** using isolated reviewer App credentials (report publication checks are implemented; review posting/issuer provisioning is not)
7. ❌ **Reviewer GitHub Apps** (Claude and Codex) with installation tokens
8. ❌ **Gate GitHub App** for posting the `green-lane` check (pinned via `app_id`) and merging
9. ❌ **Branch protection ruleset** requiring `green-lane` check pinned to gate App's `app_id`

### Policy & Process
10. ❌ **Extended denied paths** in `.github/green-lane.yml` read from base branch
11. ✅ **Lockfile/generated limits** enforced (code: 300 lines; docs/deps: 150; matching manifest/source required)
12. ❌ **Live recheck before merge** (paginated duplicate file lists and report snapshot checks are implemented; atomic merge/base freshness and SHA-pinned merge are not)

Lockfile eligibility is blocked until trusted resolver validation exists; syntax
and URL screening alone do not satisfy that requirement.

### Validation
13. ❌ **Shadow period complete:** At least 1 week with 0 wrong "would merge" calls
14. ❌ **No provenance gaps** on PRs called eligible (except CodeRabbit, which uses App identity)
15. ❌ **Repro results stable** across reruns (no flaky passes)

## Current Limitations (Shadow Mode)

1. **No actual merges:** This check is report-only. Manual merging is unchanged.

2. **Trusted issuer deployment is incomplete:**
   - Signature, App identity, provider independence and review-ID verification are implemented and tested.
   - No real issuer keys, publishing Apps or author/reviewer dispatcher services are provisioned; default provenance remains blocked.
   - CodeRabbit activity is diagnostic; its service name is not a model-provider receipt.

3. **Test repro limitations:**
   - ✅ Fully implemented for Go test files (`*_test.go`)
   - ❌ Not implemented for TypeScript/Jest/Vitest, Kotlin, or Swift tests
   - Code-lane PRs without new/changed tests or affected fixture readers are inconclusive
   - Changed test bodies are detected; directly changed tests and indirectly affected tests follow the required/optional rules described above

4. **Duplicate detection heuristic:** The client fetches complete file lists and both rename paths for other open PRs. The root-cause/token-overlap heuristic remains approximate and concurrent repository changes still require a final live recheck.

5. **No policy file yet:** Denied paths and limits are compiled into `internal/greenlane/config.go`, which is itself a denied path and is built from the base commit. A `.github/green-lane.yml` config file will be added before going live.

6. **Check not pinned:** The shadow check is not pinned to an App ID and must never be required (see the warning at the top). The real `green-lane` check will be required and pinned to prevent spoofing.

## Owner's Pre-Approval

This shadow pilot design (rev 3) was reviewed and approved by the repo owner (Jon Fox, GitHub `jfox85`) on Oct 8, 2026. See `green-lane-plan-rev3.md` for the full design, risk analysis, and rollout plan.

## Advisory candidates

`advisory_candidate` is true only when every non-provenance rule passes. It is
separate from `verdict`, has an explicit non-authorization message in JSON and
the check summary, and never changes the check conclusion. Missing provenance
still produces `would_not_merge`/neutral. An unverified lockfile, unresolved
thread, stale repro, missing required CI, duplicate or denied path cannot
become an advisory candidate. A fully valid, independently signed example can
produce `would_merge` in shadow; there is still no merge step or required gate.

## Reconciliation and manual reruns

GitHub exposes thread resolved/unresolved webhooks but does not list them as
Actions triggers. See the [webhook documentation](https://docs.github.com/en/webhooks/webhook-events-and-payloads#pull_request_review_thread)
and [Actions event reference](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows).
`green-lane-reconcile.yml` therefore refreshes open PR reports hourly at minute
23, or by manual dispatch. It builds once from trusted default-branch code,
uses only read scopes plus checks:write, disables caches, and never executes PR
code or consumes untrusted repro artifacts. There are no merge credentials.
The sweep collects duplicate file lists once into a trusted job-local snapshot
(repository-bound, maximum five minutes old) rather than repeating an O(N²)
scan for every PR. Both dispatch paths reject non-default-branch workflow refs.
API failures post a neutral unavailable report; incomplete data never admits a
candidate. More than 25 open PRs requires explicit sharding rather than silently
truncating the sweep. Standard Actions schedule delay/capacity limits apply.

The lightweight sweep deliberately supplies no repro evidence. Code-lane PRs
therefore remain blocked until a full current-head/base repro run; docs PRs can
refresh all their readiness rules without executing PR code. To rerun the full
pipeline, dispatch **Green Lane Shadow** with the `pr_number` input from the
trusted main branch. Dispatch repro permits same-repository PRs only, preserving
the default-branch cache boundary; fork PR repro still runs in PR events.

## Provisioning provenance issuers

The verifier and `cmd/green-lane-provenance` signer are implemented; no real
issuer service, keys, GitHub App or live gate is created by this change. Add
issuer policy in trusted base configuration only after the real author/reviewer
services are ready. Each key ID maps to base64 Ed25519 public key, one role,
provider, publishing App ID, and (for reviewer keys) the expected reviewer login.
An empty allowlist is intentional. Removing an issuer revokes its receipts.

Run the signer only inside the isolated trusted dispatcher/publisher after it
has observed a completed author/reviewer execution. Feed structured metadata on
stdin, pass `-key-id`, and supply the 64-byte Ed25519 private key as base64 in
`GREEN_LANE_SIGNING_KEY`. The signer never prints the private key. Do not infer
providers from PR bodies, GitHub users or harness names, or sign PR-controlled
metadata without validating it against the actual execution record. Keep signing keys in an external trusted service or protected environment restricted to the default branch, not ordinary repository secrets accessible to PR-controlled workflows. Never put
the signing key or review/merge credentials in a job that executes PR code.

The signed payload is transported as base64 (`payload`) so whitespace changes
in the outer JSON cannot break its signature. Signature bytes are base64 and
cover `redline-green-lane-provenance-v1\n` plus the decoded payload bytes.
Publish an array of signed receipts in a completed successful check named
`green-lane-provenance`, using each issuer's configured publishing App. The
collector overwrites source App identity using the API; a receipt cannot claim
its own source App. Retain the observed execution record outside PR-controlled
files with the run ID for audit. Receipts bind both head and base, so either
changing requires fresh receipts. Ingestion is limited to 100 receipts/64 KiB
per check. CI identities and issuer policy must be configured per repository.

## Next Steps

1. Review and merge this shadow-only readiness follow-up; its own check still
   uses old trusted base tooling until merge. CI tests the new verifier,
   collector and publisher boundaries directly.
2. Provision the trusted author/reviewer services, role-scoped keys and
   publishing Apps; then validate real independent review receipts. The
   cryptographic positive path is tested locally, but no production issuer
   has been configured or claimed to exist.
3. Collect manually adjudicated candidates and rejection cases for at least a
   week after real receipts can exercise the positive path. Record false
   positives/negatives and rerun stability.
4. Build a separate protected live gate with App-bound branch protections,
   isolated merge credentials, base freshness/merge queue and a final
   SHA-pinned merge. Keep lockfiles manual until resolver validation exists.

There is no human override in the shadow check. Manual merging is unchanged.
