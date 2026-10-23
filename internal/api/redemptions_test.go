package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/resetreminder"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/usage"
)

type testRedeemer struct {
	prepare func() nativeusage.RedeemRequest
	redeem  func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error)
}

func (r testRedeemer) Prepare(context.Context, config.Provider, string, bool) (nativeusage.RedeemRequest, error) {
	return r.prepare(), nil
}
func (r testRedeemer) Redeem(_ context.Context, _ config.Provider, req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
	return r.redeem(req)
}

type redemptionNotifier struct{ events []domain.NotificationEvent }

func (n *redemptionNotifier) Notify(_ context.Context, e domain.NotificationEvent) error {
	n.events = append(n.events, e)
	return nil
}

func redemptionServer(t *testing.T, provider string) (*Server, *store.DB, decision.UsageSnapshot) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	usable := true
	pre := decision.UsageSnapshot{Account: "main", Provider: provider, Source: "native", ObservedAt: now, Weekly: decision.UsageWindow{Remaining: .1, ResetsAt: now.Add(24 * time.Hour)}, BankedResetDetails: []decision.BankedReset{{ID: "reset", Claimable: true, UsableNow: &usable, ResetsLeft: 1, ExpiresAt: &expires, Clears: []string{"weekly"}}}}
	if err := db.SaveSnapshot(context.Background(), pre, nil); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{APIToken: "secret", Providers: map[string]config.Provider{"main": {Provider: provider, UsageSource: "native"}}}
	s := NewServerWithDependencies(cfg, db, func() time.Time { return now }, nil, nil)
	source := staticUsageSource{snapshots: map[string]decision.UsageSnapshot{"main": pre}}
	s.usageSources = usage.NewManager(source, source, s.now)
	return s, db, pre
}

func TestRedeemBankedResetRefusesNonDefaultAccounts(t *testing.T) {
	for _, kind := range []string{"profile", "card", "additional profile"} {
		t.Run(kind, func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			p := s.config.Providers["main"]
			p.BankedResets = config.BankedResets{OnExpiry: "redeem", RemindBefore: []string{}}
			s.config.UsageMonitor.Enabled = true
			switch kind {
			case "profile":
				p.ClaudeConfigDir = t.TempDir()
			case "card":
				p.OpenUsageCard = "claude@other"
			case "additional profile":
				s.config.Providers["other"] = config.Provider{Provider: "claude", ClaudeConfigDir: t.TempDir()}
			}
			s.config.Providers["main"] = p
			s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
				t.Fatal("unsafe native credential access")
				return nativeusage.RedeemRequest{}
			}}
			for _, dry := range []bool{false, true} {
				r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7436/v1/providers/main/banked-resets/redeem", strings.NewReader(fmt.Sprintf(`{"dry_run":%t}`, dry)))
				r.Header.Set("Authorization", "Bearer secret")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != http.StatusConflict {
					t.Fatalf("dry=%v: status=%d body=%s", dry, w.Code, w.Body.String())
				}
			}
			handled, err := s.automaticallyRedeemable(t.Context(), "main", p, snapshot)
			if err != nil || len(handled) != 0 {
				t.Fatalf("unsafe automatic selection: %v %v", handled, err)
			}
			if err := s.redeemExpiringBankedReset(t.Context(), "main", p, snapshot); err != nil {
				t.Fatal(err)
			}
			events, err := s.providerResetEvents(t.Context(), "main", snapshot)
			if err != nil || len(events) != 1 || events[0].Source != decision.ResetWeekly {
				t.Fatalf("unsafe reset horizon: %v %v", events, err)
			}
			notifier := &redemptionNotifier{}
			checker := resetreminder.Checker{Enabled: true, Store: db, Notifier: notifier,
				AutoRedeems: func(reset decision.BankedReset) bool { return handled[reset.ID] }}
			if err := checker.Check(t.Context(), "main", p, snapshot, s.now()); err != nil {
				t.Fatal(err)
			}
			if len(notifier.events) != 1 || notifier.events[0].Data["action"] != "redeem_now" {
				t.Fatalf("missing reminder fallback: %+v", notifier.events)
			}
		})
	}
}

