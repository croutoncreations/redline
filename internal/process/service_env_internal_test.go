package process

import (
	"slices"
	"testing"
)

// On Windows a mixed-case run marker inherited by the service is the same
// variable as REDLINE_OPENAI_API_CREDITS, so it never reaches a run.
func TestServiceEnvironmentDropsMixedCaseMarkersOnWindows(t *testing.T) {
	inherited := []string{"redline_openai_api_credits=1", "Redline_OpenAI_Session_Key=sess-X", "REDLINE_KEEP_ME=yes"}
	if got := serviceEnvironment("windows", inherited); !slices.Equal(got, []string{"REDLINE_KEEP_ME=yes"}) {
		t.Fatalf("windows kept %v", got)
	}
	if got := serviceEnvironment("darwin", inherited); !slices.Equal(got, inherited) {
		t.Fatalf("darwin dropped %v", got)
	}
}
