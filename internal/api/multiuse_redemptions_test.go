package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/usage"
)

func confirmedMultiuseRedemption(t *testing.T, s *Server) nativeusage.RedeemResult {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7436/v1/providers/main/banked-resets/redeem", strings.NewReader(`{"expected_reset_id":"reset"}`))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var result nativeusage.RedeemResult
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRedeemConfirmedMultiuseGrantReflection(t *testing.T) {
	for _, telemetry := range []string{"stale", "decremented", "zero", "missing", "legacy"} {
		t.Run(telemetry, func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			snapshot.ObservedAt = snapshot.ObservedAt.Add(time.Second)
			snapshot.BankedResetDetails[0].ResetsLeft = 2
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			var requests []nativeusage.RedeemRequest
			s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
				return nativeusage.RedeemRequest{ResetID: "reset", RequestID: fmt.Sprintf("key-%d", len(requests))}
			}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				requests = append(requests, req)
				return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
			}}
			if telemetry == "legacy" {
				a, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", RequestID: "legacy", Trigger: "manual", CreatedAt: s.now()})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.CompleteBankedResetRedemption(t.Context(), a.ID, "redeemed", "", "", s.now()); err != nil {
					t.Fatal(err)
				}
			} else if got := confirmedMultiuseRedemption(t, s); got.Replayed || got.Outcome != "redeemed" {
				t.Fatalf("first=%+v", got)
			}
			snapshot.ObservedAt = snapshot.ObservedAt.Add(time.Second)
			switch telemetry {
			case "decremented", "legacy":
				snapshot.BankedResetDetails[0].ResetsLeft = 1
			case "zero":
				snapshot.BankedResetDetails[0].ResetsLeft = 0
			case "missing":
				snapshot.BankedResetDetails = nil
			}
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			got := confirmedMultiuseRedemption(t, s)
			wantCalls := 1
			if telemetry == "decremented" {
				wantCalls = 2
			} else if telemetry == "legacy" {
				wantCalls = 0
			}
			if got.Outcome != "redeemed" || got.Replayed != (telemetry != "decremented") || len(requests) != wantCalls {
				t.Fatalf("result=%+v requests=%+v wantCalls=%d", got, requests, wantCalls)
			}
			if len(requests) == 2 && requests[0].RequestID == requests[1].RequestID {
				t.Fatal("second use reused the first request ID")
			}
			// The latest successful row owns the guard: the same snapshot must
			// not reflect both uses just because it reflects the older row.
			if got := confirmedMultiuseRedemption(t, s); !got.Replayed || len(requests) != wantCalls {
				t.Fatalf("latest redemption not guarded: result=%+v requests=%+v", got, requests)
			}
		})
	}
}

// A decremented count observed at or before the redemption completed may come
// from a different, earlier use. It must not release the guard, even when it
// is the newest stored snapshot.
func TestRedeemMultiuseGrantIgnoresDecrementObservedBeforeCompletion(t *testing.T) {
	for _, offset := range []time.Duration{-time.Second, 0} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			completedAt := snapshot.ObservedAt.Add(10 * time.Second)
			s.now = func() time.Time { return completedAt }
			snapshot.ObservedAt = snapshot.ObservedAt.Add(time.Second)
			snapshot.BankedResetDetails[0].ResetsLeft = 3
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			var requests []nativeusage.RedeemRequest
			s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
				return nativeusage.RedeemRequest{ResetID: "reset", RequestID: fmt.Sprintf("key-%d", len(requests))}
			}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				requests = append(requests, req)
				return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
			}}
			if got := confirmedMultiuseRedemption(t, s); got.Replayed || len(requests) != 1 {
				t.Fatalf("first=%+v requests=%d", got, len(requests))
			}
			// A concurrent observation taken before completion lands afterwards.
			snapshot.ObservedAt = completedAt.Add(offset)
			snapshot.BankedResetDetails[0].ResetsLeft = 2
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			if got := confirmedMultiuseRedemption(t, s); !got.Replayed || len(requests) != 1 {
				t.Fatalf("pre-completion telemetry released the guard: result=%+v requests=%d", got, len(requests))
			}
			// The same decrement observed after completion does release it.
			snapshot.ObservedAt = completedAt.Add(time.Second)
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			if got := confirmedMultiuseRedemption(t, s); got.Replayed || len(requests) != 2 {
				t.Fatalf("post-completion decrement not honoured: result=%+v requests=%d", got, len(requests))
			}
		})
	}
}

