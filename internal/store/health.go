package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jfox/redline/internal/domain"
)

func (d *DB) OperationalHealth(
	ctx context.Context,
	now time.Time,
	window time.Duration,
) (domain.OperationalHealth, error) {
	if window <= 0 {
		return domain.OperationalHealth{}, fmt.Errorf("health window must be positive")
	}
	since := now.UTC().Add(-window)
	health := domain.OperationalHealth{Status: "healthy", Window: window.String(), Since: since}
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs
WHERE state IN ('preparing', 'running')`).Scan(&health.ActiveRuns); err != nil {
		return domain.OperationalHealth{}, fmt.Errorf("count active runs: %w", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT
COALESCE(SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0)
FROM runs WHERE completed_at >= ?`, formatTime(since)).Scan(&health.CompletedRuns, &health.FailedRuns); err != nil {
		return domain.OperationalHealth{}, fmt.Errorf("summarize completed runs: %w", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*),
COALESCE(SUM(CASE WHEN outcome = 'error' AND error NOT LIKE '%context canceled%' THEN 1 ELSE 0 END), 0)
FROM dispatch_attempts WHERE completed_at >= ?`, formatTime(since)).Scan(
		&health.DispatchAttempts, &health.DispatchErrors,
	); err != nil {
		return domain.OperationalHealth{}, fmt.Errorf("summarize dispatch attempts: %w", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_deliveries
WHERE status = 'failed' AND updated_at >= ?`, formatTime(since)).Scan(&health.NotificationFailures); err != nil {
		return domain.OperationalHealth{}, fmt.Errorf("summarize notification failures: %w", err)
	}
	// A failed agent job is a workload outcome, not evidence that the Redline
	// service is unhealthy. Keep the count for run-history visibility, but only
	// degrade operational health when Redline itself cannot dispatch or notify.
	if health.DispatchErrors > 0 || health.NotificationFailures > 0 {
		health.Status = "degraded"
	}
	return health, nil
}
