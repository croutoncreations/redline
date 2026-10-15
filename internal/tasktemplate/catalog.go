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
	Kind              domain.TaskKind     `json:"kind"`
	Description       string              `json:"description"`
	Prompt            string              `json:"prompt"`
	Priority          int                 `json:"priority"`
	Type              domain.TaskType     `json:"type"`
	DispatchTier      domain.DispatchTier `json:"dispatch_tier"`
	MinInterval       time.Duration       `json:"min_interval"`
	RequireRepoChange bool                `json:"require_repo_change"`
	Requirements      []string            `json:"requirements,omitempty"`
}

// SafeStart is shared by every template that works on code or docs. Redline
// creates git-worktree and DevX workspaces from the freshly fetched
// origin/<base>; the agent verifies that rather than trusting a local branch.
// In an existing-directory profile the same text makes the agent fetch in
// place, which only updates remote-tracking refs, and stop if that checkout
// does not contain origin/<base>. A repository without an origin remote is
// never a reason to stop: Redline prepared it from the local base.
const SafeStart = `Start from current origin. First check whether this repository has an origin remote (git remote get-url origin). If it has one: run git fetch origin, then confirm this workspace's HEAD contains origin/<base>, where <base> is $REDLINE_BASE_BRANCH without any "origin/" prefix (or origin's default branch when it is empty). If the fetch fails or HEAD does not contain origin/<base>, stop and report instead of working from a stale base. If there is no origin remote, continue from the local base Redline prepared and say so in your report. Never reset, force-checkout, clean, or otherwise destructively sync this or any other checkout.`

// Delivery is shared by every template that works on code or docs. It only
// governs how a pull request is delivered; it never authorizes one that the
// template's own rules forbid (no change found, duplicate work, or failed
// verification all still mean no pull request).
const Delivery = `Delivery: if, and only if, the rules above lead you to open a pull request, open it ready for review (not as a draft) once the repository's local tests and linters pass. Add a "Redline-Run: $REDLINE_RUN_ID" trailer to every commit you make. Never merge, enable auto-merge, or approve anything.`

// withDelivery appends the shared start and delivery rules to a template
// prompt so the wording lives in one place.
func withDelivery(prompt string) string {
	return SafeStart + "\n\n" + prompt + "\n\n" + Delivery
}

