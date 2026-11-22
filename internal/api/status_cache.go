package api

import (
	"context"
	"sync"
	"time"

	"github.com/croutoncreations/redline/internal/calibration"
	"github.com/croutoncreations/redline/internal/domain"
)

type statusCacheEntry[V any] struct {
	value     V
	revision  uint64
	loadedAt  time.Time
	expiresAt time.Time
}

// statusCache coalesces concurrent display reads. A canceled reader can leave
// an in-flight load without canceling the reader that owns it. Failed loads
// are never cached. Keys are the server's configured accounts, not user input.
type statusCache[K comparable, V any] struct {
	mu       sync.Mutex
	entries  map[K]statusCacheEntry[V]
	inflight map[K]chan struct{}
}

func (c *statusCache[K, V]) get(ctx context.Context, key K, revision uint64, ttl time.Duration,
	now func() time.Time, load func(context.Context) (V, error),
) (V, error) {
	for {
		if err := ctx.Err(); err != nil {
			var zero V
			return zero, err
		}
		c.mu.Lock()
		at := now()
		if entry, ok := c.entries[key]; ok && entry.revision == revision && !at.Before(entry.loadedAt) && at.Before(entry.expiresAt) {
			c.mu.Unlock()
			return entry.value, nil
		}
		if pending, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				var zero V
				return zero, ctx.Err()
			case <-pending:
				continue
			}
		}
		if c.inflight == nil {
			c.inflight = make(map[K]chan struct{})
		}
		pending := make(chan struct{})
		c.inflight[key] = pending
		c.mu.Unlock()

		value, err := load(ctx)
		c.mu.Lock()
		if err == nil {
			if c.entries == nil {
				c.entries = make(map[K]statusCacheEntry[V])
			}
			c.entries[key] = statusCacheEntry[V]{value: value, revision: revision, loadedAt: at, expiresAt: at.Add(ttl)}
		}
		delete(c.inflight, key)
		close(pending)
		c.mu.Unlock()
		return value, err
	}
}

// Only display summaries use these caches. Health API checks, calibration
// endpoints, scheduler evaluations and admission continue to read fresh data.
// A dashboard's health aggregate can lag by at most one five-second event tick.
func (s *Server) cachedDashboardHealth(ctx context.Context) (domain.OperationalHealth, error) {
	return s.dashboardHealthCache.get(ctx, struct{}{}, 0, 5*time.Second, s.now, func(ctx context.Context) (domain.OperationalHealth, error) {
		return s.store.OperationalHealth(ctx, s.now(), 24*time.Hour)
	})
}

func (s *Server) cachedDashboardCalibration(ctx context.Context, providerID string) (calibration.Estimate, error) {
	return s.dashboardCalibrationCache.get(ctx, providerID, s.store.SnapshotRevision(), 30*time.Second, s.now, func(ctx context.Context) (calibration.Estimate, error) {
		return s.calibration(ctx, providerID)
	})
}
