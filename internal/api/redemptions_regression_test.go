package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/usage"
)

func TestRedeemLegacyCookieRequiresResetRedemptionSetting(t *testing.T) {
	s, db, _ := redemptionServer(t, "claude")
	calls := 0
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "redline-dryrun", RequestID: "key", DryRun: true}
	}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		calls++
		return nativeusage.RedeemResult{Outcome: "dry_run_ok"}, nil
	}}
	for _, dry := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7436/v1/providers/main/banked-resets/redeem", strings.NewReader(fmt.Sprintf(`{"dry_run":%t}`, dry)))
		r.AddCookie(&http.Cookie{Name: apiSessionCookie, Value: "secret"})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "api.paired_devices.allow_reset_redemption") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	rows, _ := db.BankedResetRedemptions(t.Context(), "main", 20)
	if calls != 0 || len(rows) != 0 {
		t.Fatalf("calls=%d rows=%+v", calls, rows)
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7436/v1/providers/main/banked-resets/redemptions", nil)
	r.AddCookie(&http.Cookie{Name: apiSessionCookie, Value: "secret"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("history: %d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7436/v1/providers/main/banked-resets/redeem", strings.NewReader(`{"dry_run":true}`))
	r.AddCookie(&http.Cookie{Name: apiSessionCookie, Value: "secret"})
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("bearer: %d %s calls=%d", w.Code, w.Body, calls)
	}
}

func TestPairedDeviceResetRedemptionSetting(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("allow=%t/dry=%t", allow, dry), func(t *testing.T) {
				s, _, _ := redemptionServer(t, "claude")
				s.config.API.PairedDevices.AllowResetRedemption = allow
				calls := 0
				s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
					return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key", DryRun: dry}
				}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
					calls++
					return nativeusage.RedeemResult{Outcome: "nothing_to_reset"}, nil
				}}
				token, _, err := s.newDeviceSession(httptest.NewRequest("GET", "http://localhost", nil), "Phone", "mobile")
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest("POST", "http://localhost/v1/providers/main/banked-resets/redeem", strings.NewReader(fmt.Sprintf(`{"dry_run":%t}`, dry)))
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if !allow && (w.Code != 403 || calls != 0) {
					t.Fatalf("blocked=%d calls=%d %s", w.Code, calls, w.Body)
				}
				if allow && (w.Code != 200 || calls != 1) {
					t.Fatalf("allowed=%d calls=%d %s", w.Code, calls, w.Body)
				}
			})
		}
	}
}

type freshRedemptionSource struct{ fetch func() decision.UsageSnapshot }

func (freshRedemptionSource) Name() string { return "native" }
func (s freshRedemptionSource) Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error) {
	return s.fetch(), nil, nil
}

func TestRedeemVerificationRequiresFreshNativeObservation(t *testing.T) {
	for _, change := range []string{"remaining", "boundary", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			s, db, pre := redemptionServer(t, "codex")
			now := pre.ObservedAt
			s.now = func() time.Time { return now }
			provider := s.config.Providers["main"]
			provider.UsageSource = "openusage"
			s.config.Providers["main"] = provider
			openCalls, nativeCalls := 0, 0
			open := freshRedemptionSource{fetch: func() decision.UsageSnapshot { openCalls++; return pre }}
			native := freshRedemptionSource{fetch: func() decision.UsageSnapshot {
				nativeCalls++
				now = now.Add(time.Second)
				post := pre
				post.ObservedAt = now
				if change == "remaining" {
					post.Weekly.Remaining = .9
				}
				if change == "boundary" {
					post.Weekly.ResetsAt = post.Weekly.ResetsAt.Add(time.Hour)
				}
				return post
			}}
			s.usageSources = usage.NewManager(open, native, s.now)
			s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest { return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"} }, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
			}}
			got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
			if err != nil || got.Verified != (change != "unchanged") || nativeCalls != 1 || openCalls != 0 {
				t.Fatalf("%+v err=%v native=%d open=%d", got, err, nativeCalls, openCalls)
			}
			latest, _, err := db.LatestSnapshot(t.Context(), "main", "codex")
			if err != nil || !latest.ObservedAt.Equal(now) {
				t.Fatalf("snapshot=%+v err=%v", latest, err)
			}
		})
	}
}

