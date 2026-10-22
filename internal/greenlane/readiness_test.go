package greenlane

import (
	"strings"
	"testing"
	"time"
)

func TestReviewReadiness(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*PRInfo)
		pass    bool
		message string
	}{
		{"approved", func(p *PRInfo) {}, true, "OK"},
		{"automated commented review without findings", func(p *PRInfo) { p.Reviews[0].User = "coderabbitai[bot]"; p.Reviews[0].State = "COMMENTED" }, true, "OK"},
		{"no reviews", func(p *PRInfo) { p.Reviews = nil }, false, "No completed"},
		{"pending", func(p *PRInfo) { p.Reviews[0].State = "PENDING" }, false, "No completed"},
		{"issue comments do not count", func(p *PRInfo) { p.Reviews = nil; p.ReviewComments = []ReviewComment{{Body: "looks good"}} }, false, "No completed"},
		{"old commented review", func(p *PRInfo) { p.Reviews[0].State = "COMMENTED"; p.Reviews[0].CommitID = "old" }, false, "No completed"},
		{"author comment", func(p *PRInfo) { p.Reviews[0].State = "COMMENTED"; p.Reviews[0].User = "JFOX85" }, false, "No completed"},
		{"unresolved", func(p *PRInfo) { p.ReviewThreads = []ReviewThread{{ID: "thread"}} }, false, "Unresolved"},
		{"outdated unresolved", func(p *PRInfo) { p.ReviewThreads = []ReviewThread{{ID: "old", Outdated: true}} }, false, "Unresolved"},
		{"resolved", func(p *PRInfo) { p.ReviewThreads = []ReviewThread{{ID: "resolved", Resolved: true}} }, true, "OK"},
		{"partial pagination", func(p *PRInfo) { p.ReviewThreadsComplete = false }, false, "completely fetched"},
		{"old approval", func(p *PRInfo) { p.Reviews[0].CommitID = "older" }, false, "No completed"},
		{"self approval", func(p *PRInfo) { p.Reviews[0].User = p.Author }, false, "No completed"},
		{"old request persists", func(p *PRInfo) {
			p.Reviews = append(p.Reviews, Review{ID: 2, User: "other", State: "CHANGES_REQUESTED", CommitID: "older"})
		}, false, "earlier heads"},
		{"comment cannot clear request", func(p *PRInfo) {
			p.Reviews = append(p.Reviews, Review{ID: 2, User: "reviewer", State: "CHANGES_REQUESTED", CommitID: "older"}, Review{ID: 3, User: "reviewer", State: "COMMENTED", CommitID: p.HeadSHA})
		}, false, "changes requested"},
		{"later approval clears request", func(p *PRInfo) {
			p.Reviews = append(p.Reviews, Review{ID: 2, User: "reviewer", State: "CHANGES_REQUESTED", CommitID: "older"}, Review{ID: 3, User: "reviewer", State: "APPROVED", CommitID: p.HeadSHA})
		}, true, "OK"},
		{"dismissal removes approval", func(p *PRInfo) {
			p.Reviews = append(p.Reviews, Review{ID: 2, User: "reviewer", State: "DISMISSED", CommitID: p.HeadSHA})
		}, false, "No completed"},
		{"trusted dismissal clears request", func(p *PRInfo) {
			p.Reviews = append(p.Reviews, Review{ID: 2, User: "other", State: "CHANGES_REQUESTED"}, Review{ID: 3, User: "other", State: "DISMISSED"})
		}, true, "OK"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := passingPR(PRFile{Filename: "README.md"})
			tc.edit(&p)
			r := NewEvaluator(DefaultConfig()).checkReviewReadiness(p)
			if r.Pass != tc.pass || !strings.Contains(r.Message, tc.message) {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestEffectiveReviewsUseTimeNotInputOrder(t *testing.T) {
	now := time.Now()
	p := passingPR()
	p.Reviews = []Review{{ID: 10, User: "reviewer", State: "CHANGES_REQUESTED", SubmittedAt: now, CommitID: "old"}, {ID: 20, User: "reviewer", State: "APPROVED", SubmittedAt: now.Add(-time.Hour), CommitID: p.HeadSHA}}
	if NewEvaluator(DefaultConfig()).checkReviewReadiness(p).Pass {
		t.Fatal("older approval cleared newer request")
	}
	p.Reviews = append(p.Reviews, Review{ID: 30, User: "reviewer", State: "APPROVED", SubmittedAt: now.Add(time.Minute), CommitID: p.HeadSHA})
	if !NewEvaluator(DefaultConfig()).checkReviewReadiness(p).Pass {
		t.Fatal("new approval did not clear request")
	}
}
func TestStateAndCIBindings(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PRInfo)
	}{
		{"draft", func(p *PRInfo) { p.Draft = true }}, {"closed", func(p *PRInfo) { p.State = "closed" }}, {"conflict or unknown", func(p *PRInfo) { p.Mergeable = false }}, {"base", func(p *PRInfo) { p.BaseBranch = "release" }}, {"changed snapshot", func(p *PRInfo) { p.SnapshotConsistent = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := passingPR()
			tc.edit(&p)
			if NewEvaluator(DefaultConfig()).checkPRState(p).Pass {
				t.Fatal("state admitted")
			}
		})
	}
	for _, edit := range []func(*CheckRun){func(c *CheckRun) { c.AppID = 999 }, func(c *CheckRun) { c.WorkflowID = 999 }, func(c *CheckRun) { c.HeadSHA = "old" }, func(c *CheckRun) { c.Conclusion = "failure" }, func(c *CheckRun) { c.Status = "in_progress" }} {
		p := passingPR()
		edit(&p.CheckRuns[0])
		if NewEvaluator(DefaultConfig()).checkCI(p).Pass {
			t.Fatal("untrusted or stale check admitted")
		}
	}
}
func TestCodeRabbitUsesLatestDecisiveReview(t *testing.T) {
	p := passingPR()
	p.Reviews = []Review{{ID: 1, User: "coderabbitai[bot]", State: "APPROVED", CommitID: p.HeadSHA}, {ID: 2, User: "coderabbitai[bot]", State: "CHANGES_REQUESTED", CommitID: p.HeadSHA}, {ID: 3, User: "coderabbitai[bot]", State: "COMMENTED", CommitID: p.HeadSHA}}
	if r := checkCodeRabbitReview(p); !r.RequestedChanges || r.Approved {
		t.Fatalf("%+v", r)
	}
}

func TestAllowedAuthors(t *testing.T) {
	for _, author := range []string{"jfox85", "JFOX85", "unknown[bot]", "", "jfox85-extra"} {
		p := passingPR()
		p.Author = author
		want := strings.EqualFold(author, "jfox85")
		if r := NewEvaluator(DefaultConfig()).checkAuthor(p); r.Pass != want {
			t.Fatalf("%s: %+v", author, r)
		}
	}
	c := DefaultConfig()
	c.AllowedAuthors = nil
	if NewEvaluator(c).checkAuthor(passingPR()).Pass {
		t.Fatal("empty allowlist trusted an author")
	}
}

func TestLockfileInMixedCodePRStaysManual(t *testing.T) {
	p := passingPR(PRFile{Filename: "internal/fix.go", Additions: 1}, PRFile{Filename: "go.mod", Patch: goModBump}, PRFile{Filename: "go.sum", Additions: 1})
	r := NewEvaluator(DefaultConfig()).Evaluate(p, nil, nil)
	if r.Verdict != VerdictWouldNotMerge || ruleByName(t, r, "Denied Paths").Pass {
		t.Fatal("code change bypassed manual lockfile policy")
	}
}
