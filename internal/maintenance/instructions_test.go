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
	// A PR marked ready on an unchanged head must still be revisited when
	// bot reviews or CI failures arrive later.
	for _, required := range []string{"Skip a PR only when BOTH are true", "CHANGES_REQUESTED review",
		"failing check run, is newer than that comment", "Never post the same note twice"} {
		if !strings.Contains(text, required) {
			t.Errorf("instructions missing revisit rule %q", required)
		}
	}
	if strings.Contains(text, "Skip a PR whose head SHA has not changed since") {
		t.Error("instructions still skip any PR whose head SHA is unchanged")
	}
	// Takeover detection uses commit order and pr_maintenance runs of this
	// repository, not dates and arbitrary runs.
	for _, required := range []string{"commit ORDER, never dates", "AFTER that one", `kind "pr_maintenance"`, `repository is "$REDLINE_REPO"`} {
		if !strings.Contains(text, required) {
			t.Errorf("instructions missing takeover rule %q", required)
		}
	}
	if strings.Contains(text, "dated after the original run") {
		t.Error("takeover check still compares commit dates")
	}
	if !strings.Contains(maintenance.Instructions(""), "version "+maintenance.Version) || maintenance.Version != "2026-10-15.3" {
		t.Errorf("version = %q", maintenance.Version)
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

// Unattended maintenance must not install anything; a test that cannot run
// with what is present is a blocker for a human.
func TestInstructionsForbidInstallingDependencies(t *testing.T) {
	text := maintenance.Instructions("")
	for _, forbidden := range []string{"documented dependency install", "You may run the repository's"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("instructions still permit installing: %q", forbidden)
		}
	}
	for _, required := range []string{
		"Use only the dependencies and tools already present", "Never install anything",
		"no packages,\n     dependencies, or system tools", "report it as a blocker for a human",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("instructions missing no-install rule %q", required)
		}
	}
}
