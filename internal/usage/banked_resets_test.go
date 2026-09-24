package usage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/usage"
)

type fakeResets struct {
	results []resetResult
	calls   int
}

type resetResult struct {
	resets *decision.BankedResets
	err    error
}

func (f *fakeResets) BankedResets(context.Context, config.Provider) (*decision.BankedResets, error) {
	index := f.calls
	f.calls++
	if index >= len(f.results) {
		return nil, errors.New("unexpected lookup")
	}
	return f.results[index].resets, f.results[index].err
}

func claudeSnapshot(now time.Time) decision.UsageSnapshot {
	s := snapshot("openusage", now)
	s.Provider = "claude"
	return s
}

func repeatSource(s decision.UsageSnapshot, n int) *fakeSource {
	results := make([]sourceResult, n)
	for i := range results {
		results[i] = sourceResult{snapshot: s}
	}
	return &fakeSource{name: "openusage", results: results}
}

// fetch runs one usage fetch and waits for any reset lookup it started, so
// tests see the state the next fetch will observe.
func fetch(t *testing.T, m *usage.Manager, account string, provider config.Provider) decision.UsageSnapshot {
	t.Helper()
	got, _, err := m.Fetch(context.Background(), account, provider)
	if err != nil {
		t.Fatalf("a reset lookup must never fail the usage fetch: %v", err)
	}
	m.WaitForBankedResetLookups()
	return got
}

func autoClaude() config.Provider {
	return config.Provider{Provider: "claude", UsageSource: "auto", OpenUsageURL: "http://x"}
}

// OpenUsage never reports Claude's banked resets, so the manager fills them in
// from a background native lookup, at most once per interval, reusing the
// answer in between.
func TestClaudeSnapshotsAreSupplementedWithCachedBankedResets(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	expires := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	resets := &fakeResets{results: []resetResult{{resets: &decision.BankedResets{Available: 1, NextExpiresAt: &expires}}}}
	manager := usage.NewManager(repeatSource(claudeSnapshot(now), 3), &fakeSource{name: "native"}, func() time.Time { return now })
	manager.BankedResets = resets

	if first := fetch(t, manager, "claude-main", autoClaude()); first.BankedResets != nil {
		t.Fatalf("the first fetch must not wait on the lookup; got %v", *first.BankedResets)
	}
	for i := 0; i < 2; i++ {
		got := fetch(t, manager, "claude-main", autoClaude())
		if got.BankedResets == nil || *got.BankedResets != 1 || got.BankedResetsExpireAt == nil || !got.BankedResetsExpireAt.Equal(expires) {
			t.Fatalf("fetch %d: resets=%v expiry=%v", i, got.BankedResets, got.BankedResetsExpireAt)
		}
	}
	if resets.calls != 1 {
		t.Fatalf("lookups = %d, want 1 within the interval", resets.calls)
	}
}

// A slow lookup must not delay the usage fetch that triggered it.
func TestBankedResetLookupDoesNotBlockTheUsageFetch(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	blocking := blockingResets{release: release}
	manager := usage.NewManager(repeatSource(claudeSnapshot(now), 1), &fakeSource{name: "native"}, func() time.Time { return now })
	manager.BankedResets = blocking
	done := make(chan struct{})
	go func() {
		_, _, _ = manager.Fetch(context.Background(), "claude-main", autoClaude())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("usage fetch waited on the banked reset lookup")
	}
	close(release)
	manager.WaitForBankedResetLookups()
}

type blockingResets struct{ release chan struct{} }

func (b blockingResets) BankedResets(context.Context, config.Provider) (*decision.BankedResets, error) {
	<-b.release
	return nil, nil
}

// A rate-limited lookup must neither fail the usage fetch nor blank a count
// that was known a moment ago, and must back off at least an hour even when
// the server says "retry-after: 0".
func TestBankedResetLookupFailureKeepsLastAnswerAndBacksOff(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	resets := &fakeResets{results: []resetResult{
		{resets: &decision.BankedResets{Available: 1}},
		{err: retryAfterError(0)},
		{resets: &decision.BankedResets{Available: 1}},
	}}
	manager := usage.NewManager(repeatSource(claudeSnapshot(now), 6), &fakeSource{name: "native"}, func() time.Time { return now })
	manager.BankedResets = resets
	manager.MaxSnapshotAge = 48 * time.Hour

	fetch(t, manager, "claude-main", autoClaude())
	now = now.Add(31 * time.Minute)
	got := fetch(t, manager, "claude-main", autoClaude()) // triggers the failing lookup
	if got.BankedResets == nil || *got.BankedResets != 1 {
		t.Fatalf("resets = %v, want the last known 1", got.BankedResets)
	}
	status := manager.Status("claude-main")
	if status.BankedResetsError == "" || status.LastError != "" {
		t.Fatalf("status = %#v: reset lookup error should be reported separately from usage errors", status)
	}
	now = now.Add(45 * time.Minute)
	fetch(t, manager, "claude-main", autoClaude())
	if resets.calls != 2 {
		t.Fatalf("lookups = %d: retried within the hour floor after a 429", resets.calls)
	}
	now = now.Add(20 * time.Minute)
	fetch(t, manager, "claude-main", autoClaude())
	if resets.calls != 3 {
		t.Fatalf("lookups = %d: did not retry after the floor", resets.calls)
	}
}

