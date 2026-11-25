package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/croutoncreations/redline/internal/resetsignal"
)

type ResetSignalFiresale struct {
	ID                      int64
	AnnouncementKey         string
	ProviderAccountID       string
	Mode                    string
	Deadline                time.Time
	StartedAt               time.Time
	Announcement            resetsignal.Announcement
	BaselineWeeklyRemaining *float64
	BaselineWeeklyResetsAt  *time.Time
	BaselineObservedAt      *time.Time
}

// StartResetSignalFiresale claims before delivery, like reset announcements.
// Only a manual start can override a terminal sale, except a config disable.
// provider is the account ID the sale belongs to; usageProvider is the key its
// usage snapshots are stored under (the configured provider type, e.g. codex).
func (d *DB) StartResetSignalFiresale(ctx context.Context, provider, usageProvider, mode string, a resetsignal.Announcement, now time.Time, manual bool) (bool, error) {
	payload, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	snapshot, _, err := d.LatestSnapshot(ctx, provider, usageProvider)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	var remaining, resetsAt, observedAt any
	if err == nil {
		remaining, resetsAt, observedAt = snapshot.Weekly.Remaining, formatTime(snapshot.Weekly.ResetsAt), formatTime(snapshot.ObservedAt)
	}
	result, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO reset_signal_firesales(announcement_key,provider_account_id,mode,deadline,started_at,announcement_json,baseline_weekly_remaining,baseline_weekly_resets_at,baseline_observed_at)
 SELECT ?,?,?,?,?,?,?,?,? WHERE ? OR COALESCE((SELECT end_reason FROM reset_signal_firesales WHERE announcement_key=? AND provider_account_id=? ORDER BY id DESC LIMIT 1),'disabled')='disabled'`, a.Key, provider, mode, formatTime(*a.WindowEnd), formatTime(now), payload, remaining, resetsAt, observedAt, manual, a.Key, provider)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (d *DB) ActiveResetSignalFiresales(ctx context.Context) ([]ResetSignalFiresale, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id,announcement_key,provider_account_id,mode,deadline,started_at,announcement_json,baseline_weekly_remaining,baseline_weekly_resets_at,baseline_observed_at FROM reset_signal_firesales WHERE ended_at IS NULL ORDER BY provider_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ResetSignalFiresale
	for rows.Next() {
		var sale ResetSignalFiresale
		var deadline, started string
		var payload []byte
		var baselineReset, baselineObserved sql.NullString
		if err := rows.Scan(&sale.ID, &sale.AnnouncementKey, &sale.ProviderAccountID, &sale.Mode, &deadline, &started, &payload, &sale.BaselineWeeklyRemaining, &baselineReset, &baselineObserved); err != nil {
			return nil, err
		}
		if sale.Deadline, err = time.Parse(time.RFC3339Nano, deadline); err != nil {
			return nil, err
		}
		if sale.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(payload, &sale.Announcement); err != nil {
			return nil, err
		}
		if baselineReset.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, baselineReset.String)
			if err != nil {
				return nil, err
			}
			sale.BaselineWeeklyResetsAt = &parsed
		}
		if baselineObserved.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, baselineObserved.String)
			if err != nil {
				return nil, err
			}
			sale.BaselineObservedAt = &parsed
		}
		result = append(result, sale)
	}
	return result, rows.Err()
}

// LowerResetSignalFiresaleBaseline persists the low-water mark without changing
// the original observation time or reset window.
func (d *DB) LowerResetSignalFiresaleBaseline(ctx context.Context, id int64, remaining float64) error {
	_, err := d.db.ExecContext(ctx, `UPDATE reset_signal_firesales SET baseline_weekly_remaining=? WHERE id=? AND ended_at IS NULL AND baseline_weekly_remaining>?`, remaining, id, remaining)
	return err
}

func (d *DB) EndResetSignalFiresale(ctx context.Context, id int64, reason string, now time.Time) (bool, error) {
	result, err := d.db.ExecContext(ctx, `UPDATE reset_signal_firesales SET ended_at=?,end_reason=? WHERE id=? AND ended_at IS NULL`, formatTime(now), reason, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (d *DB) UpdateResetSignalFiresaleDeadline(ctx context.Context, id int64, a resetsignal.Announcement) (bool, error) {
	payload, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	result, err := d.db.ExecContext(ctx, `UPDATE reset_signal_firesales SET deadline=?,announcement_json=? WHERE id=? AND ended_at IS NULL AND deadline<>?`, formatTime(*a.WindowEnd), payload, id, formatTime(*a.WindowEnd))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}
