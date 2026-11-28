package process

import "runtime"

// cursorCredentialVariables would make the Cursor CLI authenticate as
// something other than its own `agent login`: a key for another account, or
// another endpoint. Redline schedules against the logged-in account's pools,
// so a run billed elsewhere would spend what Redline cannot see.
var cursorCredentialVariables = []string{"CURSOR_API_KEY", "CURSOR_AUTH_TOKEN", "CURSOR_API_ENDPOINT"}

// WithoutCursorCredentials returns environment without the Cursor credential
// variables. Used for every Cursor CLI invocation: runs and discovery.
func WithoutCursorCredentials(environment []string) []string {
	return withoutCursorCredentials(runtime.GOOS, environment)
}

func withoutCursorCredentials(goos string, environment []string) []string {
	return WithoutVariables(goos, environment, cursorCredentialVariables...)
}
