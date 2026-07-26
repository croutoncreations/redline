package tasktemplate

import (
	"time"

	"github.com/jfox/redline/internal/domain"
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

Use issues, static analysis, failing tests, and suspicious error or boundary handling as leads. Reproduce the bug before changing code, ideally with a failing test. Fix the root cause minimally, rerun the reproduction, and run the affected test suite. Do not treat a design preference as a bug.

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
	}
}
