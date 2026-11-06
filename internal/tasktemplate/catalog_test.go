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

func TestMaintenanceTemplatesHaveBoundedSchedulingDefaults(t *testing.T) {
	catalog := map[string]tasktemplate.Template{}
	for _, template := range tasktemplate.Catalog() {
		catalog[template.ID] = template
	}
	for _, tc := range []struct {
		id                string
		interval          time.Duration
		tier              domain.DispatchTier
		requireRepoChange bool
	}{
		{"documentation-drift", 14 * 24 * time.Hour, domain.DispatchWellBehind, true},
		{"ci-warning-cleanup", 7 * 24 * time.Hour, domain.DispatchWellBehind, true},
		{"release-readiness", 7 * 24 * time.Hour, domain.DispatchWellBehind, false},
		{"accessibility-repair", 7 * 24 * time.Hour, domain.DispatchBehind, true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			template, ok := catalog[tc.id]
			if !ok {
				t.Fatal("template missing")
			}
			if template.Type != domain.Recurring || template.DispatchTier != tc.tier ||
				template.MinInterval != tc.interval || template.RequireRepoChange != tc.requireRepoChange {
				t.Fatalf("unexpected scheduling defaults: %#v", template)
			}
			if len(template.Requirements) == 0 || template.Description == "" {
				t.Fatal("template must explain its value and prerequisites")
			}
		})
	}
}

func TestReleaseReadinessStaysReportOnly(t *testing.T) {
	for _, template := range tasktemplate.Catalog() {
		if template.ID != "release-readiness" {
			continue
		}
		// Release evidence can change without a repository commit. Keep the
		// report eligible and forbid release side effects in its instructions.
		if template.RequireRepoChange {
			t.Fatal("release report must not require repository changes")
		}
		for _, required := range []string{
			"ONE consequential blocker",
			"Use only non-publishing verification commands",
			"Return the report in the run summary",
			"Make no repository changes, commits, pushes, or pull requests",
			"Do not tag, publish, deploy, or release anything",
			"Distinguish unavailable checks from confirmed failures",
		} {
			if !strings.Contains(template.Prompt, required) {
				t.Errorf("release-readiness prompt is missing boundary %q", required)
			}
		}
		return
	}
	t.Fatal("release-readiness template missing")
}
