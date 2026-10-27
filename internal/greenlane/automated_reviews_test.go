package greenlane

import (
	"strings"
	"testing"
)

const reviewHead = "b297fb2405e62748e0315a2111aa2c0e96f41e61"

func cleanSummary() AutomatedReviewComment {
	return AutomatedReviewComment{ID: 42, UserID: 136622811, User: "coderabbitai[bot]", UserType: "Bot", AppID: 347564, UpdatedAt: "2026-01-01T00:00:00Z", LastEditedAt: "2026-01-01T00:00:00Z", EditorID: 136622811, EditorUser: "coderabbitai", EditorType: "Bot", Body: "<!-- coderabbit-ai-generated:v1 -->\n<!-- This is an auto-generated comment: summarize by coderabbit.ai -->\n<!-- final_review_risk_coverage:{\"sourceCommitId\":\"" + reviewHead + "\",\"coveredCommitId\":\"" + reviewHead + "\",\"kind\":\"reviewed\"} -->"}
}
func TestAuthenticatedSummaryCompletion(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*AutomatedReviewComment)
		pass bool
	}{
		{"clean", func(c *AutomatedReviewComment) {}, true},
		{"unedited app creation", func(c *AutomatedReviewComment) {
			c.LastEditedAt = ""
			c.EditorID = 0
			c.EditorUser = ""
			c.EditorType = ""
		}, true},
		{"wrong app", func(c *AutomatedReviewComment) { c.AppID++ }, false},
		{"wrong immutable author", func(c *AutomatedReviewComment) { c.UserID++ }, false},
		{"human copy", func(c *AutomatedReviewComment) { c.UserType = "User" }, false},
		{"human edit retains attribution", func(c *AutomatedReviewComment) { c.EditorID = 123; c.EditorType = "User"; c.EditorUser = "jfox85" }, false},
		{"missing editor on edit", func(c *AutomatedReviewComment) { c.EditorID = 0; c.EditorUser = ""; c.EditorType = "" }, false},
		{"editor with no edit time", func(c *AutomatedReviewComment) { c.LastEditedAt = "" }, false},
		{"invalid timestamp", func(c *AutomatedReviewComment) { c.UpdatedAt = "invalid" }, false},
		{"wrong head", func(c *AutomatedReviewComment) {
			c.Body = strings.ReplaceAll(c.Body, reviewHead, strings.Repeat("a", 40))
		}, false},
		{"partial coverage", func(c *AutomatedReviewComment) {
			c.Body = strings.Replace(c.Body, `"coveredCommitId":"`+reviewHead, `"coveredCommitId":"old`, 1)
		}, false},
		{"skipped", func(c *AutomatedReviewComment) { c.Body = strings.Replace(c.Body, `"reviewed"`, `"skipped"`, 1) }, false},
		{"duplicate JSON key", func(c *AutomatedReviewComment) {
			c.Body = strings.Replace(c.Body, `"kind":"reviewed"`, `"kind":"skipped","kind":"reviewed"`, 1)
		}, false},
		{"mixed-case duplicate JSON key", func(c *AutomatedReviewComment) {
			c.Body = strings.Replace(c.Body, `"kind":"reviewed"`, `"kind":"skipped","Kind":"reviewed"`, 1)
		}, false},
		{"unknown protocol", func(c *AutomatedReviewComment) { c.Body = strings.Replace(c.Body, `"kind":`, `"unknown":`, 1) }, false},
		{"duplicate marker", func(c *AutomatedReviewComment) { c.Body += "\n" + strings.Split(c.Body, "\n")[2] }, false},
		{"quoted", func(c *AutomatedReviewComment) { c.Body = strings.Replace(c.Body, "\n<!-- final", "\n> <!-- final", 1) }, false},
		{"fenced", func(c *AutomatedReviewComment) {
			c.Body = strings.Replace(c.Body, "\n<!-- final", "\n```\n<!-- final", 1) + "\n```"
		}, false},
		{"running", func(c *AutomatedReviewComment) {
			c.Body += "\n<!-- This is an auto-generated comment: review in progress by coderabbit.ai -->"
		}, false},
		{"rate limited", func(c *AutomatedReviewComment) {
			c.Body += "\n<!-- This is an auto-generated comment: rate limited by coderabbit.ai -->"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cleanSummary()
			tc.edit(&c)
			p := PRInfo{HeadSHA: reviewHead, Author: "jfox85", AutomatedReviews: []AutomatedReviewComment{c}}
			if got := completedAutomatedReview(p); got != tc.pass {
				t.Fatalf("completion=%v want %v", got, tc.pass)
			}
		})
	}
}
func TestSummaryCannotBypassFeedback(t *testing.T) {
	p := passingPR()
	p.HeadSHA = reviewHead
	p.Reviews = nil
	p.AutomatedReviews = []AutomatedReviewComment{cleanSummary()}
	e := NewEvaluator(DefaultConfig())
	if !e.checkReviewReadiness(p).Pass {
		t.Fatal("clean review rejected")
	}
	p.ReviewThreads = []ReviewThread{{ID: "open", Outdated: true}}
	if e.checkReviewReadiness(p).Pass {
		t.Fatal("unresolved feedback bypassed")
	}
	p.ReviewThreads = nil
	p.Reviews = []Review{{ID: 1, User: "other", State: "CHANGES_REQUESTED", CommitID: "old"}}
	if e.checkReviewReadiness(p).Pass {
		t.Fatal("outstanding request bypassed")
	}
	p.Reviews = nil
	c := cleanSummary()
	c.ID++
	c.UpdatedAt = "2026-01-02T00:00:00Z"
	c.Body += "\n<!-- This is an auto-generated comment: review in progress by coderabbit.ai -->"
	p.AutomatedReviews = append(p.AutomatedReviews, c)
	if completedAutomatedReview(p) {
		t.Fatal("older completion bypassed latest running summary")
	}
}
func TestCodexCompletion(t *testing.T) {
	c := AutomatedReviewComment{ID: 43, UserID: 199175422, User: "chatgpt-codex-connector[bot]", UserType: "Bot", AppID: 1144995, UpdatedAt: "2026-01-01T00:00:00Z", Body: "<!-- codex-pull-request-review-summary -->\n<!-- codex-review-state:v1 {\"code_review\":{\"commit_sha\":\"" + reviewHead + "\",\"status\":\"completed\",\"has_any_findings\":false,\"has_unresolved_findings\":false},\"security_review\":null} -->"}
	for _, tc := range []struct {
		old, new string
		pass     bool
	}{{"", "", true}, {"completed", "running", false}, {`"has_unresolved_findings":false`, `"has_unresolved_findings":true`, false}, {reviewHead, "old", false}, {`"has_any_findings":false,`, "", false}} {
		v := c
		if tc.old != "" {
			v.Body = strings.Replace(v.Body, tc.old, tc.new, 1)
		}
		if got := completedAutomatedReview(PRInfo{HeadSHA: reviewHead, AutomatedReviews: []AutomatedReviewComment{v}}); got != tc.pass {
			t.Fatalf("%s: completion=%v", tc.old, got)
		}
	}
}
func TestSummarySnapshotBindsEdits(t *testing.T) {
	p := PRInfo{AutomatedReviews: []AutomatedReviewComment{cleanSummary()}}
	before := ReviewSnapshot(p)
	for _, edit := range []func(*AutomatedReviewComment){func(c *AutomatedReviewComment) { c.Body += "changed" }, func(c *AutomatedReviewComment) { c.EditorID++ }, func(c *AutomatedReviewComment) { c.LastEditedAt = "later" }} {
		c := cleanSummary()
		edit(&c)
		p.AutomatedReviews = []AutomatedReviewComment{c}
		if ReviewSnapshot(p) == before {
			t.Fatal("edit not bound to snapshot")
		}
	}
}

