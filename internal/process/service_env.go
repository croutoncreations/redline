package process

import (
	"os"
	"runtime"
	"slices"
	"strings"
)

// serviceOnlyVariables are never passed to anything Redline starts. The
// platform session key carries full dashboard access; the run markers are
// Redline's to set per run, so an inherited copy must never mislabel one.
var serviceOnlyVariables = []string{"REDLINE_OPENAI_SESSION_KEY", "REDLINE_OPENAI_API_CREDITS",
	"REDLINE_ANTHROPIC_API_CREDITS", "REDLINE_PROVIDER_ACCOUNT_ID"}

// WithoutServiceOnlyVariables drops the variables only Redline may set from
// environment, ignoring case on Windows.
func WithoutServiceOnlyVariables(environment []string) []string {
	return serviceEnvironment(runtime.GOOS, environment)
}

// ServiceEnvironment is the service's environment for an agent run or a
// workspace hook, without the variables only Redline may set.
func ServiceEnvironment() []string {
	return serviceEnvironment(runtime.GOOS, os.Environ())
}

func serviceEnvironment(goos string, environment []string) []string {
	return WithoutVariables(goos, environment, serviceOnlyVariables...)
}

// WithoutVariables drops every entry of environment named in names. Windows
// environment names are case-insensitive, so on goos "windows" a mixed-case
// entry such as OpenAI_Base_URL matches OPENAI_BASE_URL; elsewhere names
// match exactly.
func WithoutVariables(goos string, environment []string, names ...string) []string {
	kept := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		matches := slices.ContainsFunc(names, func(drop string) bool {
			if goos == "windows" {
				return strings.EqualFold(name, drop)
			}
			return name == drop
		})
		if !matches {
			kept = append(kept, entry)
		}
	}
	return kept
}