func TestRedeemBankedResetPendingRetryRefreshAndNotification(t *testing.T) {
	s, db, pre := redemptionServer(t, "claude")
	ctx := context.Background()
	notifier := &redemptionNotifier{}
	s.notifier = notifier
	post := pre
	post.Weekly.Remaining = .9
	post.ObservedAt = pre.ObservedAt.Add(time.Second)
	now := pre.ObservedAt
	s.now = func() time.Time { return now }
	fresh := freshRedemptionSource{fetch: func() decision.UsageSnapshot {
		now = post.ObservedAt
		return post
	}}
	s.usageSources = usage.NewManager(fresh, fresh, s.now)
	calls := 0
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "reset", RequestID: fmt.Sprint("key", calls)}
	}, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		rows, err := db.BankedResetRedemptions(ctx, "main", 20)
		if err != nil || len(rows) != 1 || rows[0].State != "pending" || rows[0].RequestID != req.RequestID {
			t.Fatalf("pending %+v %v", rows, err)
		}
		if req.RequestID != "key0" {
			t.Errorf("retry changed key: %s", req.RequestID)
		}
		calls++
		if calls == 1 {
			return nativeusage.RedeemResult{}, fmt.Errorf("connection lost")
		}
		return nativeusage.RedeemResult{Outcome: "redeemed", Code: "reset"}, nil
	}}
	if _, err := s.RedeemBankedReset(ctx, "main", "manual", false); err == nil {
		t.Fatal("missing transport error")
	}
	rows, _ := db.BankedResetRedemptions(ctx, "main", 20)
	if rows[0].Outcome != "failed" || rows[0].Reason != "connection lost" || rows[0].State != "pending" {
		t.Fatalf("lost failure details: %+v", rows)
	}
	got, err := s.RedeemBankedReset(ctx, "main", "manual", false)
	if err != nil || !got.Verified || got.ResetID != "reset" || got.RequestID != "key0" {
		t.Fatalf("%+v %v", got, err)
	}
	latest, _, _ := db.LatestSnapshot(ctx, "main", "claude")
	if latest.Weekly.Remaining != .9 {
		t.Fatal(latest)
	}
	if len(notifier.events) != 2 || notifier.events[1].Type != "reset.redeemed" || notifier.events[1].Data["verified"] != "true" {
		t.Fatal(notifier.events)
	}
}

func TestRedeemBankedResetSingleFlightAndAuthRoutes(t *testing.T) {
	s, db, _ := redemptionServer(t, "codex")
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest { return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"} }, redeem: func(req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		close(entered)
		<-release
		return nativeusage.RedeemResult{Outcome: "nothing_to_reset"}, nil
	}}
	go func() { _, err := s.RedeemBankedReset(context.Background(), "main", "manual", false); done <- err }()
	<-entered
	if _, err := s.RedeemBankedReset(context.Background(), "main", "manual", false); !errors.Is(err, store.ErrConflict) {
		t.Errorf("conflict=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"POST", "GET"} {
		path := "/v1/providers/main/banked-resets/redeem"
		if method == "GET" {
			path = "/v1/providers/main/banked-resets/redemptions"
		}
		request := httptest.NewRequest(method, "http://127.0.0.1:7436"+path, strings.NewReader(`{"dry_run":true}`))
		response := httptest.NewRecorder()
		s.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
			t.Fatalf("auth status=%d", response.Code)
		}
	}
	request := httptest.NewRequest("GET", "http://127.0.0.1:7436/v1/providers/main/banked-resets/redemptions", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	var attempts []store.BankedResetRedemption
	_ = json.Unmarshal(response.Body.Bytes(), &attempts)
	if response.Code != 200 || len(attempts) != 1 {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	_ = db
}

func TestRedeemBankedResetSelectionAndSafety(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)
	sooner := now.Add(time.Minute)
	no, yes := false, true
	for _, tc := range []struct {
		name, provider, want string
		details              []decision.BankedReset
	}{
		{"claimable", "claude", "next", []decision.BankedReset{{ID: "other", ResetsLeft: 1}, {ID: "next", ResetsLeft: 1, Claimable: true, UsableNow: &yes}}},
		{"earliest", "codex", "soon", []decision.BankedReset{{ID: "later", ResetsLeft: 1, ExpiresAt: &later}, {ID: "soon", ResetsLeft: 1, ExpiresAt: &sooner}}},
		{"missing details", "codex", "credit details not loaded yet; try again shortly", nil},
		{"unknown usability", "claude", "Claude did not report this reset as usable now", []decision.BankedReset{{ID: "next", ResetsLeft: 1, Claimable: true}}},
		{"not usable", "claude", "Claude did not report this reset as usable now", []decision.BankedReset{{ID: "next", ResetsLeft: 1, Claimable: true, UsableNow: &no}}},
		{"cooldown", "claude", "cooldown", []decision.BankedReset{{ID: "next", ResetsLeft: 1, Claimable: true, CooldownUntil: &later}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectBankedReset(tc.provider, tc.details, now)
			if err != nil {
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatal(err)
				}
			} else if got.ID != tc.want {
				t.Fatal(got)
			}
		})
	}
	s, _, _ := redemptionServer(t, "claude")
	s.usageSources.SkipBankedResetProviders = map[string]bool{"claude": true}
	if _, err := s.RedeemBankedReset(context.Background(), "main", "manual", false); err == nil || !strings.Contains(err.Error(), "multiple accounts") {
		t.Fatal(err)
	}
}
