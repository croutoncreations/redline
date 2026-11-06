package tasktemplate

import (
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

// Template is an editable starting point. Creating a task copies these values;
// tasks never retain a link to the template or change when the catalog evolves.
type Template struct {
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	Description       string              `json:"description"`
	Prompt            string              `json:"prompt"`
	Priority          int                 `json:"priority"`
	Type              domain.TaskType     `json:"type"`
	DispatchTier      domain.DispatchTier `json:"dispatch_tier"`
	MinInterval       time.Duration       `json:"min_interval"`
	RequireRepoChange bool                `json:"require_repo_change"`
	Requirements      []string            `json:"requirements,omitempty"`
}

func Catalog() []Template {
	return []Template{
		{
			ID: "bug-hunt", Name: "Find and fix one reproducible bug",
			Description: "Reproduce one real defect, fix its root cause, and verify the affected suite.",
			Priority:    80, Type: domain.Recurring, DispatchTier: domain.DispatchBehind,
			MinInterval: 3 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Test and static-analysis tools"},
			Prompt: `Find ONE real, demonstrable bug in this repository and fix it.

Before choosing a bug, list this repository's open pull requests, including drafts (for example, gh pr list --state open --limit 100). Skip any bug whose root cause is already being fixed by an open pull request, even if it was reported through a different symptom, and pick a different one.

Use issues, static analysis, failing tests, and suspicious error or boundary handling as leads. Reproduce the bug before changing code, ideally with a failing test. Fix the root cause minimally, rerun the reproduction, and run the affected test suite. Do not treat a design preference as a bug.

Immediately before pushing or opening a pull request, list open pull requests again. If one now addresses the same root cause, do not push; summarize your findings instead.

Make one cohesive, reviewable change. If no bug can be reproduced, make no changes and summarize what you ruled out.`,
		},
		{
			ID: "test-gap", Name: "Close one high-risk test gap",
			Description: "Add focused tests for an important untested failure path or boundary.",
			Priority:    70, Type: domain.Recurring, DispatchTier: domain.DispatchBehind,
			MinInterval: 3 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Test runner"},
			Prompt: `Find and close ONE meaningful gap in this repository's test coverage.

Measure coverage when tooling exists, then rank gaps by risk: persistence, concurrency, parsing, security checks, failure paths, and boundary conditions outrank trivial getters. Confirm the behavior is genuinely untested, add focused deterministic tests following repository conventions, and run the affected package's full suite.

Make one cohesive, reviewable change. If no worthwhile gap exists, make no changes and explain what you examined.`,
		},
		{
			ID: "quickstart", Name: "Verify the quickstart from scratch",
			Description: "Follow the documented setup as a newcomer and repair one confirmed break or ambiguity.",
			Priority:    50, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval: 14 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Ability to build in a temporary directory"},
			Prompt: `Test this repository's quickstart exactly as a newcomer would.

Use a clean temporary checkout or export. Follow the documented install, configure, build, run, and test steps literally. Record failures, missing prerequisites, and ambiguous instructions. Fix ONE cohesive confirmed problem in the documentation, setup scripts, or defaults, then repeat the corrected path.

Do not invent credentials or document aspirational behavior. If the quickstart works, make no changes and report what was verified.`,
		},
		{
			ID: "dependency-update", Name: "Apply a safe dependency update",
			Description: "Take a bounded patch/minor update after reviewing compatibility and advisories.",
			Priority:    60, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval:  7 * 24 * time.Hour,
			Requirements: []string{"Repository access", "Package-manager network access"},
			Prompt: `Apply ONE safe, cohesive dependency update to this repository.

Enumerate outdated direct dependencies. Prefer a patch or minor update with a correctness, security, or tooling benefit. Read its release notes before updating, regenerate lockfiles, and run the relevant tests, build, and linters. Do not take unrelated major upgrades or bundle broad cleanup.

If no update is worthwhile or verification fails, make no changes and explain why.`,
		},
		{
			ID: "error-clarity", Name: "Improve one confusing error path",
			Description: "Make one failure actionable without leaking secrets or creating log noise.",
			Priority:    45, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval: 14 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Test runner"},
			Prompt: `Improve ONE confusing error or logging path in this repository.

Choose a cohesive path where a failure is swallowed, lacks actionable context, crashes unnecessarily, duplicates noise, or risks leaking a secret. Trigger the failure first and record the current behavior. Improve it using the repository's existing error conventions, include the operation and safe relevant values, and add or update a focused test.

Run the affected suite. If the audited area is already clear and safe, make no changes and report what you checked.`,
		},
		{
			ID: "dead-code", Name: "Remove one verified dead-code cluster",
			Description: "Delete one cohesive unused code path after checking indirect and public references.",
			Priority:    45, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval: 14 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Build and test tools"},
			Prompt: `Find and remove ONE cohesive cluster of verified dead code.

Use the language's analysis tools and repository-wide search, then manually check configuration, reflection, templates, docs, build scripts, and public API obligations before deleting anything. Remove the dead symbol, its tests, and stale documentation completely. Build, lint, and run the relevant suite afterward.

Do not perform a scattershot cleanup. If nothing is safely removable, make no changes and explain what you checked.`,
		},
		{
			ID: "documentation-drift", Name: "Fix one documentation mismatch",
			Description: "Correct one user-facing instruction or contract that no longer matches verified behavior.",
			Priority:    50, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval: 14 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Tools to verify documented behavior"},
			Prompt: `Find and correct ONE consequential mismatch between this repository's documentation and its current behavior.

Check user-facing configuration, commands, API examples, or operational guides against the implementation and focused executable checks. Prefer a mismatch that could cause a user to fail a task or misunderstand a supported feature. Verify the current behavior is intended before documenting it; do not normalize a bug or describe aspirational behavior. Leave quickstart walkthroughs to the quickstart task.

Check open issues and pull requests, including drafts, before choosing work; skip anything already being addressed. Correct one cohesive documentation area, cite the implementation or command output that establishes the mismatch, and rerun the corrected example or relevant documentation checks. Immediately before pushing or opening a pull request, list open pull requests again, including drafts. If an open pull request already addresses the selected issue, stop; do not push or open a pull request, and summarize your findings with a link to the existing pull request.

Make one cohesive, reviewable change. If no meaningful mismatch can be verified, make no changes and summarize what you checked.`,
		},
		{
			ID: "ci-warning-cleanup", Name: "Resolve one recurring CI warning",
			Description: "Fix one actionable warning at its source while preserving the checks that protect users.",
			Priority:    55, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval: 7 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Recent CI logs", "Tools to reproduce the affected CI step"},
			Prompt: `Resolve ONE recurring, actionable warning in this repository's CI.

Inspect recent CI logs and confirm the same warning occurs across multiple runs. For GitHub Actions, use gh run list --limit 10, then gh run view RUN_ID --log or gh run view RUN_ID --job JOB_ID --log; use equivalent run or job logs for other CI systems. Prefer deprecations, compatibility warnings, or configuration problems that threaten future builds or obscure useful failures. Trace the warning to its source and reproduce it with the affected command. Skip transient infrastructure noise; if logs or a reproduction are unavailable, report that limitation instead of guessing.

Check open issues and pull requests, including drafts, before choosing work; skip anything already being addressed. Make the smallest cohesive fix, then rerun the affected CI step and relevant tests or linters. Inspect the command and scripts before running them. Do not publish, deploy, or release anything as a side effect of reproducing or verifying the warning; if the affected step cannot run without those side effects, stop and summarize the limitation. Preserve assertions, failure detection, and warning visibility; do not silence the warning by disabling checks, filtering output, or broadly suppressing diagnostics. Immediately before pushing or opening a pull request, list open pull requests again, including drafts. If an open pull request already addresses the selected issue, stop; do not push or open a pull request, and summarize your findings with a link to the existing pull request.

Make one cohesive, reviewable change. If no worthwhile warning can be reproduced and fixed, make no changes and explain what you examined.`,
		},
		{
			ID: "release-readiness", Name: "Report one release-readiness blocker",
			Description: "Give maintainers one evidence-backed release blocker and a concrete next step, without changing the repository.",
			Priority:    50, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval:  7 * 24 * time.Hour,
			Requirements: []string{"Repository access", "Release instructions and relevant CI or build evidence", "Execution profile without repository-writing or publishing lifecycle hooks"},
			Prompt: `Produce a read-only report of ONE consequential blocker to this repository's next release.

Read the release instructions and inspect relevant evidence such as required checks, version consistency, release notes, packaging, or migration compatibility. Use only non-publishing verification commands; inspect release scripts before running anything and skip commands with release side effects. Prioritize a problem that would prevent installation, break an upgrade, or leave a required release check failing. Check open issues and pull requests, including drafts, and cite existing work rather than proposing duplicate work.

Choose one verified blocker and report its affected release path, supporting file references or command output, user impact, and the smallest concrete next step with a verification command. Distinguish unavailable checks from confirmed failures. If no blocker is verified, summarize the checks performed and any evidence gaps; do not claim the release is ready based on incomplete evidence.

Return the report in the run summary. Make no repository changes, commits, pushes, or pull requests. Do not tag, publish, deploy, or release anything.`,
		},
		{
			ID: "accessibility-repair", Name: "Repair one accessibility barrier",
			Description: "Remove one verified obstacle to completing a user task with the project's interface.",
			Priority:    65, Type: domain.Recurring, DispatchTier: domain.DispatchBehind,
			MinInterval: 7 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Runnable user interface", "Relevant accessibility inspection and test tools"},
			Prompt: `Find and repair ONE verified accessibility barrier in this repository's user interface.

Choose an important user journey in the project's web, desktop, mobile, or terminal interface. Reproduce a concrete obstacle involving keyboard navigation, focus, accessible names or semantics, screen-reader output, or contrast using tools appropriate to that interface. Prioritize something that blocks or confuses completing the task, and record the affected interaction before changing code. Automated audit findings are leads; confirm the user impact. If there is no runnable interface or the necessary tools are unavailable, make no changes and explain the limitation.

Check open issues and pull requests, including drafts, before choosing work; skip anything already being addressed. Apply the smallest cohesive repair using the project's existing UI conventions. Repeat the affected interaction, verify nearby behavior still works, and add or update a focused regression test where practical. Run the affected suite. Do not turn this into a visual redesign or claim a full accessibility audit. Immediately before pushing or opening a pull request, list open pull requests again, including drafts. If an open pull request already addresses the selected issue, stop; do not push or open a pull request, and summarize your findings with a link to the existing pull request.

Make one cohesive, reviewable change. If no meaningful barrier can be verified, make no changes and summarize what you checked.`,
		},
	}
}