func TestBankedResetsSkippedWhenPinnedToOpenUsageOrDisabled(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	resets := &fakeResets{}
	manager := usage.NewManager(repeatSource(claudeSnapshot(now), 2), &fakeSource{name: "native"}, func() time.Time { return now })
	manager.BankedResets = resets
	fetch(t, manager, "claude-main", config.Provider{Provider: "claude", UsageSource: "openusage", OpenUsageURL: "http://x"})
	manager.SkipBankedResets = true
	fetch(t, manager, "claude-main", autoClaude())
	if resets.calls != 0 {
		t.Fatalf("lookups = %d, want none", resets.calls)
	}
}

func TestCachedBankedResetsHiddenOnceTheirExpiryPasses(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	expires := now.Add(10 * time.Minute)
	resets := &fakeResets{results: []resetResult{{resets: &decision.BankedResets{Available: 1, NextExpiresAt: &expires}}}}
	manager := usage.NewManager(repeatSource(claudeSnapshot(now), 2), &fakeSource{name: "native"}, func() time.Time { return now })
	manager.BankedResets = resets
	manager.BankedResetInterval = time.Hour
	manager.MaxSnapshotAge = time.Hour
	fetch(t, manager, "claude-main", autoClaude())
	now = now.Add(11 * time.Minute)
	if got := fetch(t, manager, "claude-main", autoClaude()); got.BankedResets != nil {
		t.Fatalf("a lapsed cached reset is still shown: %v", *got.BankedResets)
	}
}

type retryAfterError time.Duration

func (e retryAfterError) Error() string             { return "HTTP 429" }
func (e retryAfterError) RetryAfter() time.Duration { return time.Duration(e) }
func (e retryAfterError) RateLimited() bool         { return true }

// A native 429 must stop further native requests until Retry-After (floored,
// since the endpoint has answered "retry-after: 0" while limiting), and the
// lockout must survive a restart: asking early lengthens the penalty.
func TestNativeRateLimitGatesRequestsAndSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	lockouts := filepath.Join(t.TempDir(), "usage-lockouts.json")
	native := &fakeSource{name: "native", results: []sourceResult{{err: retryAfterError(988 * time.Second)}, {snapshot: claudeSnapshot(now)}}}
	provider := config.Provider{Provider: "claude", UsageSource: "native"}
	manager := usage.NewManager(&fakeSource{name: "openusage"}, native, func() time.Time { return now })
	manager.SetLockoutPath(lockouts)

	if _, _, err := manager.Fetch(context.Background(), "claude-main", provider); err == nil {
		t.Fatal("expected the 429")
	}
	now = now.Add(10 * time.Minute)
	if _, _, err := manager.Fetch(context.Background(), "claude-main", provider); err == nil || native.calls != 1 {
		t.Fatalf("sent a request inside Retry-After: calls=%d err=%v", native.calls, err)
	}

	restarted := usage.NewManager(&fakeSource{name: "openusage"}, native, func() time.Time { return now })
	restarted.SetLockoutPath(lockouts)
	if _, _, err := restarted.Fetch(context.Background(), "claude-main", provider); err == nil || native.calls != 1 {
		t.Fatalf("a restart reopened the lockout: calls=%d err=%v", native.calls, err)
	}
	now = now.Add(7 * time.Minute) // 17m total: past max(988s, 15m floor)
	native.results[1].snapshot = claudeSnapshot(now)
	if _, _, err := restarted.Fetch(context.Background(), "claude-main", provider); err != nil || native.calls != 2 {
		t.Fatalf("did not resume after the lockout: calls=%d err=%v", native.calls, err)
	}
}

// A 429 on the native fetch must also silence the reset lookup, however the
// config spells the provider.
func TestNativeLockoutSilencesResetLookupRegardlessOfProviderCase(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	native := &fakeSource{name: "native", results: []sourceResult{{err: retryAfterError(time.Hour)}}}
	resets := &fakeResets{}
	manager := usage.NewManager(repeatSource(claudeSnapshot(now), 1), native, func() time.Time { return now })
	manager.BankedResets = resets
	_, _, _ = manager.Fetch(context.Background(), "a", config.Provider{Provider: " Claude ", UsageSource: "native"})
	fetch(t, manager, "b", autoClaude())
	if resets.calls != 0 {
		t.Fatalf("reset lookup ignored a native lockout recorded under another spelling: %d calls", resets.calls)
	}
}

