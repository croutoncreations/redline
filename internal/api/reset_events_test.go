package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/usage"
)

// NewHorizonTestServer supplies telemetry without disabling spending as demo mode does.
func NewHorizonTestServer(cfg config.Config, db *store.DB, now func() time.Time, snapshots map[string]decision.UsageSnapshot) *Server {
	s := NewServerWithDependencies(cfg, db, now, nil, nil)
	source := staticUsageSource{snapshots: snapshots}
	s.usageSources = usage.NewManager(source, source, now)
	return s
}

func TestAutomaticFiresaleUnknownCyclePreservesState(t *testing.T) {
	s, db, snapshot := redemptionServer(t, "codex")
	trigger := .01
	p := s.config.Providers["main"]
	p.WindowWeeklyCost = .1
	s.config.Providers["main"] = p
	s.config.ActivePolicy = "standard"
	s.config.Policies = map[string]config.Policy{"standard": {FiresaleTrigger: &trigger}}
	notifier := &redemptionNotifier{}
	s.notifier = notifier
	snapshot.Weekly.Remaining = .9
	snapshot.Short = &decision.UsageWindow{Remaining: .9, ResetsAt: s.now().Add(time.Hour)}
	setSnapshot := func() {
		source := freshRedemptionSource{fetch: func() decision.UsageSnapshot { return snapshot }}
		s.usageSources = usage.NewManager(source, source, s.now)
		s.usageSources.MaxSnapshotAge = 48 * time.Hour
	}
	cycle := func() schedulerResponse {
		t.Helper()
		setSnapshot()
		response, _, err := s.dispatchCore(t.Context(), schedulerRequest{ProviderAccountID: "main"}, "automatic")
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := cycle()
	if first.Result.Decision != decision.Admit || first.ProviderFiresale == nil || !*first.ProviderFiresale {
		t.Fatalf("initial firesale=%+v", first)
	}
	snapshot.ObservedAt = snapshot.ObservedAt.Add(-24 * time.Hour)
	unknown := cycle()
	if unknown.Result.Decision != decision.Unknown || unknown.ProviderFiresale != nil {
		t.Errorf("unknown cycle=%+v", unknown)
	}
	snapshot.ObservedAt = s.now()
	cycle()
	if len(notifier.events) != 1 || notifier.events[0].Type != domain.EventFiresaleStarted {
		t.Fatalf("transitions=%+v", notifier.events)
	}
	records, err := db.ListSchedulerDecisions(t.Context(), "main", 10)
	if err != nil || len(records) != 3 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func TestProviderResetEventsRetryTiming(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay time.Duration
		want  bool
	}{
		{"provider throttle", 5 * time.Minute, true},
		{"retry deadline", 20 * time.Minute, true},
		{"cooldown", 30 * time.Minute, true},
		{"cooldown past expiry", 2 * time.Hour, false},
		{"lockout", 15 * time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			now := snapshot.ObservedAt
			p := s.config.Providers["main"]
			p.BankedResets.OnExpiry = "redeem"
			s.config.Providers["main"] = p
			s.config.UsageMonitor.Enabled = true
			a := store.AutomaticBankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", ExpiresAt: *snapshot.BankedResetDetails[0].ExpiresAt, LastAttemptAt: now, RetryAt: now, Outcome: "failed"}
			if tc.name == "retry deadline" {
				a.RetryAt = now.Add(tc.delay)
			}
			if tc.name == "cooldown" || tc.name == "cooldown past expiry" {
				until := now.Add(tc.delay)
				snapshot.BankedResetDetails[0].CooldownUntil = &until
			}
			if tc.name == "lockout" {
				s.usageSources.RecordNativeError("claude", automaticRateLimitError{})
			}
			if err := db.SaveAutomaticBankedResetRedemption(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			events, err := s.providerResetEvents(t.Context(), "main", snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want {
				if len(events) != 2 || !events[1].At.Equal(now.Add(tc.delay)) {
					t.Fatalf("events=%+v want=%s", events, now.Add(tc.delay))
				}
			} else if len(events) != 1 {
				t.Fatalf("events=%+v", events)
			}
		})
	}
}

func TestProviderResetEventsPendingPrecedence(t *testing.T) {
	for _, knownExpiry := range []bool{true, false} {
		t.Run(fmt.Sprint("known expiry=", knownExpiry), func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "codex")
			now := snapshot.ObservedAt
			p := s.config.Providers["main"]
			p.BankedResets.OnExpiry = "redeem"
			s.config.Providers["main"] = p
			s.config.UsageMonitor.Enabled = true
			pendingID := "missing"
			if knownExpiry {
				pendingID = "reset"
			}
			if _, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: "main", ResetID: pendingID, RequestID: "key", Trigger: "manual", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			snapshot.BankedResetDetails[0].ID = "B"
			state, err := s.automaticResetState(t.Context(), "main", "codex", snapshot.BankedResetDetails, snapshot.ObservedAt)
			if err != nil {
				t.Fatal(err)
			}
			reset, at := s.nextAutomaticBankedReset(p, state)
			if reset.ID != pendingID || !at.Equal(now.Add(5*time.Minute)) {
				t.Fatalf("reset=%+v at=%s", reset, at)
			}
			events, err := s.providerResetEvents(t.Context(), "main", snapshot)
			if err != nil || len(events) != 1 {
				t.Fatalf("events=%+v err=%v", events, err)
			}
		})
	}
}

