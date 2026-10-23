package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/scheduler"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/usage"
)

type automaticRateLimitError struct{}

func (automaticRateLimitError) Error() string             { return "HTTP 429" }
func (automaticRateLimitError) RateLimited() bool         { return true }
func (automaticRateLimitError) RetryAfter() time.Duration { return 10 * time.Minute }

// Drive each cycle via the public monitor lifecycle, with a new server each time
// to ensure retry limits, terminal outcomes, and deadlines survive restarts.
func automaticMonitorCycle(t *testing.T, db *store.DB, snapshot decision.UsageSnapshot, now time.Time, mode string, redeemer testRedeemer, notifier *redemptionNotifier, configure ...func(*Server)) {
	t.Helper()
	cfg := config.Config{Providers: map[string]config.Provider{"main": {Provider: snapshot.Provider, UsageSource: "native", BankedResets: config.BankedResets{OnExpiry: mode, RemindBefore: []string{}}}}, UsageMonitor: config.UsageMonitor{Enabled: true, PollInterval: "1h"}, Notifications: config.Notifications{Enabled: true}}
	s := NewServerWithDependencies(cfg, db, func() time.Time { return now }, nil, nil)
	s.redeemer, s.notifier = redeemer, notifier
	snapshot.ObservedAt = now
	source := staticUsageSource{snapshots: map[string]decision.UsageSnapshot{"main": snapshot}}
	s.usageSources = usage.NewManager(source, source, s.now)
	for _, setup := range configure {
		setup(s)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); s.Wait() }()
	s.StartScheduler(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := server.Client().Get(server.URL + "/v1/usage-monitor/status")
		if err != nil {
			t.Fatal(err)
		}
		var status scheduler.Status
		err = json.NewDecoder(response.Body).Decode(&status)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if status.LastCycleAt != nil && !status.Running {
			if len(status.Providers) != 1 || status.Providers[0].Error != "" {
				t.Fatalf("status=%+v", status)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("monitor did not complete")
}

func TestAutomaticRedemptionRetriesAndExpiry(t *testing.T) {
	for _, outcome := range []string{"transport", "429", "failed", "cooldown", "nothing_to_reset", "redeemed", "gone"} {
		t.Run(outcome, func(t *testing.T) {
			_, db, snapshot := redemptionServer(t, "codex")
			now := snapshot.ObservedAt
			calls := 0
			notifier := &redemptionNotifier{}
			r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
				return nativeusage.RedeemRequest{ResetID: "reset", RequestID: fmt.Sprint("key", calls)}
			}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				calls++
				if outcome == "transport" {
					return nativeusage.RedeemResult{}, fmt.Errorf("connection lost")
				}
				if outcome == "429" {
					return nativeusage.RedeemResult{}, automaticRateLimitError{}
				}
				if outcome == "cooldown" {
					until := now.Add(10 * time.Minute)
					return nativeusage.RedeemResult{Outcome: outcome, CooldownUntil: &until}, nil
				}
				return nativeusage.RedeemResult{Outcome: outcome}, nil
			}}
			cycle := func(at time.Time) { automaticMonitorCycle(t, db, snapshot, at, "redeem", r, notifier) }
			// Dispatch pause does not prevent using a reset that would otherwise lapse.
			if err := db.SetProviderPaused(t.Context(), "main", true); err != nil {
				t.Fatal(err)
			}
			cycle(now.Add(-time.Nanosecond))
			if calls != 0 {
				t.Fatal("redeemed before lead")
			}
			cycle(now)
			if outcome == "transport" {
				snapshot.BankedResetDetails = nil
			}
			cycle(now.Add(4 * time.Minute))
			if calls != 1 {
				t.Fatalf("calls=%d, want one", calls)
			}
			cycle(now.Add(5 * time.Minute))
			want := 2
			if outcome == "cooldown" || outcome == "429" || outcome == "nothing_to_reset" || outcome == "redeemed" || outcome == "gone" {
				want = 1
			}
			if calls != want {
				t.Fatalf("calls at 5m=%d want=%d", calls, want)
			}
			cycle(now.Add(30 * time.Minute))
			if outcome == "redeemed" || outcome == "gone" {
				want = 1
			} else {
				want++
			}
			if calls != want {
				t.Fatalf("calls at 30m=%d want=%d", calls, want)
			}
			rows, err := db.BankedResetRedemptions(t.Context(), "main", 20)
			if err != nil || len(rows) == 0 || rows[0].Trigger != "automatic" {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			if outcome == "transport" && (len(rows) != 1 || rows[0].RequestID != "key0") {
				t.Fatalf("pending key changed: %+v", rows)
			}
			// The provider can remove an expired credit from telemetry entirely.
			snapshot.BankedResetDetails = nil
			cycle(now.Add(time.Hour))
			cycle(now.Add(2 * time.Hour))
			if calls != want {
				t.Fatal("attempted after expiry")
			}
			expired := 0
			for _, e := range notifier.events {
				reason := "expired unused"
				if outcome == "transport" || outcome == "429" {
					reason = "redemption request outcome unknown at expiry; check the provider"
				}
				if e.Data["reason"] == reason {
					expired++
					if e.Type != domain.EventResetRedeemFailed {
						t.Fatal(e)
					}
				}
			}
			wantExpired := 1
			if outcome == "redeemed" || outcome == "gone" {
				wantExpired = 0
			}
			if expired != wantExpired {
				t.Fatalf("expired=%d want=%d events=%+v", expired, wantExpired, notifier.events)
			}
			if outcome == "transport" || outcome == "429" {
				rows, err := db.BankedResetRedemptions(t.Context(), "main", 20)
				if err != nil || len(rows) != 1 || rows[0].State != "done" || rows[0].Outcome != "unknown" {
					t.Fatalf("ambiguous expiry rows=%+v err=%v", rows, err)
				}
			}
		})
	}
}

