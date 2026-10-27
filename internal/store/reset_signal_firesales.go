package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/croutoncreations/redline/internal/resetsignal"
)

type ResetSignalFiresale struct {
	ID                int64
	AnnouncementKey   string
	ProviderAccountID string
	Mode              string
	Deadline          time.Time
	StartedAt         time.Time
	Announcement      resetsignal.Announcement
}

// StartResetSignalFiresale claims before delivery, like reset announcements.
// Only a manual start can override a terminal sale, except a config disable.
func (d *DB) StartResetSignalFiresale(ctx context.Context, provider, mode string, a resetsignal.Announcement, now time.Time, manual bool) (bool, error) {
	payload, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	result, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO reset_signal_firesales(announcement_key,provider_account_id,mode,deadline,started_at,announcement_json)
 SELECT ?,?,?,?,?,? WHERE ? OR COALESCE((SELECT end_reason FROM reset_signal_firesales WHERE announcement_key=? AND provider_account_id=? ORDER BY id DESC LIMIT 1),'disabled')='disabled'`, a.Key, provider, mode, formatTime(*a.WindowEnd), formatTime(now), payload, manual, a.Key, provider)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (d *DB) ActiveResetSignalFiresales(ctx context.Context) ([]ResetSignalFiresale, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id,announcement_key,provider_account_id,mode,deadline,started_at,announcement_json FROM reset_signal_firesales WHERE ended_at IS NULL ORDER BY provider_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ResetSignalFiresale
	for rows.Next() {
		var sale ResetSignalFiresale
		var deadline, started string
		var payload []byte
		if err := rows.Scan(&sale.ID, &sale.AnnouncementKey, &sale.ProviderAccountID, &sale.Mode, &deadline, &started, &payload); err != nil {
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
		result = append(result, sale)
	}
	return result, rows.Err()
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
