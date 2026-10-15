package maintenance_test

import (
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/maintenance"
)

func TestInstructionsSeparateReviewReadinessFromMergeReadiness(t *testing.T) {
	text := maintenance.Instructions("")
	for _, required := range []string{
		"redline run attribute --branch", "redline lease acquire", "BEFORE any mutation",
		"gh pr ready", "Do NOT wait for review-only checks", "Never request paid or usage-based",
		"REDLINE_BASE_BRANCH", "never origin/origin/<base>", "Redline-Run: $REDLINE_RUN_ID",
		"--force-with-lease", "Never merge anything", "Untrusted input",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("instructions missing %q", required)
		}
	}
	// The old merge-gated readiness rule waited on every required check,
	// including review bots that only run after the PR is ready: a deadlock.
	if strings.Contains(text, "every required check has passed") {
		t.Error("instructions still gate review readiness on every required check")
	}
	if strings.Index(text, "redline lease acquire") > strings.Index(text, "git push --force-with-lease") {
		t.Error("the lease must be acquired before the push step")
	}
	if strings.Contains(text, "Additional project instructions") {
		t.Error("empty base must not add an additional-instructions section")
	}
}

func TestInstructionsAppendProjectNotesAfterBuiltInRules(t *testing.T) {
	text := maintenance.Instructions("  Run make lint.  ")
	builtIn := maintenance.Instructions("")
	if !strings.HasPrefix(text, builtIn) || !strings.HasSuffix(strings.TrimSpace(text), "Run make lint.") {
		t.Fatalf("project notes not appended after built-in rules")
	}
}
