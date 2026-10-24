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
		{"bot reply publication does not count", func(p *PRInfo) { p.Reviews[0].User = "coderabbitai[bot]"; p.Reviews[0].State = "COMMENTED" }, false, "No completed"},
		{"bot review prose needs completion protocol", func(p *PRInfo) {
			p.Reviews[0].User = "coderabbitai[bot]"
			p.Reviews[0].State = "COMMENTED"
			p.Reviews[0].Body = "Verified the fix"
		}, false, "No completed"},
		{"empty human reply publication does not count", func(p *PRInfo) { p.Reviews[0].State = "COMMENTED" }, false, "No completed"},
		{"nonempty human review", func(p *PRInfo) {
			p.Reviews[0].State = "COMMENTED"
			p.Reviews[0].Body = "Reviewed implementation and tests"
		}, true, "OK"},
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

func TestReviewBodyIsPartOfSnapshot(t *testing.T) {
	p := passingPR(PRFile{Filename: "README.md"})
	before := ReviewSnapshot(p)
	p.Reviews[0].Body = "Reviewed implementation"
	if before == ReviewSnapshot(p) {
		t.Fatal("review body edits must invalidate the snapshot")
	}
}

func TestIgnoredRepliesPreserveCompletion(t *testing.T) {
	for _, user := range []string{"reviewer", "coderabbitai[bot]"} {
		for _, reply := range []string{"", "  ", "Verified inline fix"} {
			for _, invalidation := range []string{"", "CHANGES_REQUESTED", "DISMISSED"} {
				t.Run(user+"/"+reply+"/"+invalidation, func(t *testing.T) {
					p := passingPR(PRFile{Filename: "README.md"})
					p.Reviews = []Review{{ID: 1, User: user, State: "APPROVED", CommitID: p.HeadSHA}}
					if invalidation != "" {
						p.Reviews = append(p.Reviews, Review{ID: 2, User: user, State: invalidation, CommitID: p.HeadSHA})
					}
					p.Reviews = append(p.Reviews, Review{ID: 3, User: user, State: "COMMENTED", Body: reply, CommitID: p.HeadSHA})
					got := NewEvaluator(DefaultConfig()).checkReviewReadiness(p)
					// A substantive human review can restore completion after dismissal;
					// comments never clear a changes request.
					want := invalidation == "" || (invalidation == "DISMISSED" && user == "reviewer" && strings.TrimSpace(reply) != "")
					if got.Pass != want {
						t.Fatalf("pass=%v want=%v: %s", got.Pass, want, got.Message)
					}
				})
			}
		}
	}
	p := passingPR(PRFile{Filename: "README.md"})
	p.Reviews = []Review{{ID: 1, User: "reviewer", State: "COMMENTED", Body: "Reviewed implementation", CommitID: p.HeadSHA}, {ID: 2, User: "reviewer", State: "COMMENTED", CommitID: p.HeadSHA}}
	if r := NewEvaluator(DefaultConfig()).checkReviewReadiness(p); !r.Pass {
		t.Fatal(r.Message)
	}
}

func TestReviewSnapshotPublisherParity(t *testing.T) {
	// Shared with publish.test.cjs, including HTML and Unicode escape hazards.
	p := PRInfo{Reviews: []Review{{ID: 1, User: "reviewer", State: "APPROVED", CommitID: "head", Body: "Reviewed <tags> & \"quotes\" \u2028 café"}}}
	const want = "db35604bd99c33113262a7c08cce94aa8bfdc52e42e9222d1a8ab7a6a83a3adb"
	if got := ReviewSnapshot(p); got != want {
		t.Fatalf("publisher parity: got %s want %s", got, want)
	}
}

func TestLiveRequiresTrustedAutomatedCompletion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RequireAutomatedReview = true
	p := passingPR()
	p.HeadSHA = reviewHead
	for _, state := range []string{"COMMENTED", "APPROVED"} {
		p.Reviews = []Review{{ID: 1, User: "collaborator", State: state, CommitID: reviewHead, Body: "looks good"}}
		if NewEvaluator(cfg).checkReviewReadiness(p).Pass {
			t.Fatalf("live accepted untrusted %s review", state)
		}
	}
	p.Reviews = nil // A clean summary does not classify collaborator review prose.
	p.AutomatedReviews = []AutomatedReviewComment{{ID: 43, UserID: 199175422, User: "chatgpt-codex-connector[bot]", UserType: "Bot", AppID: 1144995, UpdatedAt: "2026-01-01T00:00:00Z", Body: `<!-- codex-pull-request-review-summary -->
<!-- codex-review-state:v1 {"code_review":{"commit_sha":"` + reviewHead + `","status":"completed","has_any_findings":false,"has_unresolved_findings":false},"security_review":null} -->`}}
	if !NewEvaluator(cfg).checkReviewReadiness(p).Pass {
		t.Fatal("live rejected trusted authenticated current-head completion")
	}
	p.AutomatedReviews[0].AppID = 1
	if NewEvaluator(cfg).checkReviewReadiness(p).Pass {
		t.Fatal("live accepted spoofed App plus collaborator approval")
	}
}