// Redemptions without expected_reset_id (CLI --yes, direct API) must apply the
// same completed-use guard: a stale multi-use grant must replay, not spend.
func TestRedeemUnconfirmedMultiuseGrantIsGuarded(t *testing.T) {
	for _, telemetry := range []string{"stale", "decremented"} {
		t.Run(telemetry, func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			snapshot.ObservedAt = snapshot.ObservedAt.Add(time.Second)
			snapshot.BankedResetDetails[0].ResetsLeft = 2
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			var requests []nativeusage.RedeemRequest
			s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
				return nativeusage.RedeemRequest{ResetID: "reset", RequestID: fmt.Sprintf("key-%d", len(requests))}
			}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				requests = append(requests, req)
				return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
			}}
			if got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false); err != nil || got.Replayed || len(requests) != 1 {
				t.Fatalf("first=%+v err=%v requests=%d", got, err, len(requests))
			}
			// Later telemetry, observed after the redemption and its refresh.
			later := s.now().Add(time.Minute)
			s.now = func() time.Time { return later }
			snapshot.ObservedAt = later
			if telemetry == "decremented" {
				snapshot.BankedResetDetails[0].ResetsLeft = 1
			}
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
			if err != nil {
				t.Fatal(err)
			}
			if telemetry == "stale" && (!got.Replayed || len(requests) != 1) {
				t.Fatalf("unconfirmed retry spent a stale grant again: result=%+v requests=%d", got, len(requests))
			}
			if telemetry == "decremented" && (got.Replayed || len(requests) != 2) {
				t.Fatalf("reflected remaining use not spent: result=%+v requests=%d", got, len(requests))
			}
		})
	}
}

// An indeterminate provider answer may follow an applied redemption, so the
// request stays pending and the retry reuses its key instead of a fresh one.
func TestRedeemIndeterminateOutcomeReusesRequestKey(t *testing.T) {
	s, db, _ := redemptionServer(t, "claude")
	var requests []nativeusage.RedeemRequest
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "reset", RequestID: fmt.Sprintf("key-%d", len(requests))}
	}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		requests = append(requests, req)
		if len(requests) == 1 {
			return nativeusage.RedeemResult{Outcome: "failed", Code: "reset_unconfirmed"}, fmt.Errorf("%w: claude returned %q", nativeusage.ErrIndeterminate, "reset_unconfirmed")
		}
		return nativeusage.RedeemResult{Outcome: "redeemed", Code: "already_used"}, nil
	}}
	if _, err := s.RedeemBankedReset(t.Context(), "main", "manual", false); err == nil {
		t.Fatal("indeterminate outcome reported as success")
	}
	if pending, err := db.PendingBankedResetRedemption(t.Context(), "main"); err != nil || pending.RequestID != "key-0" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
	if err != nil || got.Outcome != "redeemed" || len(requests) != 2 || requests[1].RequestID != "key-0" {
		t.Fatalf("retry=%+v err=%v requests=%+v", got, err, requests)
	}
}

func TestAutomaticRedemptionMultiuseGrantReflection(t *testing.T) {
	_, db, snapshot := redemptionServer(t, "claude")
	now := snapshot.ObservedAt.Add(time.Second)
	snapshot.BankedResetDetails[0].ResetsLeft = 2
	var requests []nativeusage.RedeemRequest
	r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "reset", RequestID: fmt.Sprintf("key-%d", len(requests))}
	}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		requests = append(requests, req)
		return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
	}}
	cycle := func() { automaticMonitorCycle(t, db, snapshot, now, "redeem", r, &redemptionNotifier{}) }
	cycle()
	now = now.Add(5 * time.Minute)
	cycle()
	if len(requests) != 1 {
		t.Fatalf("stale snapshot spent twice: %+v", requests)
	}
	snapshot.BankedResetDetails[0].ResetsLeft = 1
	now = now.Add(5 * time.Minute)
	cycle()
	if len(requests) != 2 || requests[0].RequestID == requests[1].RequestID {
		t.Fatalf("remaining use not spent with a fresh key: %+v", requests)
	}
	rows, err := db.BankedResetRedemptions(t.Context(), "main", 20)
	if err != nil || len(rows) != 2 || rows[0].ResetsLeftBefore == nil || *rows[0].ResetsLeftBefore != 1 || rows[1].ResetsLeftBefore == nil || *rows[1].ResetsLeftBefore != 2 {
		t.Fatalf("observed counts not persisted: rows=%+v err=%v", rows, err)
	}
	// Reopening the monitor must consult the latest successful use, not the
	// older row whose before-count was 2.
	now = now.Add(5 * time.Minute)
	cycle()
	if len(requests) != 2 {
		t.Fatalf("latest successful use ignored: %+v", requests)
	}
	for _, missing := range []bool{false, true} {
		snapshot.BankedResetDetails[0].ResetsLeft = 0
		if missing {
			snapshot.BankedResetDetails = nil
		}
		now = now.Add(5 * time.Minute)
		cycle()
		if len(requests) != 2 {
			t.Fatalf("exhausted/missing grant spent again: %+v", requests)
		}
	}
}

