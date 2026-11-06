package tasktemplate_test

import (
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"

	"github.com/croutoncreations/redline/internal/tasktemplate"
)

func TestBugHuntTemplateChecksOpenPullRequestsBeforeAndAfterFixing(t *testing.T) {
	var prompt string
	for _, template := range tasktemplate.Catalog() {
		if template.ID == "bug-hunt" {
			prompt = template.Prompt
		}
	}
	if prompt == "" {
		t.Fatal("bug-hunt template is missing")
	}
	// Unattended bug hunts opened many duplicate pull requests for one root
	// cause when this check lived only in private prompts.
	for _, required := range []string{"open pull requests, including drafts", "same root cause", "Immediately before pushing"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("bug-hunt prompt is missing %q", required)
		}
	}
	before := strings.Index(prompt, "list this repository's open pull requests")
	reproduce := strings.Index(prompt, "Reproduce the bug")
	recheck := strings.Index(prompt, "Immediately before pushing")
	if before < 0 || reproduce < 0 || recheck < 0 || before > reproduce || recheck < reproduce {
		t.Errorf("duplicate check must happen before choosing a bug and again before pushing")
	}
}

func TestEvidenceDrivenMaintenanceTemplatesPreserveVerificationBoundaries(t *testing.T) {
	catalog := map[string]tasktemplate.Template{}
	for _, template := range tasktemplate.Catalog() {
		catalog[template.ID] = template
	}
	for _, tc := range []struct {
		id         string
		interval   time.Duration
		tier       domain.DispatchTier
		boundaries []string
	}{
		{"api-contract-drift", 7 * 24 * time.Hour, domain.DispatchBehind, []string{
			"ONE consequential mismatch", "Reproduce one mismatch in a focused contract test", "Preserve supported clients",
		}},
		{"flaky-test-investigation", 7 * 24 * time.Hour, domain.DispatchBehind, []string{
			"ONE demonstrably flaky test", "Bound reproduction attempts", "Do not disable the test, add blind retries or sleeps, broadly inflate timeouts, or weaken assertions",
		}},
		{"incident-runbook", 14 * 24 * time.Hour, domain.DispatchWellBehind, []string{
			"ONE consequential gap", "incident report, postmortem, or verified failure evidence", "isolated local fixture or non-production simulation", "Do not invent an incident or operate on a live incident",
		}},
		{"build-reproducibility", 14 * 24 * time.Hour, domain.DispatchWellBehind, []string{
			"ONE verified source", "same source revision, pinned inputs, toolchain, and environment", "do not remove required signing or security properties", "rebuild twice under comparable conditions",
		}},
		{"migration-compatibility", 14 * 24 * time.Hour, domain.DispatchBehind, []string{
			"ONE verified compatibility defect", "disposable local fixtures", "Never run migrations against production or user data", "rollback only when it is explicitly supported",
		}},
		{"performance-regression", 14 * 24 * time.Hour, domain.DispatchWellBehind, []string{
			"ONE measured performance regression", "comparable known-good baseline", "record the baseline", "measurement variability", "do not add flaky wall-clock assertions",
		}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			template, ok := catalog[tc.id]
			if !ok {
				t.Fatal("template missing")
			}
			if template.Type != domain.Recurring || template.MinInterval != tc.interval || template.DispatchTier != tc.tier || !template.RequireRepoChange {
				t.Fatalf("unexpected scheduling defaults: %#v", template)
			}
			if template.Description == "" || len(template.Requirements) < 2 {
				t.Fatal("template must explain its user value and prerequisites")
			}
			for _, boundary := range append(tc.boundaries,
				"Check open issues and pull requests, including drafts, before choosing work",
				"Immediately before pushing or opening a pull request, list open pull requests again, including drafts",
				"If an open pull request already addresses the selected issue, stop; do not push or open a pull request",
				"summarize your findings with a link to the existing pull request",
				"Inspect verification commands and scripts before running them",
				"Do not publish, deploy, or release anything as a side effect of reproducing or verifying the problem",
				"do not modify production systems or data",
				"If verification cannot avoid these side effects, stop and summarize the limitation",
				"make no changes",
			) {
				if !strings.Contains(template.Prompt, boundary) {
					t.Errorf("prompt is missing boundary %q", boundary)
				}
			}
		})
	}
}

func TestCatalogTemplatesAreComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, template := range tasktemplate.Catalog() {
		if template.ID == "" || template.Name == "" || strings.TrimSpace(template.Prompt) == "" {
			t.Errorf("incomplete template %#v", template.ID)
		}
		if seen[template.ID] {
			t.Errorf("duplicate template ID %q", template.ID)
		}
		seen[template.ID] = true
	}
}