func TestRedeemVerificationUsesClearedWindowsAndProviderWarning(t *testing.T) {
	for _, tc := range []struct {
		name, provider, change string
		clears, cleared        []string
		wantVerified           bool
	}{
		{"claude session refilled", "claude", "short remaining", []string{"five_hour"}, nil, true},
		{"claude session moved", "claude", "short boundary", []string{"five_hour"}, nil, true},
		{"response session refilled", "claude", "short remaining", nil, []string{"five_hour"}, true},
		{"claude unverified", "claude", "unchanged", []string{"five_hour"}, nil, false},
		{"codex unverified", "codex", "unchanged", []string{"weekly"}, nil, false},
		{"weekly also cleared", "claude", "short remaining", []string{"five_hour", "weekly"}, nil, false},
		{"response clears weekly", "claude", "short remaining", []string{"five_hour"}, []string{"weekly"}, false},
		{"unknown clears uses weekly", "claude", "weekly", nil, nil, true},
		{"missing session", "claude", "missing short", []string{"five_hour"}, nil, false},
		{"stale session refill", "claude", "stale short", []string{"five_hour"}, nil, false},
		{"pending session refilled", "claude", "pending", []string{"five_hour"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, pre := redemptionServer(t, tc.provider)
			pre.ObservedAt = pre.ObservedAt.Add(time.Second)
			now := pre.ObservedAt
			s.now = func() time.Time { return now }
			pre.Short = &decision.UsageWindow{Remaining: .1, ResetsAt: pre.ObservedAt.Add(time.Hour)}
			pre.BankedResetDetails[0].Clears = tc.clears
			if err := db.SaveSnapshot(t.Context(), pre, nil); err != nil {
				t.Fatal(err)
			}
			post := pre
			short := *pre.Short
			post.Short = &short
			post.ObservedAt = pre.ObservedAt.Add(time.Second)
			switch tc.change {
			case "short remaining", "stale short", "pending":
				post.Short.Remaining = .9
			case "short boundary":
				post.Short.ResetsAt = post.Short.ResetsAt.Add(time.Hour)
			case "weekly":
				post.Weekly.Remaining = .9
			case "missing short":
				post.Short = nil
				post.Weekly.Remaining = .9
			}
			if tc.change == "stale short" {
				post.ObservedAt = pre.ObservedAt
			}
			source := freshRedemptionSource{fetch: func() decision.UsageSnapshot {
				now = post.ObservedAt
				return post
			}}
			s.usageSources = usage.NewManager(source, source, s.now)
			s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
				return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"}
			}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				return nativeusage.RedeemResult{Outcome: "redeemed", Cleared: tc.cleared}, nil
			}}
			if tc.change == "pending" {
				if _, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", RequestID: "key", Trigger: "manual", CreatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
			wantCodexWarning := tc.provider == "codex" && !tc.wantVerified
			if err != nil || got.Verified != tc.wantVerified || strings.Contains(got.Reason, "Codex may still enforce the old window (known provider bug)") != wantCodexWarning {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
}

type selectingRedeemer struct{ testRedeemer }

func (r selectingRedeemer) Prepare(_ context.Context, _ config.Provider, resetID string, _ bool) (nativeusage.RedeemRequest, error) {
	return nativeusage.RedeemRequest{ResetID: resetID, RequestID: "key-" + resetID}, nil
}

func TestRedeemCodexExcludesConsumedCredits(t *testing.T) {
	s, db, pre := redemptionServer(t, "codex")
	a, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", RequestID: "key-reset", Trigger: "manual", CreatedAt: s.now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteBankedResetRedemption(t.Context(), a.ID, "redeemed", "reset", "", s.now()); err != nil {
		t.Fatal(err)
	}
	later := s.now().Add(2 * time.Hour)
	pre.ObservedAt = pre.ObservedAt.Add(time.Second)
	pre.BankedResetDetails = append(pre.BankedResetDetails, decision.BankedReset{ID: "other", ResetsLeft: 1, ExpiresAt: &later})
	if err := db.SaveSnapshot(t.Context(), pre, nil); err != nil {
		t.Fatal(err)
	}
	s.redeemer = selectingRedeemer{testRedeemer{redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		if req.ResetID != "other" {
			t.Errorf("selected consumed credit: %+v", req)
		}
		return nativeusage.RedeemResult{Outcome: "nothing_to_reset"}, nil
	}}}
	got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
	if err != nil || got.ResetID != "other" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRedeemCompletedCodexIsNotSentAgain(t *testing.T) {
	s, db, pre := redemptionServer(t, "codex")
	notifier := &redemptionNotifier{}
	s.notifier = notifier
	calls := 0
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"}
	}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		calls++
		return nativeusage.RedeemResult{Outcome: "redeemed", Code: "reset"}, nil
	}}
	first, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
	if err != nil || first.Outcome != "redeemed" {
		t.Fatalf("%+v %v", first, err)
	}
	before, _ := db.BankedResetRedemptions(t.Context(), "main", 20)
	// The source still reports the consumed credit.
	if err := db.SaveSnapshot(t.Context(), pre, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.redeemBankedResetExpected(t.Context(), "main", "manual", false, "other"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("replay confirmation guard: %v", err)
	}
	got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
	if err != nil || got.Outcome != "redeemed" {
		t.Fatalf("%+v %v", got, err)
	}
	encoded, _ := json.Marshal(got)
	var body map[string]any
	_ = json.Unmarshal(encoded, &body)
	if body["replayed"] != true || body["completed_at"] == nil {
		t.Errorf("missing replay metadata: %s", encoded)
	}
	if _, ok := body["verified"]; ok {
		t.Errorf("replay must not claim a new verification: %s", encoded)
	}
	after, _ := db.BankedResetRedemptions(t.Context(), "main", 20)
	if calls != 1 || len(notifier.events) != 1 || !reflect.DeepEqual(before, after) {
		t.Fatalf("calls=%d events=%+v before=%+v after=%+v", calls, notifier.events, before, after)
	}
}