func TestAutomaticRedemptionAdoptsManualPending(t *testing.T) {
	for _, knownExpiry := range []bool{true, false} {
		t.Run(fmt.Sprint("known expiry=", knownExpiry), func(t *testing.T) {
			_, db, snapshot := redemptionServer(t, "codex")
			now := snapshot.ObservedAt
			resetID := "reset"
			if !knownExpiry {
				resetID = "no-history"
			}
			if _, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: resetID, RequestID: "manual-key", Trigger: "manual", CreatedAt: now.Add(-5 * time.Minute)}); err != nil {
				t.Fatal(err)
			}
			snapshot.BankedResetDetails[0].ID = "B"
			calls := 0
			r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
				t.Fatal("prepared another request instead of adopting pending")
				return nativeusage.RedeemRequest{}
			}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				calls++
				if req.ResetID != resetID || req.RequestID != "manual-key" {
					t.Fatalf("req=%+v", req)
				}
				if calls == 1 {
					return nativeusage.RedeemResult{}, fmt.Errorf("lost response")
				}
				return nativeusage.RedeemResult{Outcome: "gone"}, nil
			}}
			for _, delay := range []time.Duration{0, 4 * time.Minute, 5 * time.Minute} {
				automaticMonitorCycle(t, db, snapshot, now.Add(delay), "redeem", r, &redemptionNotifier{})
			}
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
			rows, err := db.AutomaticBankedResetRedemptions(t.Context(), "main")
			if err != nil || len(rows) != 1 || rows[0].ResetID != resetID || rows[0].Outcome != "gone" {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
		})
	}
}

// An expired pending request must be finalized even when an earlier automatic
// attempt recorded a terminal outcome for the same reset; otherwise the pending
// request keeps exclusive ownership and blocks every later reset.
func TestAutomaticRedemptionFinalizesExpiredPendingDespiteTerminalOutcome(t *testing.T) {
	for _, prior := range []string{"gone", "redeemed"} {
		t.Run(prior, func(t *testing.T) {
			_, db, snapshot := redemptionServer(t, "codex")
			now := snapshot.ObservedAt
			expired := now.Add(-time.Minute)
			if err := db.SaveAutomaticBankedResetRedemption(t.Context(), store.AutomaticBankedResetRedemption{ProviderAccountID: "main", ResetID: "A", ExpiresAt: expired, LastAttemptAt: now.Add(-time.Hour), RetryAt: now.Add(-time.Hour), Outcome: prior}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "A", RequestID: "manual-key", Trigger: "manual", CreatedAt: now.Add(-30 * time.Minute)}); err != nil {
				t.Fatal(err)
			}
			later := now.Add(10 * time.Minute)
			snapshot.BankedResetDetails[0].ID = "B"
			snapshot.BankedResetDetails[0].ExpiresAt = &later
			var sent []string
			r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
				return nativeusage.RedeemRequest{ResetID: "B", RequestID: "key-b"}
			}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				sent = append(sent, req.ResetID)
				return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
			}}
			// One second later so this observation is stored rather than
			// deduplicated against the seeded snapshot.
			automaticMonitorCycle(t, db, snapshot, now.Add(time.Second), "redeem", r, &redemptionNotifier{})
			if _, err := db.PendingBankedResetRedemption(t.Context(), "main"); err == nil {
				t.Fatal("expired pending request was not finalized")
			}
			if len(sent) != 1 || sent[0] != "B" {
				t.Fatalf("sent=%v, want the next reset after finalizing A", sent)
			}
		})
	}
}

