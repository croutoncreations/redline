package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jfox/redline/internal/decision"
)

func (d *DB) ListSnapshots(ctx context.Context, provider string, limit int) ([]decision.UsageSnapshot, error) {
	if provider == "" {
		return nil, fmt.Errorf("provider is required")
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, provider, observed_at, short_remaining,
short_resets_at, weekly_remaining, weekly_resets_at, source, confidence
FROM (
    SELECT id, provider, observed_at, short_remaining, short_resets_at,
           weekly_remaining, weekly_resets_at, source, confidence
    FROM usage_snapshots WHERE provider = ?
    ORDER BY observed_at DESC, id DESC LIMIT ?
) ORDER BY observed_at ASC, id ASC`, provider, limit)
	if err != nil {
		return nil, fmt.Errorf("list usage snapshots: %w", err)
	}
	defer rows.Close()
	snapshots := make([]decision.UsageSnapshot, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var snapshot decision.UsageSnapshot
		var id int64
		var observedAt, weeklyReset string
		var shortRemaining sql.NullFloat64
		var shortReset sql.NullString
		if err := rows.Scan(
			&id,
			&snapshot.Provider, &observedAt, &shortRemaining, &shortReset,
			&snapshot.Weekly.Remaining, &weeklyReset, &snapshot.Source, &snapshot.Confidence,
		); err != nil {
			return nil, fmt.Errorf("scan usage snapshot: %w", err)
		}
		if snapshot.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt); err != nil {
			return nil, fmt.Errorf("parse stored observation time: %w", err)
		}
		if snapshot.Weekly.ResetsAt, err = time.Parse(time.RFC3339Nano, weeklyReset); err != nil {
			return nil, fmt.Errorf("parse stored weekly reset: %w", err)
		}
		if shortRemaining.Valid != shortReset.Valid {
			return nil, fmt.Errorf("stored short window is incomplete")
		}
		if shortRemaining.Valid {
			reset, parseErr := time.Parse(time.RFC3339Nano, shortReset.String)
			if parseErr != nil {
				return nil, fmt.Errorf("parse stored short reset: %w", parseErr)
			}
			snapshot.Short = &decision.UsageWindow{Remaining: shortRemaining.Float64, ResetsAt: reset}
		}
		snapshots = append(snapshots, snapshot)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list usage snapshots: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close usage snapshots: %w", err)
	}
	for index, id := range ids {
		allowances, err := d.loadAllowances(ctx, id)
		if err != nil {
			return nil, err
		}
		snapshots[index].Allowances = allowances
	}
	return snapshots, nil
}
