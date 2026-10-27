package tasktemplate_test

import (
	"strings"
	"testing"

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
