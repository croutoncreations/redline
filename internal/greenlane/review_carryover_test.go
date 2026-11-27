package greenlane

import (
	"strings"
	"testing"
	"time"
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

// Prose a bot posted on the reviewed head must keep blocking after a branch
// update; carryover alone must not supersede it (only a fresh exact-head
// review can).
func TestCarryoverNeverClearsReviewProse(t *testing.T) {
	summary := AutomatedReviewComment{ID: 50, UserID: 199175422, User: "chatgpt-codex-connector[bot]", UserType: "Bot", AppID: 1144995, UpdatedAt: "2026-01-02T00:00:00Z",
		Body: "<!-- codex-pull-request-review-summary -->\n<!-- codex-review-state:v1 {\"code_review\":{\"commit_sha\":\"" + earlierHead + "\",\"has_any_findings\":false,\"has_unresolved_findings\":false,\"status\":\"completed\"},\"security_review\":{\"commit_sha\":\"" + earlierHead + "\",\"has_any_findings\":false,\"has_unresolved_findings\":false,\"status\":\"completed\"}} -->"}
	p := passingPR()
	p.HeadSHA = reviewHead
	p.ReviewThreads = nil
	p.ReviewEquivalentHeads = []string{earlierHead}
	p.AutomatedReviews = []AutomatedReviewComment{summary}
	p.Reviews = []Review{{ID: 7, User: "chatgpt-codex-connector[bot]", UserType: "Bot", State: "COMMENTED", CommitID: earlierHead, Body: "Outside-diff finding: also fix X", SubmittedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if r := liveEvaluator().checkReviewReadiness(p); r.Pass {
		t.Fatalf("carried-over review cleared prose: %s", r.Message)
	}
	// A fresh exact-head review after the prose still clears it, as before.
	fresh := summary
	fresh.Body = strings.ReplaceAll(summary.Body, earlierHead, reviewHead)
	p.AutomatedReviews = []AutomatedReviewComment{fresh}
	if r := liveEvaluator().checkReviewReadiness(p); !r.Pass {
		t.Fatalf("fresh exact-head review no longer clears old prose: %s", r.Message)
	}
}

// CodeRabbit's case on #284: PR history O -> P -> R, reviewed at R with main
// at O. main later fast-forwards through P to Q, and the PR merges Q. The
// collector then measures R against P, not O. That is still safe: every
// path in the merged head is either R's reviewed content or main's content,
// and if main moved any path the PR touches after P, carryover is refused.
func TestCarryoverWhenMainAbsorbedAnEarlierPRCommit(t *testing.T) {
	P := Tree{"f": blob("f1"), "g": blob("g0")} // PR commit, later on main
	R := Tree{"f": blob("f2"), "g": blob("g0")} // reviewed head; main was at O (f0, g0)
	Q := Tree{"f": blob("f1"), "g": blob("g1")} // main after P, f untouched
	head := Tree{"f": blob("f2"), "g": blob("g1")}
	if !SameChange(head, Q, R, P) {
		t.Fatal("clean update after main absorbed P rejected")
	}
	for p, e := range head {
		if e != R[p] && e != Q[p] {
			t.Fatalf("path %s is neither reviewed nor from main", p)
		}
	}
	// If main changed f again after P, the base versions differ: refused.
	Q2 := Tree{"f": blob("f1b"), "g": blob("g1")}
	if SameChange(Tree{"f": blob("f2"), "g": blob("g1")}, Q2, R, P) {
		t.Fatal("carried over although main changed a PR file after P")
	}
}

func TestBaseDriftAllowed(t *testing.T) {
	denied := []string{"internal/apiauth/**", ".github/workflows/**"}
	reviewedBase := Tree{"internal/api/handler.go": blob("h1"), "internal/api/util.go": blob("u1"), "internal/apiauth/check.go": blob("c1"), "docs/x.md": blob("x1"), "internal/other/o.go": blob("o1")}
	headBase := func(change map[string]string) Tree {
		t := Tree{}
		for k, v := range reviewedBase {
			t[k] = v
		}
		for k, v := range change {
			if v == "" {
				delete(t, k)
			} else {
				t[k] = blob(v)
			}
		}
		return t
	}
	head := func(base Tree) Tree {
		t := Tree{}
		for k, v := range base {
			t[k] = v
		}
		t["internal/api/handler.go"] = blob("h2") // the PR's change
		return t
	}
	for _, tc := range []struct {
		name   string
		change map[string]string
		ok     bool
	}{
		{"main changed nothing", nil, true},
		{"main changed docs elsewhere", map[string]string{"docs/x.md": "x2"}, true},
		{"main changed another package", map[string]string{"internal/other/o.go": "o2"}, true},
		{"main changed a helper next to the PR's file", map[string]string{"internal/api/util.go": "u2"}, false},
		{"main added a file next to the PR's file", map[string]string{"internal/api/new.go": "n1"}, false},
		{"main deleted a file next to the PR's file", map[string]string{"internal/api/util.go": ""}, false},
		{"main changed an auth helper elsewhere", map[string]string{"internal/apiauth/check.go": "c2"}, false},
		{"main added a workflow", map[string]string{".github/workflows/x.yml": "w1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hb := headBase(tc.change)
			if got := BaseDriftAllowed(head(hb), hb, reviewedBase, denied); got != tc.ok {
				t.Fatalf("got %v want %v", got, tc.ok)
			}
		})
	}
	if BaseDriftAllowed(head(reviewedBase), nil, reviewedBase, denied) || BaseDriftAllowed(head(reviewedBase), reviewedBase, nil, denied) {
		t.Fatal("missing tree accepted")
	}
}
