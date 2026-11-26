package process_test

import (
	"slices"
	"testing"

	redprocess "github.com/croutoncreations/redline/internal/process"
)

// Anything Redline starts (agent runs, prepare and finalize hooks) must not
// inherit the platform session key or a run marker the service was started
// with: a hook that starts pi with REDLINE_OPENAI_API_CREDITS=1 would bill
// whatever OPENAI_API_KEY it has instead of a subscription.
func TestServiceEnvironmentDropsSecretsAndRunMarkers(t *testing.T) {
	t.Setenv("REDLINE_OPENAI_SESSION_KEY", "sess-SYNTHETIC")
	t.Setenv("REDLINE_OPENAI_API_CREDITS", "1")
	t.Setenv("REDLINE_PROVIDER_ACCOUNT_ID", "inherited")
	t.Setenv("REDLINE_KEEP_ME", "yes")
	env := redprocess.ServiceEnvironment()
	for _, name := range []string{"REDLINE_OPENAI_SESSION_KEY", "REDLINE_OPENAI_API_CREDITS", "REDLINE_PROVIDER_ACCOUNT_ID"} {
		if slices.ContainsFunc(env, func(e string) bool { return len(e) > len(name) && e[:len(name)+1] == name+"=" }) {
			t.Fatalf("kept %s", name)
		}
	}
	if !slices.Contains(env, "REDLINE_KEEP_ME=yes") {
		t.Fatal("dropped an unrelated variable")
	}
}
