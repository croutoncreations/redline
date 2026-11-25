package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
)

// liveAtLimitResets is the shape of a Max account out of its weekly limit
// (Fable allowance 17% used) holding two full resets and three Fable resets,
// as Claude's at_wall read reported it. Claude names the Dec 10 full reset as
// its next grant; each Fable reset is also usable.
func liveAtLimitResets(now time.Time) []decision.BankedReset {
	yes := true
	at := func(s string) *time.Time { t, _ := time.Parse(time.RFC3339, s); return &t }
	full := []string{"five_hour", "weekly", "fable"}
	fable := []string{"five_hour", "fable"}
	return []decision.BankedReset{
		{ID: "full-reset-2611", Kind: "full", ResetsLeft: 1, ExpiresAt: at("2026-12-10T22:00:00Z"), Clears: full, UsableNow: &yes, Claimable: true},
		{ID: "free_reset_oct_2026", Kind: "full", ResetsLeft: 1, ExpiresAt: at("2026-12-16T04:59:00Z"), Clears: full, UsableNow: &yes},
		{ID: "launch-2610-fable-6mo", Kind: "fable", ResetsLeft: 1, ExpiresAt: at("2027-03-09T07:59:00Z"), Clears: fable, UsableNow: &yes},
		{ID: "launch-2610-fable", Kind: "fable", ResetsLeft: 1, ExpiresAt: at("2027-03-09T07:59:00Z"), Clears: fable, UsableNow: &yes},
	}
}

func TestSelectBankedResetForTheReachedLimit(t *testing.T) {
	now := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	details := liveAtLimitResets(now)
	for _, tc := range []struct {
		name   string
		choice ResetChoice
		want   string
	}{
		// Out of the week: a full reset, Claude's next one first. A Fable
		// reset would only give back what Fable used (17%).
		{"weekly limit spends a full reset", ResetChoice{Limit: "weekly"}, "full-reset-2611"},
		// Only the Fable allowance is used up: save the full resets.
		{"Fable allowance spends a Fable reset", ResetChoice{Limit: "fable"}, "launch-2610-fable-6mo"},
		{"5-hour limit prefers the narrower Fable reset over a full one", ResetChoice{Limit: "five_hour"}, "launch-2610-fable-6mo"},
		{"asked for a Fable reset at the weekly limit", ResetChoice{Limit: "weekly", Kind: "fable"}, "launch-2610-fable-6mo"},
		{"asked for a full reset with no limit reached", ResetChoice{Kind: "full"}, "full-reset-2611"},
		{"no choice keeps Claude's next grant", ResetChoice{}, "full-reset-2611"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectBankedResetFor("claude", details, now, tc.choice)
			if err != nil || got.ID != tc.want {
				t.Fatalf("got %q err=%v, want %q", got.ID, err, tc.want)
			}
		})
	}
}

func TestSelectBankedResetForExplainsWhatCanBeUsed(t *testing.T) {
	now := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	details := liveAtLimitResets(now)
	_, err := SelectBankedResetFor("claude", details, now, ResetChoice{Limit: "weekly", Kind: "session"})
	if err == nil || !strings.Contains(err.Error(), "the 5-hour reset is not available; you can use the full reset or the Fable reset") {
		t.Fatalf("err = %v", err)
	}
	// Only Fable resets left: asking for a full one names what can be used.
	_, err = SelectBankedResetFor("claude", details[2:], now, ResetChoice{Limit: "five_hour", Kind: "full"})
	if err == nil || !strings.Contains(err.Error(), "the full reset is not available; you can use the Fable reset") {
		t.Fatalf("err = %v", err)
	}
	blocked := details[2]
	no := false
	blocked.UsableNow, blocked.UnavailableReason = &no, "blocked_seven_day"
	_, err = SelectBankedResetFor("claude", []decision.BankedReset{blocked}, now, ResetChoice{Kind: "fable"})
	if err == nil || !strings.Contains(err.Error(), "the Fable reset can't be used while you're at your weekly limit") {
		t.Fatalf("err = %v", err)
	}
	if _, err := SelectBankedResetFor("codex", nil, now, ResetChoice{Kind: "full"}); err == nil {
		t.Fatal("codex accepted a reset kind")
	}
}

type fakeAtLimit struct {
	calls  int
	result nativeusage.ClaudeResetsAtLimit
	err    error
}

func (f *fakeAtLimit) BankedResetsAtLimit(context.Context, config.Provider) (nativeusage.ClaudeResetsAtLimit, error) {
	f.calls++
	return f.result, f.err
}

