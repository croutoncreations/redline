package greenlane

import (
	"encoding/json"
	"path"
)

// A branch update merges main into a PR without changing what the PR itself
// changes. Re-running Codex and CodeRabbit after every such update is the
// main cost of keeping branches current, so a completed review of an earlier
// head also counts for the current head when the two heads make exactly the
// same change: the same paths, each with the same resulting file (blob and
// mode), on top of unchanged base versions of those paths.
//
// The collector proves this from git trees, which GitHub addresses by content
// hash, and records the earlier heads in PRInfo.ReviewEquivalentHeads. CI is
// still required on the exact current head, so interactions between the PR
// and newer main are still tested; only the review is carried over.

// TreeEntry is one path in a recursive git tree.
type TreeEntry struct {
	Mode string
	Type string
	SHA  string
}

// Tree maps a path to its entry. Directory entries are omitted.
type Tree map[string]TreeEntry

// SameChange reports whether head's change against headBase is identical to
// reviewed's change against reviewedBase. Any doubt returns false.
func SameChange(head, headBase, reviewed, reviewedBase Tree) bool {
	if head == nil || headBase == nil || reviewed == nil || reviewedBase == nil {
		return false
	}
	changed := func(after, before Tree) map[string]bool {
		paths := map[string]bool{}
		for p, e := range after {
			if old, ok := before[p]; !ok || old != e {
				paths[p] = true
			}
		}
		for p := range before {
			if _, ok := after[p]; !ok {
				paths[p] = true
			}
		}
		return paths
	}
	headPaths := changed(head, headBase)
	reviewedPaths := changed(reviewed, reviewedBase)
	if len(headPaths) == 0 || len(headPaths) != len(reviewedPaths) {
		return false
	}
	same := func(a, b Tree, p string) bool {
		x, okA := a[p]
		y, okB := b[p]
		return okA == okB && x == y
	}
	for p := range reviewedPaths {
		if !headPaths[p] || !same(head, reviewed, p) || !same(headBase, reviewedBase, p) {
			return false
		}
	}
	return true
}

// BaseDriftAllowed limits how far main may have moved under a carried-over
// review. Same-change equality covers the PR's own files, but a review also
// judged them against the code around them. Carryover is refused when main
// changed, since the reviewed base, any denied path (auth, credentials,
// workflows, release, the lane itself) or any file in a directory the PR
// touches, where same-package helpers live. Changes elsewhere are left to CI.
func BaseDriftAllowed(head, headBase, reviewedBase Tree, denied []string) bool {
	if headBase == nil || reviewedBase == nil {
		return false
	}
	dirs := map[string]bool{}
	for p, e := range head {
		if old, ok := headBase[p]; !ok || old != e {
			dirs[path.Dir(p)] = true
		}
	}
	for p := range headBase {
		if _, ok := head[p]; !ok {
			dirs[path.Dir(p)] = true
		}
	}
	drifted := func(p string) bool {
		if matchesAnyPattern(p, denied) {
			return true
		}
		return dirs[path.Dir(p)]
	}
	for p, e := range headBase {
		if old, ok := reviewedBase[p]; (!ok || old != e) && drifted(p) {
			return false
		}
	}
	for p := range reviewedBase {
		if _, ok := headBase[p]; !ok && drifted(p) {
			return false
		}
	}
	return true
}

// reviewCovers reports whether a review of commit sha covers the PR's current
// head, either exactly or through a proven-identical earlier head.
func reviewCovers(pr PRInfo, sha string) bool {
	if sha == "" || !fullHead.MatchString(sha) {
		return false
	}
	if sha == pr.HeadSHA {
		return true
	}
	for _, equivalent := range pr.ReviewEquivalentHeads {
		if equivalent == sha {
			return true
		}
	}
	return false
}

// ReviewedCommits lists the commits a trusted summary reports as reviewed,
// so the collector knows which earlier heads to test for equivalence. It is
// only discovery: completion is still parsed strictly in
// completedAutomatedReview.
func ReviewedCommits(c AutomatedReviewComment) []string {
	if !TrustedAutomatedSummary(c) {
		return nil
	}
	var out []string
	if c.AppID == 347564 {
		var state struct {
			Source  string `json:"sourceCommitId"`
			Covered string `json:"coveredCommitId"`
			Kind    string `json:"kind"`
		}
		if summaryJSON(summaryPayload(c.Body, "<!-- final_review_risk_coverage:"), &state) == nil && state.Source == state.Covered {
			out = append(out, state.Source)
		}
		return out
	}
	var state struct {
		Code *struct {
			Commit string `json:"commit_sha"`
		} `json:"code_review"`
		Security *struct {
			Commit string `json:"commit_sha"`
		} `json:"security_review"`
	}
	if json.Unmarshal(summaryPayload(c.Body, "<!-- codex-review-state:v1 "), &state) == nil {
		if state.Code != nil {
			out = append(out, state.Code.Commit)
		}
		if state.Security != nil {
			out = append(out, state.Security.Commit)
		}
	}
	return out
}