func TestAutomaticRedemptionMultiuseGrantUnusedExpiry(t *testing.T) {
	_, db, snapshot := redemptionServer(t, "claude")
	now := snapshot.ObservedAt.Add(time.Second)
	snapshot.BankedResetDetails[0].ResetsLeft = 2
	calls := 0
	r := testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "first"}
	}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		calls++
		return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
	}}
	notifier := &redemptionNotifier{}
	automaticMonitorCycle(t, db, snapshot, now, "redeem", r, notifier)
	snapshot.BankedResetDetails[0].ResetsLeft = 1
	automaticMonitorCycle(t, db, snapshot, now.Add(time.Hour), "redeem", r, notifier)
	unused := 0
	for _, event := range notifier.events {
		if event.Data["reason"] == "expired unused" {
			unused++
		}
	}
	if calls != 1 || unused != 1 {
		t.Fatalf("calls=%d unused=%d events=%+v", calls, unused, notifier.events)
	}
}

func TestMultiuseGrantHorizonReflection(t *testing.T) {
	for _, telemetry := range []string{"stale", "decremented", "zero", "missing", "legacy"} {
		t.Run(telemetry, func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			snapshot.ObservedAt = snapshot.ObservedAt.Add(time.Second)
			snapshot.BankedResetDetails[0].ResetsLeft = 2
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			if telemetry == "legacy" {
				a, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", RequestID: "legacy", Trigger: "manual", CreatedAt: s.now()})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.CompleteBankedResetRedemption(t.Context(), a.ID, "redeemed", "", "", s.now()); err != nil {
					t.Fatal(err)
				}
			} else {
				s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
					return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "first"}
				}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
					return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
				}}
				confirmedMultiuseRedemption(t, s)
			}
			snapshot.ObservedAt = snapshot.ObservedAt.Add(time.Second)
			// Put the automatic action in the future so it forms a horizon.
			expiry := s.now().Add(3 * time.Hour)
			snapshot.BankedResetDetails[0].ExpiresAt = &expiry
			switch telemetry {
			case "decremented", "legacy":
				snapshot.BankedResetDetails[0].ResetsLeft = 1
			case "zero":
				snapshot.BankedResetDetails[0].ResetsLeft = 0
			case "missing":
				snapshot.BankedResetDetails = nil
			}
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			if err := db.SaveAutomaticBankedResetRedemption(t.Context(), store.AutomaticBankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", ExpiresAt: expiry, LastAttemptAt: s.now(), RetryAt: s.now().Add(5 * time.Minute), Outcome: "redeemed"}); err != nil {
				t.Fatal(err)
			}
			provider := s.config.Providers["main"]
			provider.BankedResets.OnExpiry = "redeem"
			provider.WindowWeeklyCost = .08
			s.config.Providers["main"] = provider
			s.config.UsageMonitor.Enabled = true
			s.config.ActivePolicy = "standard"
			s.config.Policies = map[string]config.Policy{"standard": {}}
			s.now = func() time.Time { return snapshot.ObservedAt }
			source := staticUsageSource{snapshots: map[string]decision.UsageSnapshot{"main": snapshot}}
			s.usageSources = usage.NewManager(source, source, s.now)
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7436/v1/providers/main/decision", nil)
			req.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			var got struct {
				Result decision.Result `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := snapshot.Weekly.ResetsAt
			if telemetry == "decremented" {
				want = expiry.Add(-time.Hour)
			}
			if w.Code != http.StatusOK || !got.Result.EffectiveResetAt.Equal(want) {
				t.Fatalf("status=%d body=%s want=%s", w.Code, w.Body, want)
			}
		})
	}
}
