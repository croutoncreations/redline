package greenlane

import (
	"strings"
	"testing"
)

func blob(sha string) TreeEntry { return TreeEntry{Mode: "100644", Type: "blob", SHA: sha} }

func TestSameChange(t *testing.T) {
	oldBase := Tree{"a.go": blob("a1"), "b.go": blob("b1"), "docs.md": blob("d1")}
	reviewed := Tree{"a.go": blob("a2"), "b.go": blob("b1"), "docs.md": blob("d1")}
	// main moved docs.md; the PR still changes only a.go, a1 -> a2.
	newBase := Tree{"a.go": blob("a1"), "b.go": blob("b1"), "docs.md": blob("d2")}
	head := Tree{"a.go": blob("a2"), "b.go": blob("b1"), "docs.md": blob("d2")}
	if !SameChange(head, newBase, reviewed, oldBase) {
		t.Fatal("clean branch update rejected")
	}
	clone := func(t Tree) Tree {
		out := Tree{}
		for k, v := range t {
			out[k] = v
		}
		return out
	}
	for name, tc := range map[string]struct{ head, headBase Tree }{
		"PR file changed again":                        {func() Tree { h := clone(head); h["a.go"] = blob("a3"); return h }(), newBase},
		"conflict fix: main changed the PR's file too": {func() Tree { h := clone(head); h["a.go"] = blob("a2b"); return h }(), func() Tree { b := clone(newBase); b["a.go"] = blob("a1b"); return b }()},
		"main changed the PR's file, result identical": {head, func() Tree { b := clone(newBase); b["a.go"] = blob("a1b"); return b }()},
		"extra file in PR":                             {func() Tree { h := clone(head); h["c.go"] = blob("c1"); return h }(), newBase},
		"PR now deletes a file":                        {func() Tree { h := clone(head); delete(h, "b.go"); return h }(), newBase},
		"mode change": {func() Tree {
			h := clone(head)
			h["a.go"] = TreeEntry{Mode: "100755", Type: "blob", SHA: "a2"}
			return h
		}(), newBase},
		"blob became symlink": {func() Tree {
			h := clone(head)
			h["a.go"] = TreeEntry{Mode: "120000", Type: "blob", SHA: "a2"}
			return h
		}(), newBase},
		"no change at all":           {newBase, newBase},
		"nil head tree":              {nil, newBase},
		"nil base tree":              {head, nil},
		"PR change reverted by main": {head, head},
	} {
		t.Run(name, func(t *testing.T) {
			if SameChange(tc.head, tc.headBase, reviewed, oldBase) {
				t.Fatal("accepted a different change")
			}
		})
	}
	// Neither side changes anything: nothing was reviewed, so nothing carries.
	if SameChange(newBase, newBase, oldBase, oldBase) {
		t.Fatal("empty change carried over")
	}
	if SameChange(head, newBase, reviewed, nil) || SameChange(head, newBase, nil, oldBase) {
		t.Fatal("accepted missing reviewed trees")
	}
	// Added and deleted files carry over too when identical.
	addBase := Tree{"x": blob("x1")}
	addReviewed := Tree{"x": blob("x1"), "new.go": blob("n1")}
	addNewBase := Tree{"x": blob("x2")}
	addHead := Tree{"x": blob("x2"), "new.go": blob("n1")}
	if !SameChange(addHead, addNewBase, addReviewed, addBase) {
		t.Fatal("identical added file rejected")
	}
	if !SameChange(Tree{"y": blob("y2")}, Tree{"gone.go": blob("g1"), "y": blob("y2")}, Tree{"y": blob("y1")}, Tree{"gone.go": blob("g1"), "y": blob("y1")}) {
		t.Fatal("identical deletion rejected")
	}
}

const earlierHead = "1111111111111111111111111111111111111111"

