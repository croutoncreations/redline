package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/usage"
)

// A profile account reads only its own login: never OpenUsage (whose
// default card is another account), even with no source written.
func TestProfileAccountNeverCallsOpenUsage(t *testing.T) {
	now := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	open := &fakeSource{name: "openusage", results: []sourceResult{{snapshot: claudeSnapshot(now)}}}
	native := &fakeSource{name: "native", results: []sourceResult{{snapshot: claudeSnapshot(now)}}}
	manager := usage.NewManager(open, native, func() time.Time { return now })
	_, _, err := manager.Fetch(context.Background(), "claude-alpha", config.Provider{Provider: "claude", ClaudeConfigDir: "/p/.claude-cc-alpha"})
	if err != nil || open.calls != 0 || native.calls != 1 {
		t.Fatalf("err=%v open=%d native=%d", err, open.calls, native.calls)
	}
}

// Each profile is its own login with its own rate limit: one profile's 429
// must not silence the other profiles or the default login.
func TestOneProfileRateLimitDoesNotSilenceTheOthers(t *testing.T) {
	now := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	native := &fakeSource{name: "native", results: []sourceResult{
		{err: retryAfterError(time.Hour)}, {snapshot: claudeSnapshot(now)}, {snapshot: claudeSnapshot(now)},
	}}
	manager := usage.NewManager(&fakeSource{name: "openusage"}, native, func() time.Time { return now })
	alpha := config.Provider{Provider: "claude", ClaudeConfigDir: "/p/.claude-cc-alpha"}
	if _, _, err := manager.Fetch(context.Background(), "claude-alpha", alpha); err == nil {
		t.Fatal("expected the 429")
	}
	if _, _, err := manager.Fetch(context.Background(), "claude-bravo", config.Provider{Provider: "claude", ClaudeConfigDir: "/p/.claude-cc-bravo"}); err != nil {
		t.Fatalf("another profile was silenced: %v", err)
	}
	if _, _, err := manager.Fetch(context.Background(), "claude-main", config.Provider{Provider: "claude", UsageSource: "native"}); err != nil {
		t.Fatalf("the default login was silenced: %v", err)
	}
	if _, _, err := manager.Fetch(context.Background(), "claude-alpha", alpha); err == nil || native.calls != 3 {
		t.Fatalf("the limited profile was asked again inside its lockout: calls=%d err=%v", native.calls, err)
	}
}

// The default login's reset lookup must never be attached to a profile.
func TestProfileAccountNeverGetsTheDefaultLoginsResets(t *testing.T) {
	now := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	snapshot := claudeSnapshot(now)
	snapshot.Source = "openusage-shaped" // a source that carries no resets
	native := &fakeSource{name: "native", results: []sourceResult{{snapshot: snapshot}}}
	resets := &fakeResets{}
	manager := usage.NewManager(&fakeSource{name: "openusage"}, native, func() time.Time { return now })
	manager.BankedResets = resets
	got, _, err := manager.Fetch(context.Background(), "claude-alpha", config.Provider{Provider: "claude", ClaudeConfigDir: "/p/.claude-cc-alpha"})
	manager.WaitForBankedResetLookups()
	if err != nil || got.BankedResets != nil || resets.calls != 0 {
		t.Fatalf("err=%v resets=%#v lookups=%d", err, got.BankedResets, resets.calls)
	}
}