// A confirmed retry of a reset that already redeemed must replay the stored
// result for both providers, whatever current telemetry now lists.
func TestRedeemConfirmedRetryReplaysCompletedResetRegardlessOfTelemetry(t *testing.T) {
	for _, providerName := range []string{"claude", "codex"} {
		for _, telemetry := range []string{"dropped", "different"} {
			t.Run(providerName+"/"+telemetry, func(t *testing.T) {
				s, db, pre := redemptionServer(t, providerName)
				a, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", RequestID: "key", Trigger: "manual", CreatedAt: s.now()})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.CompleteBankedResetRedemption(t.Context(), a.ID, "redeemed", "reset", "", s.now()); err != nil {
					t.Fatal(err)
				}
				later := pre
				later.ObservedAt = pre.ObservedAt.Add(time.Second)
				later.BankedResetDetails = nil
				if telemetry == "different" {
					usable := true
					later.BankedResetDetails = []decision.BankedReset{{ID: "other", ResetsLeft: 1, UsableNow: &usable, Claimable: true, ExpiresAt: pre.BankedResetsExpireAt}}
				}
				if err := db.SaveSnapshot(t.Context(), later, nil); err != nil {
					t.Fatal(err)
				}
				notifier := &redemptionNotifier{}
				s.notifier = notifier
				s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
					t.Fatal("prepared a new spending request for a completed reset")
					return nativeusage.RedeemRequest{}
				}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
					t.Fatal("re-sent a completed reset")
					return nativeusage.RedeemResult{}, nil
				}}
				before, _ := db.BankedResetRedemptions(t.Context(), "main", 20)
				got, err := s.redeemBankedResetExpected(t.Context(), "main", "manual", false, "reset")
				if err != nil || !got.Replayed || got.Outcome != "redeemed" || got.ResetID != "reset" {
					t.Fatalf("%+v %v", got, err)
				}
				after, _ := db.BankedResetRedemptions(t.Context(), "main", 20)
				if len(notifier.events) != 0 || !reflect.DeepEqual(before, after) {
					t.Fatalf("events=%+v history=%+v", notifier.events, after)
				}
			})
		}
	}
}

