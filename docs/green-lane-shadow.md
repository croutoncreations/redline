# Green Lane Shadow Mode

## Overview

The `green-lane-shadow` check is a **report-only** evaluation that runs on every pull request to simulate auto-merge decisions without actually merging anything. It evaluates PRs against six rules and posts a clear "would merge" or "would not merge" verdict.

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

The green-lane-shadow check evaluates six rules:

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
All required checks must pass on the current head SHA:
- `go`
- `dashboard`
- `linux-smoke`
- `windows-smoke`
- `macos`
- `macos-intel`

### 4. Cross-Vendor Reviewer
Requires an approved review from a different AI provider than the author.

**CodeRabbit Detection (Implemented):**
CodeRabbit (`coderabbitai[bot]`) counts as a reviewer vendor when it posts a real review on the current head SHA:
- **Explicit approve/request-changes:** `APPROVED` or `CHANGES_REQUESTED` reviews count
- **Inline findings:** `COMMENTED` reviews with inline review comments (not just summary/walkthrough) count
- **Non-blocking findings are fine:** Comments without REQUEST_CHANGES don't fail the gate
- **Empty or summary-only comments don't count:** Must have actual diff review activity

The check reports CodeRabbit status separately (approved/reviewed/requested changes + findings count) even though full cross-vendor provenance is not yet implemented.

**Status:** CodeRabbit detection is fully implemented. The required infrastructure for other reviewers does not exist yet:
- Trusted dispatcher recording provenance (harness, provider/model, run ID, head SHA)
- Read-only analyzer with structured prompts
- Trusted publisher with separated approval credentials
- Reviewer GitHub Apps (Claude and Codex)

Shadow mode always reports "needs human or real Apps" for the overall reviewer rule, but shows CodeRabbit evidence when available.

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
OK: all 6 required checks passed

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
4. ❌ **Trusted dispatcher** recording provenance for author and reviewer runs (except CodeRabbit)
5. ❌ **Read-only analyzer** with structured prompts (separate job, no secrets/tokens)
6. ❌ **Trusted publisher** that validates analyzer output and submits reviews with reviewer App tokens
7. ❌ **Reviewer GitHub Apps** (Claude and Codex) with installation tokens
8. ❌ **Gate GitHub App** for posting the `green-lane` check (pinned via `app_id`) and merging
9. ❌ **Branch protection ruleset** requiring `green-lane` check pinned to gate App's `app_id`

### Policy & Process
10. ❌ **Extended denied paths** in `.github/green-lane.yml` read from base branch
11. ✅ **Lockfile/generated limits** enforced (code: 300 lines; docs/deps: 150; matching manifest/source required)
12. ❌ **Live recheck before merge** (fully paginated duplicate query with file lists, fresh check/review state, SHA-pinned merge)

Lockfile eligibility is blocked until trusted resolver validation exists; syntax
and URL screening alone do not satisfy that requirement.

### Validation
13. ❌ **Shadow period complete:** At least 1 week with 0 wrong "would merge" calls
14. ❌ **No provenance gaps** on PRs called eligible (except CodeRabbit, which uses App identity)
15. ❌ **Repro results stable** across reruns (no flaky passes)

## Current Limitations (Shadow Mode)

1. **No actual merges:** This check is report-only. Manual merging is unchanged.

2. **Reviewer provenance partially implemented:** 
   - ✅ CodeRabbit detection works: The check distinguishes between real reviews (with findings or explicit approve/request-changes) and summary-only comments
   - ❌ Other reviewer provenance not verified: Cannot distinguish between Anthropic and OpenAI reviews, so non-CodeRabbit approvals always report "needs human"
   - The real gate will verify cross-vendor provenance from the trusted dispatcher for all reviewers

3. **Test repro limitations:**
   - ✅ Fully implemented for Go test files (`*_test.go`)
   - ❌ Not implemented for TypeScript/Jest/Vitest, Kotlin, or Swift tests
   - Code-lane PRs without new/changed tests or affected fixture readers are inconclusive
   - Changed test bodies are detected; directly changed tests and indirectly affected tests follow the required/optional rules described above

4. **Duplicate detection simplified:** The check doesn't fetch files for every open PR (to stay fast), so it may miss some duplicate patterns. The real gate will use full file lists.

