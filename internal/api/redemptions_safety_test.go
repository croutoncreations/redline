package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/usage"
)

type redemptionRateLimit struct{}

func (redemptionRateLimit) Error() string             { return "429" }
func (redemptionRateLimit) RateLimited() bool         { return true }
func (redemptionRateLimit) RetryAfter() time.Duration { return time.Hour }

func TestRedeemBankedResetSharesRateLimitWithUsageAndSuppressesDryNotifications(t *testing.T) {
	s, db, _ := redemptionServer(t, "claude")
	notifier := &redemptionNotifier{}
	s.notifier = notifier
	calls := 0
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "redline-dryrun", RequestID: "dry-key", DryRun: true}
	}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		calls++
		return nativeusage.RedeemResult{}, redemptionRateLimit{}
	}}
	_, err := s.RedeemBankedReset(context.Background(), "main", "manual", true)
	if err == nil {
		t.Fatal("missing rate limit")
	}
	until := s.usageSources.NativeBlockedUntil("Claude")
	if !until.Equal(s.now().Add(time.Hour)) {
		t.Fatal(until)
	}
	got, err := s.RedeemBankedReset(context.Background(), "main", "manual", false)
	if err != nil || got.Outcome != "cooldown" || calls != 1 {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
	if len(notifier.events) != 1 || notifier.events[0].Type != "reset.redeem_failed" {
		t.Fatal(notifier.events)
	}
	rows, _ := db.BankedResetRedemptions(context.Background(), "main", 20)
	if len(rows) != 1 || rows[0].Trigger != "dry_run" || rows[0].ResetID != "redline-dryrun" {
		t.Fatal(rows)
	}
}

func TestRedeemBankedResetOutcomesAndVerification(t *testing.T) {
	for _, outcome := range []string{"redeemed", "nothing_to_reset", "cooldown", "unavailable", "gone", "failed", "dry_run_ok"} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry=%t", outcome, dryRun), func(t *testing.T) {
				s, db, pre := redemptionServer(t, "codex")
				notifier := &redemptionNotifier{}
				s.notifier = notifier
				s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
					return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key", DryRun: dryRun}
				}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
					return nativeusage.RedeemResult{Outcome: outcome, Code: "raw"}, nil
				}}
				got, err := s.RedeemBankedReset(context.Background(), "main", "manual", dryRun)
				if err != nil {
					t.Fatal(err)
				}
				if got.Verified {
					t.Fatal("unchanged usage verified")
				}
				if outcome == "redeemed" && !dryRun && !strings.Contains(got.Reason, "Codex") {
					t.Fatal(got)
				}
				want := 1
				if dryRun || outcome == "nothing_to_reset" || outcome == "dry_run_ok" {
					want = 0
				}
				if len(notifier.events) != want {
					t.Fatalf("events=%+v", notifier.events)
				}
				if want == 1 {
					event := "reset.redeem_failed"
					if outcome == "redeemed" {
						event = "reset.redeemed"
					}
					if notifier.events[0].Type != event {
						t.Fatal(notifier.events)
					}
				}
				latest, _, err := db.LatestSnapshot(context.Background(), "main", "codex")
				if err != nil || latest.Weekly != pre.Weekly {
					t.Fatalf("%+v %v", latest, err)
				}
			})
		}
	}
	// A moved weekly boundary with an unchanged observation is not verification.
	s, _, pre := redemptionServer(t, "codex")
	post := pre
	post.Weekly.ResetsAt = pre.Weekly.ResetsAt.Add(time.Hour)
	s.usageSources = usage.NewManager(staticUsageSource{snapshots: map[string]decision.UsageSnapshot{"main": post}}, staticUsageSource{snapshots: map[string]decision.UsageSnapshot{"main": post}}, s.now)
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest { return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"} }, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
	}}
	got, err := s.RedeemBankedReset(context.Background(), "main", "manual", false)
	if err != nil || got.Verified || !strings.Contains(got.Reason, "Usage has not refilled") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestBankedResetRedeemAuthenticatedAPIAndInvalidBody(t *testing.T) {
	s, _, _ := redemptionServer(t, "claude")
	calls := 0
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "redline-dryrun", RequestID: "key", DryRun: true}
	}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		calls++
		if !req.DryRun {
			t.Error("dry run lost")
		}
		return nativeusage.RedeemResult{Outcome: "dry_run_ok", Code: "ineligible"}, nil
	}}
	for _, body := range []string{`{"dry_run":true}`, `{"dry_run":"true"}`, `{"dry_run":true,"reset_id":"real"}`, `{} {}`, `null`} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:7436/v1/providers/main/banked-resets/redeem", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if body == `{"dry_run":true}` {
			var got nativeusage.RedeemResult
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if w.Code != 200 || got.Outcome != "dry_run_ok" || got.RequestID != "key" {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		} else if w.Code != 400 {
			t.Fatalf("body=%s status=%d %s", body, w.Code, w.Body)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
