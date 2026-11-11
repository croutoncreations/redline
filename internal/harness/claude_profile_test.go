package harness_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/harness"
)

func claudeEnv(t *testing.T, profileDir string) []string {
	t.Helper()
	// A CLAUDE_CONFIG_DIR inherited from the service's own environment.
	t.Setenv("CLAUDE_CONFIG_DIR", "/inherited/should-not-leak")
	runner := &captureRunner{}
	_, err := harness.Adapter{Runner: runner}.Run(context.Background(), harness.Request{
		RunID: "run-profile", OutputDirectory: t.TempDir(),
		Task:      domain.Task{ID: "task", Prompt: "p"},
		Profile:   domain.ExecutionProfile{HarnessType: "claude-code", ClaudeConfigDir: profileDir},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, entry := range runner.command.Env {
		if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
			found = append(found, entry)
		}
		name := strings.SplitN(entry, "=", 2)[0]
		if profileDir != "" && slices.Contains([]string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN",
			"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK"}, name) {
			t.Fatalf("a profile run kept another login or route: %s", name)
		}
	}
	return found
}

func TestClaudeRunOnAProfileAccountUsesThatProfileOnly(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-not-a-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "synthetic-not-a-token")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "synthetic-not-a-token")
	t.Setenv("ANTHROPIC_BASE_URL", "http://synthetic.invalid")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	got := claudeEnv(t, "/Users/someone/.claude-cc-alpha")
	if !slices.Equal(got, []string{"CLAUDE_CONFIG_DIR=/Users/someone/.claude-cc-alpha"}) {
		t.Fatalf("CLAUDE_CONFIG_DIR entries = %v", got)
	}
}

func TestClaudeRunOnTheDefaultAccountNeverInheritsAProfile(t *testing.T) {
	if got := claudeEnv(t, ""); len(got) != 0 {
		t.Fatalf("a default-login run must not run on a profile, got %v", got)
	}
}

func TestSignedOutProfileRunNamesThatProfilesSignIn(t *testing.T) {
	result, err := harness.Adapter{Runner: claudeSignedOutRunner{}}.Run(context.Background(), harness.Request{
		RunID: "run-signed-out", OutputDirectory: t.TempDir(),
		Task:      domain.Task{ID: "task", Prompt: "p"},
		Profile:   domain.ExecutionProfile{HarnessType: "claude-code", ClaudeConfigDir: "/Users/someone/.claude-cc-alpha"},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !harness.IsAuthenticationFailure(result.Failure) || !strings.Contains(result.Failure, "CLAUDE_CONFIG_DIR=/Users/someone/.claude-cc-alpha claude auth login") {
		t.Fatalf("failure = %q", result.Failure)
	}
}
