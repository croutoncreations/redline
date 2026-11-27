package greenlane

import (
	"fmt"
	"sort"
	"strings"
)

// checkMainDrift is the merge-queue counterpart of requiring a current base.
// With a queue, the review covers the PR against its own (possibly older)
// base, and GitHub's queue runs CI on the PR combined with the latest base
// before merging. That leaves one gap worth closing cheaply: if main changed
// a denied path (auth, credentials, workflows, release, the lane itself) since
// the PR's base, the reviewer never judged the PR against that code, so a
// branch update and fresh review are required. Unknown drift fails closed.
func (e *Evaluator) checkMainDrift(pr PRInfo) RuleResult {
	r := RuleResult{Name: "Main Drift"}
	if !pr.MainDriftKnown {
		r.Inconclusive = true
		r.Message = "Could not list what main changed since this PR's base"
		return r
	}
	var sensitive []string
	for _, p := range pr.MainDrift {
		if matchesAnyPattern(p, e.config.DeniedPaths) {
			sensitive = append(sensitive, p)
		}
	}
	if len(sensitive) > 0 {
		sort.Strings(sensitive)
		if len(sensitive) > 5 {
			sensitive = append(sensitive[:5], fmt.Sprintf("and %d more", len(sensitive)-5))
		}
		r.Message = "Main changed sensitive paths since this PR's base; update the branch and get a fresh review: " + strings.Join(sensitive, ", ")
		return r
	}
	r.Pass = true
	if len(pr.MainDrift) == 0 {
		r.Message = "OK: PR is current with main"
	} else {
		r.Message = fmt.Sprintf("OK: main changed %d files since this PR's base, none sensitive; the merge queue tests the combination", len(pr.MainDrift))
	}
	return r
}
