package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/croutoncreations/redline/internal/decision"
)

// ListSnapshots returns one provider account's recent snapshots, oldest first.
func (d *DB) ListSnapshots(ctx context.Context, account, provider string, limit int) ([]decision.UsageSnapshot, error) {
	if provider == "" {
		return nil, fmt.Errorf("provider is required")
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, provider_account_id, provider, observed_at, short_remaining,
short_resets_at, weekly_remaining, weekly_resets_at, source, confidence,
banked_resets, banked_resets_expire_at, short_window_unavailable, short_window_not_started,
banked_resets_expiries, plan, source_stale, weekly_unavailable, banked_reset_details
FROM (
    SELECT id, provider_account_id, provider, observed_at, short_remaining, short_resets_at,
           weekly_remaining, weekly_resets_at, source, confidence,
           banked_resets, banked_resets_expire_at, short_window_unavailable, short_window_not_started,
           banked_resets_expiries, plan, source_stale, weekly_unavailable, banked_reset_details
    FROM usage_snapshots WHERE provider_account_id = ? AND provider = ?
    ORDER BY observed_at DESC, id DESC LIMIT ?
) ORDER BY observed_at ASC, id ASC`, account, provider, limit)
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
		var bankedResets sql.NullInt64
		var bankedResetsExpireAt, bankedResetsExpiries, bankedResetDetails sql.NullString
		if err := rows.Scan(
			&id, &snapshot.Account,
			&snapshot.Provider, &observedAt, &shortRemaining, &shortReset,
			&snapshot.Weekly.Remaining, &weeklyReset, &snapshot.Source, &snapshot.Confidence,
			&bankedResets, &bankedResetsExpireAt, &snapshot.ShortWindowUnavailable,
			&snapshot.ShortWindowNotStarted, &bankedResetsExpiries, &snapshot.Plan, &snapshot.SourceStale,
			&snapshot.WeeklyUnavailable, &bankedResetDetails,
		); err != nil {
			return nil, fmt.Errorf("scan usage snapshot: %w", err)
		}
		if err := scanBankedResets(&snapshot, bankedResets, bankedResetsExpireAt, bankedResetDetails); err != nil {
			return nil, err
		}
		if err := scanBankedResetExpiries(&snapshot, bankedResetsExpiries); err != nil {
			return nil, err
		}
		if snapshot.ObservedAt, err = parseStoredTimeField("stored observation time", observedAt); err != nil {
			return nil, err
		}
		if snapshot.Weekly, err = storedWeekly(snapshot.WeeklyUnavailable, snapshot.Weekly.Remaining, weeklyReset); err != nil {
			return nil, err
		}
		if shortRemaining.Valid != shortReset.Valid {
			return nil, fmt.Errorf("stored short window is incomplete")
		}
		if shortRemaining.Valid {
			reset, parseErr := parseStoredTimeField("stored short reset", shortReset.String)
			if parseErr != nil {
				return nil, parseErr
			}
			snapshot.Short = &decision.UsageWindow{Remaining: shortRemaining.Float64, ResetsAt: reset}
		}
		dropContradictoryNotStarted(&snapshot)
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