func TestMonitorRedeemExpiryNotificationFallsBackWhenAutomaticDisabled(t *testing.T) {
	for _, gate := range []string{"enabled", "multiple accounts", "demo", "monitor disabled"} {
		t.Run(gate, func(t *testing.T) {
			s, _, _ := redemptionServer(t, "claude")
			p := s.config.Providers["main"]
			p.BankedResets = config.BankedResets{OnExpiry: "redeem", RemindBefore: []string{}}
			s.config.Providers["main"] = p
			s.config.UsageMonitor.Enabled = gate != "monitor disabled"
			s.config.Notifications.Enabled = true
			s.redemptionDisabled = gate == "demo"
			if gate == "multiple accounts" {
				s.config.Providers["other"] = p
				s.usageSources.SkipBankedResetProviders = map[string]bool{"claude": true}
			}
			notifier := &redemptionNotifier{}
			s.notifier = notifier
			s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
				return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"}
			}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				return nativeusage.RedeemResult{Outcome: "nothing_to_reset"}, nil
			}}
			if err := s.monitorProvider(t.Context(), "main"); err != nil {
				t.Fatal(err)
			}
			if gate == "enabled" {
				if len(notifier.events) != 0 {
					t.Fatalf("automatic enabled: events=%+v", notifier.events)
				}
			} else if len(notifier.events) != 1 || notifier.events[0].Type != domain.EventResetExpiring || notifier.events[0].Data["action"] != "redeem_now" {
				t.Fatalf("automatic disabled: events=%+v", notifier.events)
			}
		})
	}
}

// A grant automatic redemption will never attempt must not silence the
// redeem-now warning just because redeem mode is configured.
func TestMonitorRedeemModeNotifiesUnredeemableGrant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*decision.BankedReset, time.Time)
		want   int
	}{
		{"usable", func(*decision.BankedReset, time.Time) {}, 0},
		{"unclaimable", func(r *decision.BankedReset, _ time.Time) { r.Claimable = false }, 1},
		{"requires limit", func(r *decision.BankedReset, _ time.Time) { yes := true; r.RequiresLimit = &yes }, 1},
		{"not usable now", func(r *decision.BankedReset, _ time.Time) { no := false; r.UsableNow = &no }, 1},
		{"cooldown past expiry", func(r *decision.BankedReset, now time.Time) {
			until := now.Add(2 * time.Hour)
			r.CooldownUntil = &until
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			p := s.config.Providers["main"]
			p.BankedResets = config.BankedResets{OnExpiry: "redeem", RemindBefore: []string{}}
			s.config.Providers["main"] = p
			s.config.UsageMonitor.Enabled = true
			s.config.Notifications.Enabled = true
			tc.mutate(&snapshot.BankedResetDetails[0], snapshot.ObservedAt)
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			source := staticUsageSource{snapshots: map[string]decision.UsageSnapshot{"main": snapshot}}
			s.usageSources = usage.NewManager(source, source, s.now)
			notifier := &redemptionNotifier{}
			s.notifier = notifier
			s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
				return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"}
			}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
				return nativeusage.RedeemResult{Outcome: "redeemed"}, nil
			}}
			if err := s.monitorProvider(t.Context(), "main"); err != nil {
				t.Fatal(err)
			}
			var expiring []domain.NotificationEvent
			for _, e := range notifier.events {
				if e.Type == domain.EventResetExpiring {
					expiring = append(expiring, e)
				}
			}
			if len(expiring) != tc.want || (tc.want == 1 && expiring[0].Data["action"] != "redeem_now") {
				t.Fatalf("expiring=%+v all=%+v", expiring, notifier.events)
			}
		})
	}
}

func TestProviderResetEventsAutomaticGates(t *testing.T) {
	for _, gate := range []string{"enabled", "notify", "off", "demo", "multiple accounts", "monitor disabled"} {
		t.Run(gate, func(t *testing.T) {
			s, _, snapshot := redemptionServer(t, "claude")
			p := s.config.Providers["main"]
			p.BankedResets.OnExpiry = "redeem"
			if gate == "notify" || gate == "off" {
				p.BankedResets.OnExpiry = gate
			}
			s.config.Providers["main"] = p
			s.config.UsageMonitor.Enabled = gate != "monitor disabled"
			s.redemptionDisabled = gate == "demo"
			s.usageSources.SkipBankedResetProviders = map[string]bool{"claude": gate == "multiple accounts"}
			expiry := snapshot.ObservedAt.Add(3 * time.Hour)
			snapshot.BankedResetDetails[0].ExpiresAt = &expiry
			events, err := s.providerResetEvents(t.Context(), "main", snapshot)
			want := 1
			if gate == "enabled" {
				want = 2
			}
			if err != nil || len(events) != want {
				t.Fatalf("events=%+v err=%v want=%d", events, err, want)
			}
		})
	}
}
