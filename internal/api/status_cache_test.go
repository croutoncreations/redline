package api

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

type waitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestStatusCacheCoalescesReadersAndHonorsExpiryAndRevision(t *testing.T) {
	var cache statusCache[string, int]
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return at }
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	load := func(context.Context) (int, error) {
		n := int(calls.Add(1))
		if n == 1 {
			close(started)
			<-release
		}
		return n, nil
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			got, err := cache.get(t.Context(), "account", 1, time.Minute, now, load)
			if err != nil || got != 1 {
				t.Errorf("got %d, %v; want shared first load", got, err)
			}
		})
	}
	<-started
	// A reader can cancel while another request owns the load.
	ctx, cancel := context.WithCancel(t.Context())
	waiter := &waitingContext{Context: ctx, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := cache.get(waiter, "account", 1, time.Minute, now, load); done <- err }()
	<-waiter.waiting
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	close(release)
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loads=%d, want 1", calls.Load())
	}
	at = at.Add(time.Minute)
	if got, err := cache.get(t.Context(), "account", 1, time.Minute, now, load); err != nil || got != 2 {
		t.Fatalf("expiry: %d, %v", got, err)
	}
	if got, err := cache.get(t.Context(), "account", 2, time.Minute, now, load); err != nil || got != 3 {
		t.Fatalf("revision: %d, %v", got, err)
	}
	at = at.Add(-2 * time.Minute)
	if got, err := cache.get(t.Context(), "account", 2, time.Minute, now, load); err != nil || got != 4 {
		t.Fatalf("clock moved backwards: %d, %v", got, err)
	}
}

func TestStatusCacheDoesNotCacheFailures(t *testing.T) {
	var cache statusCache[string, int]
	now := time.Now
	_, err := cache.get(t.Context(), "a", 0, time.Minute, now, func(context.Context) (int, error) { return 0, errors.New("unavailable") })
	if err == nil {
		t.Fatal("missing load error")
	}
	got, err := cache.get(t.Context(), "a", 0, time.Minute, now, func(context.Context) (int, error) { return 42, nil })
	if err != nil || got != 42 {
		t.Fatalf("retry: %d, %v", got, err)
	}
}

func TestDashboardCalibrationCacheInvalidatesOnUsageAndLeavesFreshReadsUncached(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	s := &Server{store: db, now: func() time.Time { return at }, config: config.Config{Providers: map[string]config.Provider{"main": {Provider: "claude", WindowWeeklyCost: .2}}}}
	snapshot := decision.UsageSnapshot{Account: "main", Provider: "claude", ObservedAt: at, Source: "native", Confidence: "high", Weekly: decision.UsageWindow{Remaining: .8, ResetsAt: at.Add(7 * 24 * time.Hour)}}
	if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	first, err := s.cachedDashboardCalibration(t.Context(), "main")
	if err != nil || first.SnapshotCount != 1 {
		t.Fatalf("initial: %+v, %v", first, err)
	}
	at = at.Add(time.Second)
	cached, err := s.cachedDashboardCalibration(t.Context(), "main")
	if err != nil || !cached.CalculatedAt.Equal(first.CalculatedAt) {
		t.Fatalf("cache miss: %+v, %v", cached, err)
	}
	fresh, err := s.calibration(t.Context(), "main")
	if err != nil || !fresh.CalculatedAt.Equal(at) {
		t.Fatalf("fresh calibration used display cache: %+v, %v", fresh, err)
	}
	snapshot.ObservedAt = at
	if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	updated, err := s.cachedDashboardCalibration(t.Context(), "main")
	if err != nil || updated.SnapshotCount != 2 || !updated.CalculatedAt.Equal(at) {
		t.Fatalf("new usage not visible: %+v, %v", updated, err)
	}
	// Another account must never receive this account's cached history.
	s.config.Providers["other"] = config.Provider{Provider: "claude", WindowWeeklyCost: .3}
	other, err := s.cachedDashboardCalibration(t.Context(), "other")
	if err != nil || other.SnapshotCount != 0 || other.ConfiguredCost != .3 {
		t.Fatalf("account isolation: %+v, %v", other, err)
	}
}

func TestDashboardHealthCacheIsBoundedAndDoesNotCacheHealthChecks(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	s := &Server{store: db, now: func() time.Time { return at }}
	first, err := s.cachedDashboardHealth(t.Context())
	if err != nil || first.Status != "healthy" {
		t.Fatalf("initial: %+v, %v", first, err)
	}
	_, err = db.RecordDispatchAttempt(t.Context(), domain.DispatchAttempt{ProviderAccountID: "main", Trigger: "automatic", Outcome: domain.DispatchError, Error: "offline", StartedAt: at, CompletedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := db.OperationalHealth(t.Context(), at, 24*time.Hour)
	if err != nil || fresh.Status != "degraded" {
		t.Fatalf("fresh health: %+v, %v", fresh, err)
	}
	at = at.Add(5 * time.Second)
	updated, err := s.cachedDashboardHealth(t.Context())
	if err != nil || updated.Status != "degraded" {
		t.Fatalf("expired summary: %+v, %v", updated, err)
	}
}