func Catalog() []Template {
	return []Template{
		{
			ID: "bug-hunt", Name: "Find and fix one reproducible bug",
			Description: "Reproduce one real defect, fix its root cause, and verify the affected suite.",
			Kind:        domain.KindStandard, Priority: 80, Type: domain.Recurring, DispatchTier: domain.DispatchBehind,
			MinInterval: 3 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Test and static-analysis tools"},
			Prompt: withDelivery(`Find ONE real, demonstrable bug in this repository and fix it.

Before choosing a bug, list this repository's open pull requests, including drafts (for example, gh pr list --state open --limit 100). Skip any bug whose root cause is already being fixed by an open pull request, even if it was reported through a different symptom, and pick a different one.

Use issues, static analysis, failing tests, and suspicious error or boundary handling as leads. Reproduce the bug before changing code, ideally with a failing test. Fix the root cause minimally, rerun the reproduction, and run the affected test suite. Do not treat a design preference as a bug.

Immediately before pushing or opening a pull request, list open pull requests again. If one now addresses the same root cause, do not push; summarize your findings instead.

Make one cohesive, reviewable change. If no bug can be reproduced, make no changes and summarize what you ruled out.`),
		},
		{
			ID: "test-gap", Name: "Close one high-risk test gap",
			Description: "Add focused tests for an important untested failure path or boundary.",
			Kind:        domain.KindStandard, Priority: 70, Type: domain.Recurring, DispatchTier: domain.DispatchBehind,
			MinInterval: 3 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Test runner"},
			Prompt: withDelivery(`Find and close ONE meaningful gap in this repository's test coverage.

Measure coverage when tooling exists, then rank gaps by risk: persistence, concurrency, parsing, security checks, failure paths, and boundary conditions outrank trivial getters. Confirm the behavior is genuinely untested, add focused deterministic tests following repository conventions, and run the affected package's full suite.

Make one cohesive, reviewable change. If no worthwhile gap exists, make no changes and explain what you examined.`),
		},
		{
			ID: "quickstart", Name: "Verify the quickstart from scratch",
			Description: "Follow the documented setup as a newcomer and repair one confirmed break or ambiguity.",
			Kind:        domain.KindStandard, Priority: 50, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval: 14 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Ability to build in a temporary directory"},
			Prompt: withDelivery(`Test this repository's quickstart exactly as a newcomer would.

Use a clean temporary checkout or export. Follow the documented install, configure, build, run, and test steps literally. Record failures, missing prerequisites, and ambiguous instructions. Fix ONE cohesive confirmed problem in the documentation, setup scripts, or defaults, then repeat the corrected path.

Do not invent credentials or document aspirational behavior. If the quickstart works, make no changes and report what was verified.`),
		},
		{
			ID: "dependency-update", Name: "Apply a safe dependency update",
			Description: "Take a bounded patch/minor update after reviewing compatibility and advisories.",
			Kind:        domain.KindStandard, Priority: 60, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval:  7 * 24 * time.Hour,
			Requirements: []string{"Repository access", "Package-manager network access"},
			Prompt: withDelivery(`Apply ONE safe, cohesive dependency update to this repository.

Enumerate outdated direct dependencies. Prefer a patch or minor update with a correctness, security, or tooling benefit. Read its release notes before updating, regenerate lockfiles, and run the relevant tests, build, and linters. Do not take unrelated major upgrades or bundle broad cleanup.

If no update is worthwhile or verification fails, make no changes and explain why.`),
		},
		{
			ID: "error-clarity", Name: "Improve one confusing error path",
			Description: "Make one failure actionable without leaking secrets or creating log noise.",
			Kind:        domain.KindStandard, Priority: 45, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval: 14 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Test runner"},
			Prompt: withDelivery(`Improve ONE confusing error or logging path in this repository.

Choose a cohesive path where a failure is swallowed, lacks actionable context, crashes unnecessarily, duplicates noise, or risks leaking a secret. Trigger the failure first and record the current behavior. Improve it using the repository's existing error conventions, include the operation and safe relevant values, and add or update a focused test.

Run the affected suite. If the audited area is already clear and safe, make no changes and report what you checked.`),
		},
		{
			ID: "dead-code", Name: "Remove one verified dead-code cluster",
			Description: "Delete one cohesive unused code path after checking indirect and public references.",
			Kind:        domain.KindStandard, Priority: 45, Type: domain.Recurring, DispatchTier: domain.DispatchWellBehind,
			MinInterval: 14 * 24 * time.Hour, RequireRepoChange: true,
			Requirements: []string{"Repository access", "Build and test tools"},
			Prompt: withDelivery(`Find and remove ONE cohesive cluster of verified dead code.

Use the language's analysis tools and repository-wide search, then manually check configuration, reflection, templates, docs, build scripts, and public API obligations before deleting anything. Remove the dead symbol, its tests, and stale documentation completely. Build, lint, and run the relevant suite afterward.

Do not perform a scattershot cleanup. If nothing is safely removable, make no changes and explain what you checked.`),
		},
		{
			ID: "pr-maintenance", Name: "Maintain Redline pull requests",
			Description: "Keeps this project's Redline-created pull requests mergeable: resolves conflicts, fixes the PR's own CI failures and valid review feedback, and marks them ready for review. The instructions are built into Redline, so the prompt is optional and only adds project-specific notes. Never merges.",
			Kind:        domain.KindPRMaintenance, Priority: 65, Type: domain.Recurring, DispatchTier: domain.DispatchBehind,
			MinInterval:  12 * time.Hour,
			Requirements: []string{"Git worktree or DevX profile with a local agent harness", "GitHub CLI signed in", "Repository push access"},
		},
	}
}