func TestReviewCarriesOverOnlyToProvenEquivalentHeads(t *testing.T) {
	codex := func(code, security string) AutomatedReviewComment {
		return AutomatedReviewComment{ID: 50, UserID: 199175422, User: "chatgpt-codex-connector[bot]", UserType: "Bot", AppID: 1144995, UpdatedAt: "2026-01-01T00:00:00Z",
			Body: "<!-- codex-pull-request-review-summary -->\n<!-- codex-review-state:v1 {\"code_review\":{\"commit_sha\":\"" + code + "\",\"has_any_findings\":false,\"has_unresolved_findings\":false,\"status\":\"completed\"},\"security_review\":{\"commit_sha\":\"" + security + "\",\"has_any_findings\":false,\"has_unresolved_findings\":false,\"status\":\"completed\"}} -->"}
	}
	rabbit := func(sha string) AutomatedReviewComment {
		c := cleanSummary()
		c.Body = strings.ReplaceAll(c.Body, reviewHead, sha)
		return c
	}
	nullSecurity := func(security string, n int) AutomatedReviewComment {
		c := nullSecuritySummary(security, n)
		c.Body = strings.Replace(c.Body, `"code_review":{"commit_sha":"`+security, `"code_review":{"commit_sha":"`+reviewHead, 1)
		return c
	}
	pr := func(equivalent []string, summary AutomatedReviewComment) PRInfo {
		p := passingPR()
		p.HeadSHA = reviewHead
		p.Reviews, p.ReviewThreads = nil, nil
		p.ReviewEquivalentHeads = equivalent
		p.AutomatedReviews = []AutomatedReviewComment{summary}
		return p
	}
	for _, tc := range []struct {
		name string
		p    PRInfo
		pass bool
	}{
		{"codex both on head", pr(nil, codex(reviewHead, reviewHead)), true},
		{"codex security on equivalent earlier head", pr([]string{earlierHead}, codex(reviewHead, earlierHead)), true},
		{"codex both on equivalent earlier head", pr([]string{earlierHead}, codex(earlierHead, earlierHead)), true},
		{"codex security on earlier head NOT proven equivalent", pr(nil, codex(reviewHead, earlierHead)), false},
		{"codex security on a different unproven head", pr([]string{earlierHead}, codex(reviewHead, strings.Repeat("2", 40))), false},
		{"coderabbit on equivalent earlier head", pr([]string{earlierHead}, rabbit(earlierHead)), true},
		{"coderabbit on unproven earlier head", pr(nil, rabbit(earlierHead)), false},
		{"null security on equivalent head with matching marker", pr([]string{earlierHead}, nullSecurity(earlierHead, 1)), true},
		{"null security on unproven head", pr(nil, nullSecurity(earlierHead, 1)), false},
		{"short sha in equivalents cannot match", pr([]string{"1111111"}, codex(reviewHead, "1111111")), false},
		{"equivalent sharing only a prefix does not match", pr([]string{"1111111" + strings.Repeat("9", 33)}, codex(reviewHead, earlierHead)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveEvaluator().checkReviewReadiness(tc.p).Pass; got != tc.pass {
				t.Fatalf("pass=%v want %v", got, tc.pass)
			}
		})
	}
	// The null-security marker must name the same reviewed commit.
	p := pr([]string{earlierHead}, nullSecurity(earlierHead, 1))
	p.AutomatedReviews[0].Body = strings.Replace(p.AutomatedReviews[0].Body, `"headSha":"`+earlierHead, `"headSha":"`+reviewHead, 1)
	if liveEvaluator().checkReviewReadiness(p).Pass {
		t.Fatal("marker for a different commit accepted")
	}
}

func TestReviewedCommitsDiscovery(t *testing.T) {
	c := cleanSummary()
	if got := ReviewedCommits(c); len(got) != 1 || got[0] != reviewHead {
		t.Fatalf("coderabbit: %v", got)
	}
	c.UserType = "User"
	if ReviewedCommits(c) != nil {
		t.Fatal("untrusted summary discovered")
	}
	n := nullSecuritySummary(earlierHead, 1)
	if got := ReviewedCommits(n); len(got) != 2 {
		t.Fatalf("codex: %v", got)
	}
}
