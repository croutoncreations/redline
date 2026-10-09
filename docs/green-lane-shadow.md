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

## Rules Evaluated

The green-lane-shadow check evaluates six rules:

### 1. Size (≤150 changed lines)
- Regular files must have ≤150 changed lines (additions + deletions)
- Lockfiles and generated files have a separate cap of ≤300 lines
- Maximum 6 files total
- Lockfile changes require a matching manifest change
- Generated file changes require a matching source change

**Lockfiles and generated patterns:**
- `go.sum`, `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `*.lock`
- `Package.resolved`, `gradle.lockfile`
- `*.pb.go`, `*_gen.go`, `*.generated.*`, `dist/**`

### 2. Test Repro (fails before, passes after)
Tests that the same test IDs:
- Execute and fail on the base commit
- Execute and pass on the head commit
- In fresh workspaces with identical blobs and dependencies

**Implementation:**
- Identifies added/changed Go test function names from the diff
- Runs those specific tests on base (with PR's test files overlaid) using `go test -count=1 -json -run '^(TestA|TestB)$'`
- Runs the same tests on head in a fresh workspace
- Each test must execute and fail on base, then execute and pass on head
- Skipped tests count as failures
- Compile failures on either base or head are inconclusive
- Preserves evidence: test/fixture blob hashes, results, base failure output
- Runs with read-only permissions and no secrets

**Status:** Fully implemented. PRs without changed test files are marked inconclusive.

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
- Green-lane controls: `.github/green-lane.yml`, test runner config
- Schema migrations: `internal/store/sqlite.go`

See `.github/green-lane.yml` for the complete list (planned).

## Reading the Output

The check run summary shows:

1. **Verdict:** "would merge" or "would not merge"
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
11. ❌ **Lockfile/generated limits** enforced (max 300 lines, must have matching manifest/source)
12. ❌ **Live recheck before merge** (fully paginated duplicate query, fresh check/review state, SHA-pinned merge)

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
   - PRs without changed test files are marked inconclusive
   - The evaluator expects to see test function changes; PRs that only fix tests without changing their signatures may not be detected correctly

4. **Duplicate detection simplified:** The check doesn't fetch files for every open PR (to stay fast), so it may miss some duplicate patterns. The real gate will use full file lists.

5. **No policy file yet:** Denied paths and limits are hardcoded in the evaluator. A `.github/green-lane.yml` config file will be added before going live.

6. **Check not pinned:** The shadow check is not pinned to an App ID, so it's not a required check. The real `green-lane` check will be required and pinned to prevent spoofing.

## Owner's Pre-Approval

This shadow pilot design (rev 3) was reviewed and approved by the repo owner (Jon Fox, GitHub `jfox85`) on Oct 8, 2026. See `green-lane-plan-rev3.md` for the full design, risk analysis, and rollout plan.

## Next Steps

1. **Monitor shadow runs:** Review the check results on PRs over the next week.
2. **Verify verdicts:** Check that "would merge" decisions match your manual judgment.
3. **Build missing infrastructure:** Implement the pre-enable checklist items.
4. **Go/no-go:** After the shadow period, decide whether to proceed with live merges.

---

**Questions?** See the full plan in `green-lane-plan-rev3.md` or reach out to the repo owner.
