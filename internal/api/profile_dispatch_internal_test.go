package api

import (
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/domain"
)

// A run on a profile account starts Claude Code with that profile; a run on
// any other account never carries one, even if the stored value had one.
func TestRunsCarryTheirAccountsClaudeProfile(t *testing.T) {
	stored := domain.ExecutionProfile{ID: "p", HarnessType: "claude-code", ClaudeConfigDir: "/stale"}
	got := profileForAccount(stored, config.Provider{Provider: "claude", ClaudeConfigDir: "/Users/x/.claude-cc-alpha/"})
	if got.ClaudeConfigDir != "/Users/x/.claude-cc-alpha" {
		t.Fatalf("profile account run dir = %q", got.ClaudeConfigDir)
	}
	for _, account := range []config.Provider{{Provider: "claude"}, {Provider: "codex", ClaudeConfigDir: "/x"}} {
		if got := profileForAccount(stored, account); got.ClaudeConfigDir != "" {
			t.Fatalf("%+v: run dir = %q, want none", account, got.ClaudeConfigDir)
		}
	}
}

// Only Claude Code runs on a profile account's login; anything else would
// spend another login while being scheduled against this one.
func TestProfileAccountsAcceptOnlyClaudeCodeRuns(t *testing.T) {
	account := config.Provider{Provider: "claude", ClaudeConfigDir: "/Users/x/.claude-cc-alpha"}
	if err := profileHarnessFitsAccount(domain.ExecutionProfile{HarnessType: "claude-code"}, account); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"command", "pi", "codex-cli", "hermes"} {
		err := profileHarnessFitsAccount(domain.ExecutionProfile{ProviderAccountID: "claude-alpha", HarnessType: harness}, account)
		if err == nil || !strings.Contains(err.Error(), "must use the claude-code harness") {
			t.Fatalf("%s: err = %v", harness, err)
		}
	}
	if err := profileHarnessFitsAccount(domain.ExecutionProfile{HarnessType: "command"}, config.Provider{Provider: "claude"}); err != nil {
		t.Fatalf("a default-login account may use any harness: %v", err)
	}
}