// A lockout restored from disk must survive being saved again, or a second
// restart would reopen it.
func TestRestoredResetLockoutSurvivesASecondRestart(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "usage-lockouts.json")
	resets := &fakeResets{results: []resetResult{{err: retryAfterError(2 * time.Hour)}}}
	first := usage.NewManager(repeatSource(claudeSnapshot(now), 1), &fakeSource{name: "native"}, func() time.Time { return now })
	first.BankedResets = resets
	first.SetLockoutPath(path)
	fetch(t, first, "claude-main", autoClaude())

	// Second process: restores the lockout, then saves for an unrelated
	// reason (a native 429 on Codex).
	codexNative := &fakeSource{name: "native", results: []sourceResult{{err: retryAfterError(time.Minute)}}}
	second := usage.NewManager(&fakeSource{name: "openusage"}, codexNative, func() time.Time { return now })
	second.SetLockoutPath(path)
	_, _, _ = second.Fetch(context.Background(), "codex-main", config.Provider{Provider: "codex", UsageSource: "native"})
	saved, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(saved), `"banked_resets":{"claude"`) {
		t.Fatalf("the re-save dropped the restored claude reset lockout: %s (%v)", saved, err)
	}

	now = now.Add(time.Hour)
	third := usage.NewManager(repeatSource(claudeSnapshot(now), 1), &fakeSource{name: "native"}, func() time.Time { return now })
	third.BankedResets = resets
	third.SetLockoutPath(path)
	fetch(t, third, "claude-main", autoClaude())
	if resets.calls != 1 {
		t.Fatalf("lookups = %d: a second restart reopened the reset lockout", resets.calls)
	}
}

func TestZeroRetryAfterStillBacksOff(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	native := &fakeSource{name: "native", results: []sourceResult{{err: retryAfterError(0)}}}
	manager := usage.NewManager(&fakeSource{name: "openusage"}, native, func() time.Time { return now })
	provider := config.Provider{Provider: "claude", UsageSource: "native"}
	_, _, _ = manager.Fetch(context.Background(), "claude-main", provider)
	now = now.Add(time.Minute)
	_, _, _ = manager.Fetch(context.Background(), "claude-main", provider)
	if native.calls != 1 {
		t.Fatalf("calls = %d: retry-after: 0 was treated as permission to retry", native.calls)
	}
}

func TestBankedResetLookupHonorsRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	resets := &fakeResets{results: []resetResult{{err: retryAfterError(3 * time.Hour)}, {resets: &decision.BankedResets{Available: 1}}}}
	manager := usage.NewManager(repeatSource(claudeSnapshot(now), 4), &fakeSource{name: "native"}, func() time.Time { return now })
	manager.BankedResets = resets
	manager.MaxSnapshotAge = 24 * time.Hour

	fetch(t, manager, "claude-main", autoClaude())
	now = now.Add(2 * time.Hour) // past the hour floor, inside Retry-After
	fetch(t, manager, "claude-main", autoClaude())
	if resets.calls != 1 {
		t.Fatalf("lookups = %d; asked again before Retry-After elapsed", resets.calls)
	}
	now = now.Add(time.Hour + time.Minute)
	fetch(t, manager, "claude-main", autoClaude())
	if got := fetch(t, manager, "claude-main", autoClaude()); resets.calls != 2 || got.BankedResets == nil {
		t.Fatalf("lookups = %d resets = %v after Retry-After", resets.calls, got.BankedResets)
	}
}

func TestBankedResetsAreNotLookedUpForCodexOrWhenAlreadyReported(t *testing.T) {
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	reported := claudeSnapshot(now)
	reported.ApplyBankedResets(&decision.BankedResets{Available: 3})
	native := claudeSnapshot(now)
	native.Source = "native"
	resets := &fakeResets{}
	for _, s := range []decision.UsageSnapshot{snapshot("openusage", now), reported, native} {
		manager := usage.NewManager(repeatSource(s, 1), &fakeSource{name: "native"}, func() time.Time { return now })
		manager.BankedResets = resets
		got := fetch(t, manager, "a", config.Provider{Provider: s.Provider, UsageSource: "auto", OpenUsageURL: "http://x"})
		if s.BankedResets != nil && (got.BankedResets == nil || *got.BankedResets != 3) {
			t.Fatalf("reported resets were overwritten: %v", got.BankedResets)
		}
	}
	if resets.calls != 0 {
		t.Fatalf("lookups = %d, want 0", resets.calls)
	}
}
