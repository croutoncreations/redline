package api

import (
	"context"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/resetsignal"
)

func TestResetSignalStartupJitterBeforeFirstPoll(t *testing.T) {
	s, db, _ := redemptionServer(t, "codex")
	cfg := s.config
	cfg.ResetSignals.Enabled = true
	s = newServer(cfg, db, s.now, nil, nil, nil)
	pub := s.now().Add(-time.Hour)
	signal := resetsignal.Signal{Kind: resetsignal.KindPromised, Freshness: resetsignal.Fresh, PublishedAt: &pub, AnnouncementID: "123"}
	calls := installResetSignalFixture(t, s, &signal)
	waits := make(chan time.Duration, 1)
	release := make(chan struct{})
	s.resetSignalSleep = func(ctx context.Context, delay time.Duration) error {
		waits <- delay
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); s.Wait() }()
	s.StartScheduler(ctx)
	select {
	case delay := <-waits:
		if delay < 30*time.Second || delay > 90*time.Second {
			t.Fatalf("startup jitter=%v", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("startup sleeper was not called")
	}
	if calls.Load() != 0 || s.ResetSignalState().LastPollAt != nil {
		t.Fatal("polled before startup delay")
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for s.ResetSignalState().LastPollAt == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls after delay=%d", calls.Load())
	}
}

func TestResetSignalStartupCancellationNeverPolls(t *testing.T) {
	s, db, _ := redemptionServer(t, "codex")
	cfg := s.config
	cfg.ResetSignals.Enabled = true
	s = newServer(cfg, db, s.now, nil, nil, nil)
	signal := resetsignal.Signal{}
	calls := installResetSignalFixture(t, s, &signal)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.StartScheduler(ctx)
	s.Wait()
	if calls.Load() != 0 {
		t.Fatalf("polled cancelled startup: %d", calls.Load())
	}
}