5. **No policy file yet:** Denied paths and limits are compiled into `internal/greenlane/config.go`, which is itself a denied path and is built from the base commit. A `.github/green-lane.yml` config file will be added before going live.

6. **Check not pinned:** The shadow check is not pinned to an App ID and must never be required (see the warning at the top). The real `green-lane` check will be required and pinned to prevent spoofing.

## Owner's Pre-Approval

This shadow pilot design (rev 3) was reviewed and approved by the repo owner (Jon Fox, GitHub `jfox85`) on Oct 8, 2026. See `green-lane-plan-rev3.md` for the full design, risk analysis, and rollout plan.

## Next Steps

1. Merge these shadow-only tooling fixes after code review, then verify the new
   plan → run → analyze pipeline in real Actions runs. This PR's shadow scorer
   is still built from the old base branch; its result does not exercise the
   new analyzer.
2. Implement review readiness and trusted provenance as described below.
3. Collect manually adjudicated candidate and rejection examples for at least
   one week after the positive eligibility path is observable.
4. Build the protected merge gate and complete the pre-enable checklist before
   considering live merges. Keep lockfile changes manual until resolver
   validation is implemented.

### Follow-up: review readiness and trusted provenance

The current Reviewer rule intentionally always fails. A successful review
workflow, CodeRabbit findings, or a human approval alone does not verify the
cross-vendor requirement. Merely waiting a week cannot exercise `would_merge`.
Implement this as a focused follow-up, rather than adding a partial merge gate
to the repro repair:

1. **Review readiness:** fetch every review thread with GraphQL pagination.
   Block unresolved threads, including outdated threads: an outdated diff or a
   reply does not prove the finding was addressed. Use the latest effective
   review per reviewer; outstanding change requests remain blocking across
   pushes until cleared by a later effective review or a trusted dismissal.
   A disposition must be recorded by a trusted reviewer/owner and the thread
   resolved; PR-body claims are not evidence. Reevaluate on thread
   resolve/reopen, new review, dismissal, and head changes. If the thread event
   is not available to the workflow, use a trusted webhook dispatcher or
   reconciliation job; a review-comment event alone is insufficient.
2. **Trusted provenance:** have the trusted dispatcher store author and
   reviewer receipts outside PR-controlled files. Each receipt identifies the
   repository/PR, head SHA, harness, provider/model and run ID. A reviewer
   receipt also binds to the reviewed content and structured outcome. Validate
   the publishing App/installation and the receipt's trusted issuer; a login,
   workflow success or comment text is insufficient. Unknown author/provider,
   missing receipt or same-provider review remains ineligible. CodeRabbit's
   App identity establishes its service identity, not automatically an
   independent model provider relative to the author.
3. **Useful shadow reporting:** report review readiness and provenance
   separately. Add a clearly advisory candidate field only when all implemented
   non-provenance requirements pass, with explicit unimplemented blockers.
   Never turn that field into a successful authorization check. The final
   verdict remains `would_not_merge` until every mandatory requirement passes;
   the existing `would_merge` verdict must not be weakened.
4. **Consistent snapshot:** require an open, non-draft, mergeable PR against an
   allowed base branch. Bind CI (including expected check/workflow identities),
   review receipts, repro, file lists and policy to the same head/base SHAs.
   The present workflow's early head comparison is not sufficient if the CLI
   fetches a newer PR afterward. Reject mismatches inside the evaluator and
   re-fetch state before publication and immediately before a SHA-pinned merge.
   Require base freshness or merge-queue validation for integration safety.
5. **Acceptance tests:** unresolved/outdated threads; dismissed and superseded
   reviews; changes requested on an older head; same-provider and unknown
   provenance; forged receipts; pushes between reads; changed base; draft or
   conflicted PRs; missing/pending CI; and missing API pages all stay blocked.
   Include a real independently reviewed positive candidate and demonstrate
   its advisory status separately from merge authorization.

Once these checks have been validated in shadow, implement the trusted publisher
and gate App with separate credentials and App-bound branch protections. Never
execute PR code in the job that holds review-publishing or merge credentials.
There is no human override in the shadow check; manual merging is unchanged.

---

**Questions?** See the full plan in `green-lane-plan-rev3.md` or reach out to the repo owner.
