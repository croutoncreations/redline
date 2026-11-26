package greenlane

import (
	"strings"
	"testing"
)

func claudeNotice(body string) Review {
	return Review{ID: 900, User: "claude[bot]", UserID: claudeBotUserID, UserType: "Bot", State: "COMMENTED", CommitID: reviewHead, Body: body}
}

func liveReadiness(reviews ...Review) RuleResult {
	p := passingPR()
	p.HeadSHA = reviewHead
	p.Reviews = reviews
	p.AutomatedReviews = []AutomatedReviewComment{cleanSummary()}
	cfg := DefaultConfig()
	cfg.RequireAutomatedReview = true
	return NewEvaluator(cfg).checkReviewReadiness(p)
}

func TestClaudeNoReviewNoticesDoNotBlock(t *testing.T) {
	if len(claudeNoReviewNotices) != 2 {
		t.Fatalf("notice list changed: %d entries", len(claudeNoReviewNotices))
	}
	for body := range claudeNoReviewNotices {
		if r := liveReadiness(claudeNotice(body)); !r.Pass {
			t.Fatalf("exact notice blocked: %s", r.Message)
		}
	}
}

func TestClaudeNoticeExemptionIsExact(t *testing.T) {
	var body string
	for b := range claudeNoReviewNotices {
		if strings.HasPrefix(b, "⚠️") {
			body = b
		}
	}
	for name, edit := range map[string]func(*Review){
		"trailing newline":     func(r *Review) { r.Body += "\n" },
		"leading space":        func(r *Review) { r.Body = " " + r.Body },
		"appended finding":     func(r *Review) { r.Body += "\n\nAlso: this leaks the token." },
		"one character":        func(r *Review) { r.Body = strings.Replace(r.Body, "skipped", "Skipped", 1) },
		"CRLF":                 func(r *Review) { r.Body = strings.ReplaceAll(r.Body, "\n", "\r\n") },
		"prefix only":          func(r *Review) { r.Body = r.Body[:60] },
		"wrong user id":        func(r *Review) { r.UserID++ },
		"missing user id":      func(r *Review) { r.UserID = 0 },
		"human type":           func(r *Review) { r.UserType = "User" },
		"other login":          func(r *Review) { r.User = "claude" },
		"changes requested":    func(r *Review) { r.State = "CHANGES_REQUESTED" },
		"approved with notice": func(r *Review) { r.State = "APPROVED" },
		"real review":          func(r *Review) { r.Body = "## Code review\n\nFound 1 issue: nil dereference in handler.go." },
	} {
		t.Run(name, func(t *testing.T) {
			r := claudeNotice(body)
			edit(&r)
			if liveReadiness(r).Pass {
				t.Fatal("non-exact Claude review body was ignored")
			}
		})
	}
}

func TestClaudeNoticeIsNotAReview(t *testing.T) {
	p := passingPR()
	p.HeadSHA = reviewHead
	p.AutomatedReviews = nil
	for body := range claudeNoReviewNotices {
		p.Reviews = []Review{claudeNotice(body)}
		cfg := DefaultConfig()
		cfg.RequireAutomatedReview = true
		if NewEvaluator(cfg).checkReviewReadiness(p).Pass {
			t.Fatal("notice counted as a completed live review")
		}
		if NewEvaluator(DefaultConfig()).checkReviewReadiness(p).Pass {
			t.Fatal("bot notice counted as a completed shadow review")
		}
	}
}

func TestClaudeNoticeDoesNotHideOtherFeedback(t *testing.T) {
	var notice string
	for b := range claudeNoReviewNotices {
		notice = b
		break
	}
	other := Review{ID: 901, User: "coderabbitai[bot]", UserType: "Bot", State: "COMMENTED", CommitID: reviewHead, Body: "Nitpick: rename x."}
	if liveReadiness(claudeNotice(notice), other).Pass {
		t.Fatal("other reviewer prose bypassed by a Claude notice")
	}
}
