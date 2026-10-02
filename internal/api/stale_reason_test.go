package api

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/usage"
)

type scriptedUsageSource struct {
	name     string
	snapshot decision.UsageSnapshot
	err      error
}

func (s scriptedUsageSource) Name() string { return s.name }
func (s scriptedUsageSource) Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error) {
	return s.snapshot, nil, s.err
}

// When the last good sample has gone stale, the dashboard error is what every
// surface shows -- web, /m, the phone, the menu bar -- and "usage data is
// stale" alone sends people looking in the wrong place. If collection is
// failing for a known reason, the stale message carries it.
func TestStaleUsageSaysWhyCollectionIsFailing(t *testing.T) {
	now := time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC)
	frozen := decision.UsageSnapshot{
		Provider: "claude", ObservedAt: now.Add(-8 * time.Hour), Source: "openusage", Confidence: "high",
		Weekly: decision.UsageWindow{Remaining: .22, ResetsAt: now.Add(13 * time.Hour)},
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SaveSnapshot(context.Background(), frozen, nil); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ActivePolicy: "standard", MaxSnapshotAge: "15m",
		Providers: map[string]config.Provider{"claude-main": {Provider: "claude", UsageSource: "auto", WindowWeeklyCost: 0.08}},
		Policies: map[string]config.Policy{"standard": {
			TriggerMargin: 0.02, RollingReserve: 0.25,
			PaceThresholds: []config.PaceThreshold{{TimeRemaining: "72h", MinWeeklyRemaining: 0.50}},
		}},
	}
	server := newServer(cfg, db, func() time.Time { return now }, nil, nil, nil)
	// OpenUsage keeps serving its last good sample while Claude Code is signed
	// out; the native fallback then fails for the real reason.
	server.usageSources = usage.NewManager(
		scriptedUsageSource{name: "openusage", snapshot: frozen},
		scriptedUsageSource{name: "native", err: nativeusage.ErrSignedOut},
		func() time.Time { return now },
	)
	provider := cfg.Providers["claude-main"]
	if _, _, err := server.usageSources.Fetch(context.Background(), "claude-main", provider); !errors.Is(err, nativeusage.ErrSignedOut) {
		t.Fatalf("fetch err = %v, want the signed-out error", err)
	}

	data, err := server.dashboardData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claude := data.Providers[0]
	if !claude.SnapshotStale {
		t.Fatalf("provider = %#v, want stale", claude)
	}
	for _, want := range []string{"Usage data is stale", "Claude Code is signed out", "claude auth login"} {
		if !strings.Contains(claude.Error, want) {
			t.Errorf("stale error %q does not say %q", claude.Error, want)
		}
	}
	// Read by people, so no collector plumbing ("native usage source: ...").
	if strings.Contains(strings.ToLower(claude.Error), "usage source") {
		t.Errorf("stale error %q leaks internal source names", claude.Error)
	}
	// And machine-readable, so a client can offer the fix without parsing prose.
	if claude.UsageSource.Reason != usage.ReasonSignedOut {
		t.Errorf("usage_source.reason = %q, want %q", claude.UsageSource.Reason, usage.ReasonSignedOut)
	}
}

// A sign-in that fixes collection clears the reason, both pinned to the
// native source and in auto mode, where OpenUsage keeps serving its frozen
// sample and the native fallback is what recovers.
func TestSignedOutReasonClearsOnRecovery(t *testing.T) {
	now := time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC)
	frozen := decision.UsageSnapshot{Provider: "claude", ObservedAt: now.Add(-8 * time.Hour), Source: "openusage",
		Weekly: decision.UsageWindow{Remaining: .22, ResetsAt: now.Add(time.Hour)}}
	for _, mode := range []string{"native", "auto"} {
		t.Run(mode, func(t *testing.T) {
			native := &toggleSource{err: nativeusage.ErrSignedOut}
			manager := usage.NewManager(scriptedUsageSource{name: "openusage", snapshot: frozen}, native, func() time.Time { return now })
			provider := config.Provider{Provider: "claude", UsageSource: mode}
			_, _, _ = manager.Fetch(context.Background(), "claude-main", provider)
			if got := manager.Status("claude-main").Reason; got != usage.ReasonSignedOut {
				t.Fatalf("reason after sign-out = %q", got)
			}
			native.err = nil
			native.snapshot = decision.UsageSnapshot{Provider: "claude", ObservedAt: now, Source: "native",
				Weekly: decision.UsageWindow{Remaining: .5, ResetsAt: now.Add(time.Hour)}}
			if _, _, err := manager.Fetch(context.Background(), "claude-main", provider); err != nil {
				t.Fatal(err)
			}
			if got := manager.Status("claude-main"); got.Reason != "" || strings.Contains(got.LastError, "signed out") {
				t.Fatalf("status after sign-in = %#v, want the sign-out cleared", got)
			}
		})
	}
}

type toggleSource struct {
	snapshot decision.UsageSnapshot
	err      error
}

func (s *toggleSource) Name() string { return "native" }
func (s *toggleSource) Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error) {
	return s.snapshot, nil, s.err
}

// Staleness reported for a reason Redline has no better words for -- the
// collector saying its own data is stale -- adds nothing, so it is not
// repeated after the stale message.
func TestStaleUsageDoesNotRepeatItself(t *testing.T) {
	if got := staleUsageError("usage snapshot is stale"); got != staleUsageMessage {
		t.Fatalf("got %q", got)
	}
	if got := staleUsageError(""); got != staleUsageMessage {
		t.Fatalf("got %q", got)
	}
}