// Redeeming by kind reads Claude's resets fresh at the limit and spends the
// chosen grant, not Claude's next one.
func TestRedeemByKindUsesTheFreshAtLimitRead(t *testing.T) {
	s, _, pre := redemptionServer(t, "claude")
	now := pre.ObservedAt
	fresh := liveAtLimitResets(now)
	for i := range fresh {
		future := now.Add(time.Duration(i+1) * 24 * time.Hour)
		fresh[i].ExpiresAt = &future
	}
	reader := &fakeAtLimit{result: nativeusage.ClaudeResetsAtLimit{
		Resets: &decision.BankedResets{Available: len(fresh), Details: fresh}, Exhausted: []string{"fable"}}}
	s.atLimitResets = reader
	var spent string
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "launch-2610-fable-6mo", RequestID: "key"}
	}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		spent = req.ResetID
		return nativeusage.RedeemResult{Outcome: "nothing_to_reset", Code: "not_limited"}, nil
	}}
	if _, err := s.redeemBankedResetKind(context.Background(), "main", "manual", false, "launch-2610-fable-6mo", "auto"); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || spent != "launch-2610-fable-6mo" {
		t.Fatalf("calls=%d spent=%q", reader.calls, spent)
	}
	// Bound to a different confirmed ID: refuse rather than spend another.
	if _, err := s.redeemBankedResetKind(context.Background(), "main", "manual", false, "full-reset-2611", "auto"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("err = %v", err)
	}
}

func TestRedeemByKindHonorsTheRateLimitLockoutAndReportsReadErrors(t *testing.T) {
	s, _, _ := redemptionServer(t, "claude")
	reader := &fakeAtLimit{err: &nativeusage.HTTPError{Provider: "claude", StatusCode: http.StatusTooManyRequests}}
	s.atLimitResets = reader
	got, err := s.redeemBankedResetKind(context.Background(), "main", "manual", false, "", "auto")
	if err != nil || got.Outcome != "unavailable" || !strings.Contains(got.Reason, "HTTP 429") {
		t.Fatalf("first = %+v err=%v", got, err)
	}
	// The 429 locked the endpoint out; the next attempt must not ask again.
	got, err = s.redeemBankedResetKind(context.Background(), "main", "manual", false, "", "auto")
	if err != nil || got.Outcome != "cooldown" || got.CooldownUntil == nil {
		t.Fatalf("second = %+v err=%v", got, err)
	}
	if reader.calls != 1 {
		t.Fatalf("calls = %d, want 1", reader.calls)
	}
}

func TestBankedResetChoiceRoute(t *testing.T) {
	s, _, pre := redemptionServer(t, "claude")
	fresh := liveAtLimitResets(pre.ObservedAt)
	for i := range fresh {
		future := pre.ObservedAt.Add(time.Duration(i+1) * 24 * time.Hour)
		fresh[i].ExpiresAt = &future
	}
	s.atLimitResets = &fakeAtLimit{result: nativeusage.ClaudeResetsAtLimit{
		Resets: &decision.BankedResets{Available: len(fresh), Details: fresh}, Exhausted: []string{"weekly"}}}
	for _, tc := range []struct {
		query string
		code  int
		want  string
	}{
		{"auto", http.StatusOK, `"id":"full-reset-2611"`},
		{"fable", http.StatusOK, `"id":"launch-2610-fable-6mo"`},
		{"session", http.StatusUnprocessableEntity, "you can use"},
		{"bogus", http.StatusBadRequest, "kind must be one of"},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7436/v1/providers/main/banked-resets/choice?kind="+tc.query, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("kind=%s: %d %s", tc.query, rec.Code, rec.Body.String())
		}
	}
}

func TestReachedLimitAndRefills(t *testing.T) {
	now := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	week := now.Add(48 * time.Hour)
	s := decision.UsageSnapshot{Weekly: decision.UsageWindow{Remaining: 0, ResetsAt: week},
		Short:      &decision.UsageWindow{Remaining: 1, ResetsAt: now.Add(time.Hour)},
		Allowances: []decision.AllowanceWindow{{Key: "model:fable:weekly", Remaining: .83, ResetsAt: week}}}
	if got := decision.ReachedLimit(s); got != "weekly" {
		t.Fatalf("limit = %q", got)
	}
	s.Weekly.Remaining, s.Allowances[0].Remaining = .4, 0
	if got := decision.ReachedLimit(s); got != "fable" {
		t.Fatalf("limit = %q", got)
	}
	s.Allowances[0].Remaining = .5
	if got := decision.ReachedLimit(s); got != "" {
		t.Fatalf("limit = %q", got)
	}
	fable := decision.BankedReset{Kind: "fable", Clears: []string{"five_hour", "fable"}}
	if !fable.RefillsLimit("fable") || !fable.RefillsLimit("weekly") || fable.RefillsLimit("opus") {
		t.Fatal("fable refills")
	}
	if (decision.BankedReset{Kind: "full"}).RefillsLimit("opus") || !(decision.BankedReset{Kind: "full"}).RefillsLimit("weekly") {
		t.Fatal("full refills")
	}
}
