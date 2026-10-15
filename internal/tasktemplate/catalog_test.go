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
		// Maintenance templates use built-in instructions, so their prompt is
		// optional; every other template needs its own.
		promptRequired := template.Kind != domain.KindPRMaintenance
		if template.ID == "" || template.Name == "" || (promptRequired && strings.TrimSpace(template.Prompt) == "") {
			t.Errorf("incomplete template %#v", template.ID)
		}
		if template.Kind != domain.KindStandard && template.Kind != domain.KindPRMaintenance {
			t.Errorf("template %q has kind %q", template.ID, template.Kind)
		}
		if seen[template.ID] {
			t.Errorf("duplicate template ID %q", template.ID)
		}
		seen[template.ID] = true
	}
}

// Every template that changes code or docs starts from fetched origin and, if
// its own rules lead to a pull request, delivers it ready for review with a
// run trailer. The shared text must not displace the template's own
// no-change and duplicate guards.
func TestCodeTemplatesShareSafeStartAndConditionalDelivery(t *testing.T) {
	standard := 0
	for _, template := range tasktemplate.Catalog() {
		if template.Kind != domain.KindStandard {
			continue
		}
		standard++
		prompt := template.Prompt
		if !strings.HasPrefix(prompt, tasktemplate.SafeStart) {
			t.Errorf("%s: prompt does not start with the shared safe-start text", template.ID)
		}
		if !strings.HasSuffix(prompt, tasktemplate.Delivery) {
			t.Errorf("%s: prompt does not end with the shared delivery rule", template.ID)
		}
		for _, required := range []string{"git fetch origin", "origin/<base>", "Never reset", "if, and only if", "ready for review (not as a draft)", "Redline-Run: $REDLINE_RUN_ID", "Never merge"} {
			if !strings.Contains(prompt, required) {
				t.Errorf("%s: prompt is missing %q", template.ID, required)
			}
		}
		if strings.Count(prompt, tasktemplate.Delivery) != 1 || strings.Count(prompt, tasktemplate.SafeStart) != 1 {
			t.Errorf("%s: shared text is duplicated", template.ID)
		}
		if !strings.Contains(prompt, "make no changes") {
			t.Errorf("%s: lost its no-change requirement", template.ID)
		}
	}
	if standard < 6 {
		t.Fatalf("only %d standard templates checked", standard)
	}
}

func TestPRMaintenanceTemplateUsesBuiltInInstructions(t *testing.T) {
	for _, template := range tasktemplate.Catalog() {
		if template.ID != "pr-maintenance" {
			continue
		}
		if template.Kind != domain.KindPRMaintenance || template.Type != domain.Recurring ||
			template.DispatchTier != domain.DispatchBehind || template.MinInterval != 12*time.Hour ||
			template.Priority != 65 || template.Prompt != "" ||
			!strings.Contains(template.Description, "built into Redline") {
			t.Fatalf("pr-maintenance template = %#v", template)
		}
		return
	}
	t.Fatal("pr-maintenance template is missing")
}