func TestRedeemPendingDryRunDoesNotRerouteRealRedemption(t *testing.T) {
	s, db, _ := redemptionServer(t, "codex")
	dry, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "redline-dryrun-unused", RequestID: "dry-key", Trigger: "dry_run", CreatedAt: s.now()})
	if err != nil {
		t.Fatal(err)
	}
	s.redeemer = selectingRedeemer{testRedeemer{redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		if req.ResetID != "reset" || req.RequestID == dry.RequestID || req.DryRun {
			t.Errorf("dry run rerouted real redemption: %+v", req)
		}
		return nativeusage.RedeemResult{Outcome: "nothing_to_reset"}, nil
	}}}
	got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
	if err != nil || got.ResetID != "reset" {
		t.Fatalf("%+v %v", got, err)
	}
	rows, err := db.BankedResetRedemptions(t.Context(), "main", 20)
	if err != nil || len(rows) != 2 || rows[0].State != "done" || rows[1].ID != dry.ID || rows[1].State != "pending" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestRedeemCompletedRequestIsReplayed(t *testing.T) {
	s, db, _ := redemptionServer(t, "claude")
	a, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", RequestID: "key", Trigger: "manual", CreatedAt: s.now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteBankedResetRedemption(t.Context(), a.ID, "redeemed", "already_used", "stored reason", s.now()); err != nil {
		t.Fatal(err)
	}
	notifier := &redemptionNotifier{}
	s.notifier = notifier
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"}
	}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		t.Fatal("re-sent completed request")
		return nativeusage.RedeemResult{}, nil
	}}
	before, _ := db.BankedResetRedemptions(t.Context(), "main", 20)
	got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
	if err != nil || !got.Replayed || got.Code != "already_used" || got.Reason != "stored reason" || got.CompletedAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	after, _ := db.BankedResetRedemptions(t.Context(), "main", 20)
	if len(notifier.events) != 0 || !reflect.DeepEqual(before, after) {
		t.Fatalf("events=%+v history=%+v", notifier.events, after)
	}
}

func TestRedeemPendingPrecedesSelection(t *testing.T) {
	for _, selection := range []string{"different", "empty"} {
		t.Run(selection, func(t *testing.T) {
			s, db, pre := redemptionServer(t, "codex")
			calls, prepares := 0, 0
			s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
				prepares++
				id := "reset"
				if prepares > 1 {
					id = "other"
				}
				return nativeusage.RedeemRequest{ResetID: id, RequestID: fmt.Sprintf("key%d", prepares)}
			}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				calls++
				if req.ResetID != "reset" || req.RequestID != "key1" {
					t.Errorf("retried wrong operation: %+v", req)
				}
				if calls == 1 {
					return nativeusage.RedeemResult{}, fmt.Errorf("timeout")
				}
				return nativeusage.RedeemResult{Outcome: "nothing_to_reset"}, nil
			}}
			if _, err := s.RedeemBankedReset(t.Context(), "main", "manual", false); err == nil {
				t.Fatal("missing timeout")
			}
			pre.ObservedAt = pre.ObservedAt.Add(time.Second)
			pre.BankedResetDetails = nil
			if selection == "different" {
				pre.BankedResetDetails = []decision.BankedReset{{ID: "other", ResetsLeft: 1}}
			}
			if err := db.SaveSnapshot(t.Context(), pre, nil); err != nil {
				t.Fatal(err)
			}
			_, err := s.redeemBankedResetExpected(t.Context(), "main", "manual", false, "other")
			if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "previous redemption of reset is unresolved") {
				t.Fatalf("guard: %v", err)
			}
			got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
			if err != nil || got.ResetID != "reset" || calls != 2 || prepares != 1 {
				t.Fatalf("got=%+v err=%v calls=%d prepares=%d", got, err, calls, prepares)
			}
			rows, _ := db.BankedResetRedemptions(context.Background(), "main", 20)
			if len(rows) != 1 || rows[0].State != "done" {
				t.Fatal(rows)
			}
		})
	}
}
