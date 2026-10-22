package greenlane

import (
	"fmt"
	"sort"
	"strings"
)

func (e *Evaluator) checkPRState(pr PRInfo) RuleResult {
	r := RuleResult{Name: "PR State"}
	switch {
	case !pr.SnapshotConsistent:
		r.Inconclusive = true
		r.Message = "PR snapshot is missing or changed during evaluation: " + pr.SnapshotMessage
	case pr.State != "open":
		r.Message = "PR is not open"
	case pr.Draft:
		r.Message = "PR is a draft"
	case !pr.Mergeable:
		r.Message = "PR mergeability is unknown or conflicted"
	case pr.Repository == "" || pr.HeadSHA == "" || pr.BaseSHA == "":
		r.Message = "Missing repository or head/base SHA"
	default:
		for _, branch := range e.config.AllowedBaseBranches {
			if branch == pr.BaseBranch {
				r.Pass = true
				r.Message = "OK: open, ready and mergeable on allowed base " + branch
				return r
			}
		}
		r.Message = "Base branch is not allowed: " + pr.BaseBranch
	}
	return r
}

// effectiveReviews ignores later comments: a comment cannot revoke an
// approval or clear requested changes. A later decisive review/dismissal can.
// IDs break equal-timestamp ties and avoid relying on REST response ordering.
func effectiveReviews(reviews []Review) map[string]Review {
	latest := map[string]Review{}
	for _, r := range reviews {
		if r.User == "" || (r.State != "APPROVED" && r.State != "CHANGES_REQUESTED" && r.State != "DISMISSED") {
			continue
		}
		old, ok := latest[r.User]
		if !ok || r.SubmittedAt.After(old.SubmittedAt) || (r.SubmittedAt.Equal(old.SubmittedAt) && r.ID > old.ID) {
			latest[r.User] = r
		}
	}
	return latest
}

func (e *Evaluator) checkReviewReadiness(pr PRInfo) RuleResult {
	r := RuleResult{Name: "Review Readiness"}
	if !pr.ReviewThreadsComplete {
		r.Inconclusive = true
		r.Message = "Review threads were not completely fetched"
		return r
	}
	var unresolved []string
	for _, thread := range pr.ReviewThreads {
		if !thread.Resolved {
			unresolved = append(unresolved, thread.ID)
		}
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		r.Message = "Unresolved review threads (outdated threads also block): " + strings.Join(unresolved, ", ")
		return r
	}
	approved := false
	var requests []string
	for user, review := range effectiveReviews(pr.Reviews) {
		if review.State == "CHANGES_REQUESTED" {
			requests = append(requests, user)
		}
		if review.State == "APPROVED" && review.CommitID == pr.HeadSHA && user != pr.Author {
			approved = true
		}
	}
	if len(requests) > 0 {
		sort.Strings(requests)
		r.Message = "Outstanding changes requested, including earlier heads: " + strings.Join(requests, ", ")
		return r
	}
	if !approved {
		r.Message = "No effective independent approval on current head SHA"
		return r
	}
	r.Pass = true
	r.Message = fmt.Sprintf("OK: all %d review threads resolved and an effective approval exists on current head", len(pr.ReviewThreads))
	return r
}
