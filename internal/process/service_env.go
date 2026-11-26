package process

import (
	"os"
	"slices"
	"strings"
)

// serviceOnlyVariables are never passed to anything Redline starts. The
// platform session key carries full dashboard access; the run markers are
// Redline's to set per run, so an inherited copy must never mislabel one.
var serviceOnlyVariables = []string{"REDLINE_OPENAI_SESSION_KEY", "REDLINE_OPENAI_API_CREDITS", "REDLINE_PROVIDER_ACCOUNT_ID"}

// ServiceEnvironment is the service's environment for an agent run or a
// workspace hook, without the variables only Redline may set.
func ServiceEnvironment() []string {
	environment := os.Environ()
	kept := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(serviceOnlyVariables, name) {
			kept = append(kept, entry)
		}
	}
	return kept
}
