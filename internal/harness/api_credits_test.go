package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/harness"
)

func runEnv(t *testing.T, profile domain.ExecutionProfile) []string {
	t.Helper()
	runner := &captureRunner{}
	_, err := harness.Adapter{Runner: runner}.Run(context.Background(), harness.Request{
		RunID: "run-credits", OutputDirectory: t.TempDir(),
		Task: domain.Task{ID: "task", Prompt: "p"}, Profile: profile,
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner.command.Env
}

func envValues(env []string, name string) []string {
	var values []string
	for _, entry := range env {
		if key, value, _ := strings.Cut(entry, "="); key == name {
			values = append(values, value)
		}
	}
	return values
}

// A run on an API credits account spends exactly that organization's key:
// inherited OpenAI credentials and routing are dropped first, so the run
// cannot bill another organization, or send the key to another host.
func TestCreditsRunUsesOnlyTheAccountsKeyAndOrganization(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-inherited-should-not-leak")
	t.Setenv("OPENAI_BASE_URL", "http://synthetic.invalid")
	t.Setenv("OPENAI_API_BASE", "http://synthetic.invalid")
	t.Setenv("OPENAI_ORG_ID", "org-inherited")
	t.Setenv("OPENAI_ORGANIZATION", "org-inherited")
	t.Setenv("OPENAI_PROJECT_ID", "proj-inherited")
	for _, harnessType := range []string{"pi", "command"} {
		profile := domain.ExecutionProfile{HarnessType: harnessType, HarnessCommand: "true",
			APICreditsVendor: "openai", APICreditsKey: "sk-proj-ACCOUNT", APICreditsOrganization: "org-account"}
		env := runEnv(t, profile)
		if got := envValues(env, "OPENAI_API_KEY"); len(got) != 1 || got[0] != "sk-proj-ACCOUNT" {
			t.Fatalf("%s: OPENAI_API_KEY = %v", harnessType, got)
		}
		for _, name := range []string{"OPENAI_ORG_ID", "OPENAI_ORGANIZATION"} {
			if got := envValues(env, name); len(got) != 1 || got[0] != "org-account" {
				t.Fatalf("%s: %s = %v", harnessType, name, got)
			}
		}
		for _, name := range []string{"OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_PROJECT_ID"} {
			if got := envValues(env, name); len(got) != 0 {
				t.Fatalf("%s: kept %s = %v", harnessType, name, got)
			}
		}
	}
}

// A credits run is marked so tools inside it (such as pi extensions that
// move OpenAI models to a ChatGPT subscription) keep billing the API key.
// Other runs, including subscription Codex runs, are never marked, even if
// the service inherited the marker.
func TestOnlyCreditsRunsAreMarkedAsSpendingAPICredits(t *testing.T) {
	t.Setenv("REDLINE_OPENAI_API_CREDITS", "1")
	t.Setenv("REDLINE_PROVIDER_ACCOUNT_ID", "inherited")
	credits := runEnv(t, domain.ExecutionProfile{HarnessType: "pi", ProviderAccountID: "openai-credits", APICreditsVendor: "openai", APICreditsKey: "sk-proj-ACCOUNT"})
	if got := envValues(credits, "REDLINE_OPENAI_API_CREDITS"); len(got) != 1 || got[0] != "1" {
		t.Fatalf("credits run REDLINE_OPENAI_API_CREDITS = %v", got)
	}
	if got := envValues(credits, "REDLINE_PROVIDER_ACCOUNT_ID"); len(got) != 1 || got[0] != "openai-credits" {
		t.Fatalf("credits run REDLINE_PROVIDER_ACCOUNT_ID = %v", got)
	}
	for _, profile := range []domain.ExecutionProfile{
		{HarnessType: "pi", ProviderAccountID: "claude-main"},
		{HarnessType: "codex-cli", ProviderAccountID: "codex-main"},
	} {
		env := runEnv(t, profile)
		if got := envValues(env, "REDLINE_OPENAI_API_CREDITS"); len(got) != 0 {
			t.Fatalf("%s run REDLINE_OPENAI_API_CREDITS = %v", profile.ProviderAccountID, got)
		}
		if got := envValues(env, "REDLINE_PROVIDER_ACCOUNT_ID"); len(got) != 1 || got[0] != profile.ProviderAccountID {
			t.Fatalf("%s run REDLINE_PROVIDER_ACCOUNT_ID = %v", profile.ProviderAccountID, got)
		}
	}
}

// Without an organization the run still drops any inherited one, so the
// key's own default organization is billed, as Redline measured.
func TestCreditsRunWithoutOrganizationDropsInheritedOne(t *testing.T) {
	t.Setenv("OPENAI_ORG_ID", "org-inherited")
	env := runEnv(t, domain.ExecutionProfile{HarnessType: "pi", APICreditsVendor: "openai", APICreditsKey: "sk-proj-ACCOUNT"})
	if got := envValues(env, "OPENAI_ORG_ID"); len(got) != 0 {
		t.Fatalf("OPENAI_ORG_ID = %v", got)
	}
}

// Other runs are untouched: their OpenAI environment is the user's own.
func TestNonCreditsRunKeepsItsEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-user-own")
	env := runEnv(t, domain.ExecutionProfile{HarnessType: "pi"})
	if got := envValues(env, "OPENAI_API_KEY"); len(got) != 1 || got[0] != "sk-user-own" {
		t.Fatalf("OPENAI_API_KEY = %v", got)
	}
}

// The injected key is never part of the stored or serialized profile.
func TestCreditsKeyIsNeverSerialized(t *testing.T) {
	profile := domain.ExecutionProfile{ID: "p", HarnessType: "pi", APICreditsVendor: "openai", APICreditsKey: "sk-proj-SECRET", APICreditsOrganization: "org-x"}
	encoded := mustJSON(t, profile)
	if strings.Contains(encoded, "sk-proj-SECRET") || strings.Contains(encoded, "org-x") {
		t.Fatalf("profile JSON leaks the key: %s", encoded)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// A session key supplied through the environment never reaches a run: the
// service captures and removes it before starting anything, and runs strip
// it as a second line of defence.
func TestRunsNeverSeeTheSessionKeyEnvironment(t *testing.T) {
	t.Setenv("REDLINE_OPENAI_SESSION_KEY", "sess-SHOULD-NOT-LEAK")
	for _, profile := range []domain.ExecutionProfile{{HarnessType: "pi"}, {HarnessType: "command", HarnessCommand: "true"},
		{HarnessType: "pi", APICreditsVendor: "openai", APICreditsKey: "sk-proj-ACCOUNT"}} {
		if got := envValues(runEnv(t, profile), "REDLINE_OPENAI_SESSION_KEY"); len(got) != 0 {
			t.Fatalf("%s run inherited the session key", profile.HarnessType)
		}
	}
}
