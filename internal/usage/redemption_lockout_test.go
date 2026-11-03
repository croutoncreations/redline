package usage_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/usage"
)

type redemptionLockoutError struct{ wait time.Duration }

func (e redemptionLockoutError) Error() string             { return "429" }
func (e redemptionLockoutError) RateLimited() bool         { return true }
func (e redemptionLockoutError) RetryAfter() time.Duration { return e.wait }

type redemptionUsageSource struct {
	calls int
	now   time.Time
}

func (*redemptionUsageSource) Name() string { return "native" }
func (s *redemptionUsageSource) Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error) {
	s.calls++
	return decision.UsageSnapshot{Provider: "claude", Source: "native", ObservedAt: s.now, Weekly: decision.UsageWindow{ResetsAt: s.now.Add(time.Hour), Remaining: .5}}, nil, nil
}

func TestRedemptionLockoutUsesUsageFloorAndPersists(t *testing.T) {
	now := time.Now().UTC()
	source := &redemptionUsageSource{now: now}
	manager := usage.NewManager(source, source, func() time.Time { return now })
	path := filepath.Join(t.TempDir(), "lockouts.json")
	manager.SetLockoutPath(path)
	manager.RecordNativeError("Claude", fmt.Errorf("not rate limited"))
	if !manager.NativeBlockedUntil("claude").IsZero() {
		t.Fatal("ordinary error locked out")
	}
	manager.RecordNativeError(" Claude ", redemptionLockoutError{})
	if !manager.NativeBlockedUntil("claude").Equal(now.Add(15 * time.Minute)) {
		t.Fatal("floor not applied")
	}
	manager.RecordNativeError("claude", redemptionLockoutError{wait: time.Hour})
	manager.RecordNativeError("claude", redemptionLockoutError{})
	if !manager.NativeBlockedUntil("CLAUDE").Equal(now.Add(time.Hour)) {
		t.Fatal("lockout shortened")
	}
	restarted := usage.NewManager(source, source, func() time.Time { return now })
	restarted.SetLockoutPath(path)
	_, _, err := restarted.Fetch(t.Context(), "main", config.Provider{Provider: "claude", UsageSource: "native"})
	if err == nil || source.calls != 0 || !restarted.NativeBlockedUntil("claude").Equal(now.Add(time.Hour)) {
		t.Fatalf("calls=%d err=%v", source.calls, err)
	}
}
