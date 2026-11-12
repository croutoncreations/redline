package process

import (
	"slices"
	"strings"
)

// cursorCredentialVariables would make the Cursor CLI authenticate as
// something other than its own `agent login`: a key for another account, or
// another endpoint. Redline schedules against the logged-in account's pools,
// so a run billed elsewhere would spend what Redline cannot see.
var cursorCredentialVariables = []string{"CURSOR_API_KEY", "CURSOR_AUTH_TOKEN", "CURSOR_API_ENDPOINT"}

// WithoutCursorCredentials returns environment without the Cursor credential
// variables. Used for every Cursor CLI invocation: runs and discovery.
func WithoutCursorCredentials(environment []string) []string {
	kept := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(cursorCredentialVariables, name) {
			kept = append(kept, entry)
		}
	}
	return kept
}
