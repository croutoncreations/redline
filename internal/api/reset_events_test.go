package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
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
			state, err := s.automaticResetState(t.Context(), "main", "codex", snapshot.BankedResetDetails)
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
