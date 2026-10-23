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
	var requests []string
	for user, review := range effectiveReviews(pr.Reviews) {
		if review.State == "CHANGES_REQUESTED" {
			requests = append(requests, user)
		}
	}
	// Bot COMMENTED publications can be inline replies, not completed reviews.
	// Bots must use the authenticated exact-head completion summaries;
	// ordinary issue comments and successful workflow jobs do not.
	latest := map[string]Review{}
	for _, review := range pr.Reviews {
		if review.User == "" || review.ID <= 0 || (review.State != "APPROVED" && review.State != "COMMENTED" && review.State != "CHANGES_REQUESTED" && review.State != "DISMISSED") {
			continue
		}
		user := strings.ToLower(review.User)
		old, ok := latest[user]
		if !ok || review.SubmittedAt.After(old.SubmittedAt) || (review.SubmittedAt.Equal(old.SubmittedAt) && review.ID > old.ID) {
			latest[user] = review
		}
	}
	reviewed := completedAutomatedReview(pr)
	for user, review := range latest {
		if user != strings.ToLower(pr.Author) && review.CommitID == pr.HeadSHA && (review.State == "APPROVED" || (review.State == "COMMENTED" && !strings.HasSuffix(strings.ToLower(review.User), "[bot]") && strings.TrimSpace(review.Body) != "")) {
			reviewed = true
		}
	}
	if len(requests) > 0 {
		sort.Strings(requests)
		r.Message = "Outstanding changes requested, including earlier heads: " + strings.Join(requests, ", ")
		return r
	}
	if !reviewed {
		r.Message = "No completed non-author review on current head SHA"
		return r
	}
	r.Pass = true
	r.Message = fmt.Sprintf("OK: all %d review threads resolved and a completed non-author review exists on current head", len(pr.ReviewThreads))
	return r
}

// Only explicit GitHub logins are trusted; agent-written PRs using the owner's
// account naturally share that identity. An empty list admits nobody.
func (e *Evaluator) checkAuthor(pr PRInfo) RuleResult {
	r := RuleResult{Name: "Author", Message: "PR author is not allowed: " + pr.Author}
	for _, author := range e.config.AllowedAuthors {
		if author != "" && strings.EqualFold(author, pr.Author) {
			r.Pass = true
			r.Message = "OK: allowed PR author " + pr.Author
			break
		}
	}
	return r
}
