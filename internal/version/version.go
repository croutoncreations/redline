// Package version exposes build metadata injected at release time. Release
// builds set these through -ldflags; source builds report "dev".
package version

import "fmt"

var (
	// Version is the semantic version without a leading "v", or "dev".
	Version = "dev"
	// Commit is the short git commit the binary was built from, if known.
	Commit = ""
	// Date is the build timestamp in RFC 3339, if known.
	Date = ""
)

// String renders the version with commit and date when they are available.
func String() string {
	out := Version
	if Commit != "" {
		out += " (" + Commit
		if Date != "" {
			out += ", " + Date
		}
		out += ")"
	}
	return fmt.Sprintf("redline %s", out)
}