// When a known-unused reset and an ambiguous request expire at the same time,
// the outcome-unknown warning must not be suppressed by the unused notice.
func TestExpiredBankedResetUnknownWarningWinsSharedExpiry(t *testing.T) {
	s, db, snapshot := redemptionServer(t, "codex")
	now := snapshot.ObservedAt
	expiry := now.Add(-time.Minute)
	notifier := &redemptionNotifier{}
	s.notifier = notifier
	s.config.Notifications = config.Notifications{Enabled: true}
	if _, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "ambiguous", RequestID: "key", Trigger: "automatic", CreatedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []store.AutomaticBankedResetRedemption{
		{ProviderAccountID: "main", ResetID: "unused", ExpiresAt: expiry, LastAttemptAt: now.Add(-time.Hour), RetryAt: now.Add(-time.Hour), Outcome: "nothing_to_reset"},
		{ProviderAccountID: "main", ResetID: "ambiguous", ExpiresAt: expiry, LastAttemptAt: now.Add(-time.Hour), RetryAt: now.Add(-time.Hour), Outcome: "failed"},
	} {
		if err := s.expiredBankedReset(t.Context(), a, now); err != nil {
			t.Fatal(err)
		}
	}
	unknown := 0
	for _, event := range notifier.events {
		if event.Data["outcome"] == "unknown" {
			unknown++
		}
	}
	if unknown != 1 {
		t.Fatalf("events=%+v, want one outcome-unknown warning", notifier.events)
	}
}

func TestExpiredBankedResetUnknownHistoryAPI(t *testing.T) {
	s, db, snapshot := redemptionServer(t, "claude")
	now := snapshot.ObservedAt
	pending, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", RequestID: "key", Trigger: "manual", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.expiredBankedReset(t.Context(), store.AutomaticBankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", ExpiresAt: now.Add(time.Hour)}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1:7436/v1/providers/main/banked-resets/redemptions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, req)
	var rows []store.BankedResetRedemption
	if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(rows) != 1 || rows[0].ID != pending.ID || rows[0].Outcome != "unknown" || rows[0].Reason != "redemption request outcome unknown at expiry; check the provider" {
		t.Fatalf("status=%d rows=%+v", response.Code, rows)
	}
}

func TestAutomaticRedemptionRecoversPendingExpiry(t *testing.T) {
	_, db, snapshot := redemptionServer(t, "codex")
	now := snapshot.ObservedAt
	created := now.Add(-24 * time.Hour)
	expiry := now.Add(2 * time.Hour)
	snapshot.BankedResetDetails[0].ExpiresAt = &expiry
	if _, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", RequestID: "key", Trigger: "manual", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveAutomaticBankedResetRedemption(t.Context(), store.AutomaticBankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", LastAttemptAt: created, RetryAt: created.Add(5 * time.Minute), Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	r := testRedeemer{prepare: func() nativeusage.RedeemRequest { t.Fatal("unexpected prepare"); return nativeusage.RedeemRequest{} }, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		t.Fatal("attempted before recovered lead")
		return nativeusage.RedeemResult{}, nil
	}}
	automaticMonitorCycle(t, db, snapshot, now, "redeem", r, &redemptionNotifier{})
	pending, err := db.PendingBankedResetRedemption(t.Context(), "main")
	if err != nil || pending.ResetID != "reset" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	rows, err := db.AutomaticBankedResetRedemptions(t.Context(), "main")
	if err != nil || len(rows) != 1 || !rows[0].ExpiresAt.Equal(expiry) {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestAutomaticRedemptionUnknownExpiryPendingTimesOut(t *testing.T) {
	_, db, snapshot := redemptionServer(t, "codex")
	now := snapshot.ObservedAt
	if _, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "no-history", RequestID: "old-key", Trigger: "manual", CreatedAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "new-key"}
	}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		calls++
		if req.ResetID != "reset" {
			t.Fatalf("req=%+v", req)
		}
		return nativeusage.RedeemResult{Outcome: "gone"}, nil
	}}
	notifier := &redemptionNotifier{}
	automaticMonitorCycle(t, db, snapshot, now, "redeem", r, notifier)
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	rows, err := db.BankedResetRedemptions(t.Context(), "main", 20)
	if err != nil || len(rows) != 2 || rows[1].Outcome != "unknown" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	found := false
	for _, event := range notifier.events {
		if event.Data["reason"] == "redemption request outcome unknown at expiry; check the provider" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events=%+v", notifier.events)
	}
}