// PR187 had zero threads and positive exact-head coverage, but review-body
// feedback still required an exact retry candidate-list assertion.
func TestLiveReviewBodyFeedbackCannotBeBypassed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RequireAutomatedReview = true
	for _, tc := range []struct {
		name, user, state, body string
		pass                    bool
	}{
		{"PR187 nitpick", "coderabbitai[bot]", "COMMENTED", "<!-- coderabbit-ai-generated:v1 -->\n<summary>🧹 Nitpick comments: 1</summary>\nAssert the complete candidate list in the retry-order test.", false},
		{"outside diff", "coderabbitai[bot]", "COMMENTED", "Outside diff range comments: 1\nFix the missing check", false},
		{"dismissed feedback", "coderabbitai[bot]", "DISMISSED", "Fix the missing check", false},
		{"unknown provider format", "other[bot]", "COMMENTED", "Needs a fix", false},
		{"approval with feedback", "reviewer", "APPROVED", "Looks good, but fix the order assertion", false},
		{"empty reply", "coderabbitai[bot]", "COMMENTED", "", true},
		{"whitespace reply", "coderabbitai[bot]", "COMMENTED", " \n", true},
		{"author response", "JFOX85", "COMMENTED", "Fixed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := passingPR()
			p.HeadSHA = reviewHead
			p.AutomatedReviews = []AutomatedReviewComment{cleanSummary()}
			p.Reviews = []Review{{ID: 5638297150, User: tc.user, State: tc.state, CommitID: reviewHead, Body: tc.body}}
			r := NewEvaluator(cfg).checkReviewReadiness(p)
			if r.Pass != tc.pass {
				t.Fatalf("pass=%v want=%v: %s", r.Pass, tc.pass, r.Message)
			}
			if !tc.pass && !strings.Contains(r.Message, "review bodies") {
				t.Fatal(r.Message)
			}
		})
	}
}

func TestLiveBodyBlockSurvivesOtherProviderCompletion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RequireAutomatedReview = true
	p := passingPR()
	p.HeadSHA = reviewHead
	p.Reviews = []Review{{ID: 5638297150, User: "coderabbitai[bot]", State: "COMMENTED", CommitID: reviewHead, SubmittedAt: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), Body: "Assert the complete retry candidate list"}}
	p.AutomatedReviews = []AutomatedReviewComment{{ID: 43, UserID: 199175422, User: "chatgpt-codex-connector[bot]", UserType: "Bot", AppID: 1144995, UpdatedAt: "2026-01-01T00:00:00Z", Body: `<!-- codex-pull-request-review-summary -->
<!-- codex-review-state:v1 {"code_review":{"commit_sha":"` + reviewHead + `","status":"completed","has_any_findings":false,"has_unresolved_findings":false},"security_review":null} -->`}}
	if r := NewEvaluator(cfg).checkReviewReadiness(p); r.Pass {
		t.Fatal("clean Codex summary bypassed CodeRabbit body feedback")
	}
	// Moving the head cannot clear feedback by reusing the old completion.
	p.HeadSHA = strings.Repeat("a", 40)
	if r := NewEvaluator(cfg).checkReviewReadiness(p); r.Pass {
		t.Fatal("new head reused stale completion")
	}
	p.AutomatedReviews[0].Body = strings.ReplaceAll(p.AutomatedReviews[0].Body, reviewHead, p.HeadSHA)
	if r := NewEvaluator(cfg).checkReviewReadiness(p); r.Pass {
		t.Fatal("new head plus another provider cleared historical body feedback")
	}
	// The fix must receive fresh full current-head completion, not an author reply.
	p.AutomatedReviews = []AutomatedReviewComment{cleanSummary()}
	p.AutomatedReviews[0].Body = strings.ReplaceAll(p.AutomatedReviews[0].Body, reviewHead, p.HeadSHA)
	if r := NewEvaluator(cfg).checkReviewReadiness(p); !r.Pass {
		t.Fatal(r.Message)
	}
}

func TestHistoricalBodyNeedsLaterAuthenticProviderCompletion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RequireAutomatedReview = true
	for _, tc := range []struct {
		name string
		edit func(*PRInfo)
	}{
		{"completion predates finding", func(p *PRInfo) {}},
		{"forged later App", func(p *PRInfo) {
			c := p.AutomatedReviews[0]
			c.ID++
			c.AppID = 1
			c.UpdatedAt = "2026-01-03T00:00:00Z"
			p.AutomatedReviews = append(p.AutomatedReviews, c)
		}},
		{"author edited later summary", func(p *PRInfo) {
			c := p.AutomatedReviews[0]
			c.ID++
			c.UpdatedAt = "2026-01-03T00:00:00Z"
			c.LastEditedAt = c.UpdatedAt
			c.EditorID = 123
			c.EditorType = "User"
			c.EditorUser = "jfox85"
			p.AutomatedReviews = append(p.AutomatedReviews, c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := passingPR()
			p.HeadSHA = reviewHead
			p.AutomatedReviews = []AutomatedReviewComment{cleanSummary()}
			p.Reviews = []Review{{ID: 1, User: "coderabbitai[bot]", State: "COMMENTED", CommitID: strings.Repeat("a", 40), Body: "Fix missing candidates", SubmittedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}}
			tc.edit(&p)
			if NewEvaluator(cfg).checkReviewReadiness(p).Pass {
				t.Fatal("historical feedback cleared without later authentic completion")
			}
		})
	}
}