// Security findings may exist only in the summary, without inline threads.
func TestCodexSecurityReviewReadiness(t *testing.T) {
	component := `{"commit_sha":"` + reviewHead + `","status":"completed","has_any_findings":false,"has_unresolved_findings":false}`
	for _, tc := range []struct {
		name, security string
		pass           bool
	}{
		{"absent", "", true}, {"not requested", "null", true}, {"completed clean", component, true},
		{"findings addressed", strings.Replace(component, `"has_any_findings":false`, `"has_any_findings":true`, 1), true},
		{"running", strings.Replace(component, `"completed"`, `"running"`, 1), false},
		{"queued", strings.Replace(component, `"completed"`, `"queued"`, 1), false},
		{"unresolved", strings.Replace(component, `"has_unresolved_findings":false`, `"has_unresolved_findings":true`, 1), false},
		{"stale", strings.Replace(component, reviewHead, strings.Repeat("a", 40), 1), false},
		{"empty object", `{}`, false}, {"wrong type", `false`, false},
		{"missing findings", strings.Replace(component, `"has_any_findings":false,`, "", 1), false},
		{"missing unresolved", strings.Replace(component, `,"has_unresolved_findings":false`, "", 1), false},
		{"unknown schema", strings.Replace(component, `"status":`, `"unknown":`, 1), false},
		{"mixed-case duplicate status", strings.Replace(component, `"status":"completed"`, `"status":"running","Status":"completed"`, 1), false},
		{"Unicode alias", strings.Replace(component, `"status":"completed"`, `"status":"running","ſtatus":"completed"`, 1), false},
		{"duplicate status", strings.Replace(component, `"status":"completed"`, `"status":"running","status":"completed"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"code_review":` + component
			if tc.security != "" {
				payload += `,"security_review":` + tc.security
			}
			payload += `}`
			c := AutomatedReviewComment{ID: 43, UserID: 199175422, User: "chatgpt-codex-connector[bot]", UserType: "Bot", AppID: 1144995, UpdatedAt: "2026-01-01T00:00:00Z", Body: "<!-- codex-pull-request-review-summary -->\n<!-- codex-review-state:v1 " + payload + " -->"}
			p := passingPR()
			p.HeadSHA = reviewHead
			p.Reviews = nil
			p.ReviewThreads = nil
			p.AutomatedReviews = []AutomatedReviewComment{c}
			if got := NewEvaluator(DefaultConfig()).checkReviewReadiness(p).Pass; got != tc.pass {
				t.Fatalf("review readiness=%v want %v (no inline threads)", got, tc.pass)
			}
		})
	}
}