func TestAutomaticRedemptionUsesHorizonCandidate(t *testing.T) {
	_, db, snapshot := redemptionServer(t, "codex")
	now := snapshot.ObservedAt
	later := now.Add(65 * time.Minute)
	second := snapshot.BankedResetDetails[0]
	second.ID = "second"
	second.ExpiresAt = &later
	snapshot.BankedResetDetails = append(snapshot.BankedResetDetails, second)
	if err := db.SaveAutomaticBankedResetRedemption(t.Context(), store.AutomaticBankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", ExpiresAt: *snapshot.BankedResetDetails[0].ExpiresAt, LastAttemptAt: now.Add(-time.Hour), RetryAt: now.Add(30 * time.Minute), Outcome: "nothing_to_reset"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "second", RequestID: "second-key"}
	}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		calls++
		if req.ResetID != "second" {
			t.Fatalf("req=%+v", req)
		}
		return nativeusage.RedeemResult{Outcome: "gone"}, nil
	}}
	automaticMonitorCycle(t, db, snapshot, now.Add(5*time.Minute), "redeem", r, &redemptionNotifier{})
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestAutomaticRedemptionSkipsGoneCreditAndThrottlesProvider(t *testing.T) {
	_, db, snapshot := redemptionServer(t, "codex")
	now := snapshot.ObservedAt
	later := now.Add(65 * time.Minute)
	second := snapshot.BankedResetDetails[0]
	second.ID, second.ExpiresAt = "second", &later
	snapshot.BankedResetDetails = append(snapshot.BankedResetDetails, second)
	calls := 0
	r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
		id := "reset"
		if calls > 0 {
			id = "second"
		}
		return nativeusage.RedeemRequest{ResetID: id, RequestID: id}
	}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		calls++
		return nativeusage.RedeemResult{Outcome: "gone"}, nil
	}}
	for _, at := range []time.Time{now, now.Add(4 * time.Minute), now.Add(5 * time.Minute)} {
		automaticMonitorCycle(t, db, snapshot, at, "redeem", r, &redemptionNotifier{})
	}
	if calls != 2 {
		t.Fatalf("calls=%d want=2", calls)
	}
}

func TestAutomaticRedemptionSafetyAndExpiryNoticeFiltering(t *testing.T) {
	for _, mode := range []string{"demo", "multiple accounts"} {
		t.Run(mode, func(t *testing.T) {
			_, db, snapshot := redemptionServer(t, "claude")
			r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
				t.Fatal("unsafe automatic attempt")
				return nativeusage.RedeemRequest{}
			}}
			automaticMonitorCycle(t, db, snapshot, snapshot.ObservedAt, "redeem", r, &redemptionNotifier{}, func(s *Server) {
				if mode == "demo" {
					s.redemptionDisabled = true
				} else {
					s.usageSources.SkipBankedResetProviders = map[string]bool{"claude": true}
				}
			})
		})
	}
	_, db, snapshot := redemptionServer(t, "claude")
	now := snapshot.ObservedAt
	notifier := &redemptionNotifier{}
	r := testRedeemer{prepare: func() nativeusage.RedeemRequest { return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"} }, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		return nativeusage.RedeemResult{}, fmt.Errorf("offline")
	}}
	automaticMonitorCycle(t, db, snapshot, now, "redeem", r, notifier)
	for _, enabled := range []bool{false, true} {
		automaticMonitorCycle(t, db, snapshot, now.Add(time.Hour), "redeem", r, notifier, func(s *Server) {
			s.config.Notifications.Enabled = enabled
			s.config.Notifications.Events = []string{domain.EventRunStarted}
		})
	}
	claimed, err := db.RecordBankedResetNotice(t.Context(), "main", "reset", now.Add(time.Hour), "expired_unredeemed", now.Add(time.Hour))
	if err != nil || !claimed {
		t.Fatalf("disabled/filtered notice claimed: %v %v", claimed, err)
	}
}

func TestAutomaticRedemptionSelectionAndOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		mutate     func(*decision.BankedReset)
	}{
		{"notify", "notify", func(*decision.BankedReset) {}},
		{"off", "off", func(*decision.BankedReset) {}},
		{"requires limit", "redeem", func(r *decision.BankedReset) { yes := true; r.RequiresLimit = &yes }},
		{"not claimable", "redeem", func(r *decision.BankedReset) { r.Claimable = false }},
		{"not usable", "redeem", func(r *decision.BankedReset) { r.UsableNow = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db, snapshot := redemptionServer(t, "claude")
			tc.mutate(&snapshot.BankedResetDetails[0])
			r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
				t.Fatal("unexpected automatic attempt")
				return nativeusage.RedeemRequest{}
			}}
			automaticMonitorCycle(t, db, snapshot, snapshot.ObservedAt, tc.mode, r, &redemptionNotifier{})
		})
	}
}
