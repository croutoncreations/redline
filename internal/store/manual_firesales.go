package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type ManualFiresale struct {
	ID                     int64      `json:"-"`
	ProviderAccountID      string     `json:"account"`
	StartedAt              time.Time  `json:"started_at"`
	Deadline               time.Time  `json:"deadline"`
	StartedBy              string     `json:"started_by"`
	BaselineWeeklyResetsAt *time.Time `json:"-"`
}

// StartManualFiresale claims a new sale or replaces an active sale's deadline.
// The original actor, start time and reset baseline survive extensions.
func (d *DB) StartManualFiresale(ctx context.Context, account string, at, deadline time.Time, by string, baseline *time.Time) (ManualFiresale, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return ManualFiresale{}, false, err
	}
	defer tx.Rollback()
	var reset any
	if baseline != nil {
		reset = formatTime(*baseline)
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO manual_firesales(provider_account_id,started_at,deadline,started_by,baseline_weekly_resets_at) VALUES(?,?,?,?,?)`, account, formatTime(at), formatTime(deadline), by, reset)
	if err != nil {
		return ManualFiresale{}, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ManualFiresale{}, false, err
	}
	if n == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE manual_firesales SET deadline=? WHERE provider_account_id=? AND ended_at IS NULL`, formatTime(deadline), account); err != nil {
			return ManualFiresale{}, false, err
		}
	}
	sale, err := scanManualFiresale(tx.QueryRowContext(ctx, manualFiresaleSelect+` WHERE provider_account_id=? AND ended_at IS NULL`, account))
	if err != nil {
		return ManualFiresale{}, false, err
	}
	return sale, n > 0, tx.Commit()
}

func (d *DB) EndManualFiresale(ctx context.Context, id int64, reason string, at time.Time) (bool, error) {
	result, err := d.db.ExecContext(ctx, `UPDATE manual_firesales SET ended_at=?,end_reason=? WHERE id=? AND ended_at IS NULL`, formatTime(at), reason, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

const manualFiresaleSelect = `SELECT id,provider_account_id,started_at,deadline,started_by,baseline_weekly_resets_at FROM manual_firesales`

func scanManualFiresale(row interface{ Scan(...any) error }) (ManualFiresale, error) {
	var sale ManualFiresale
	var started, deadline string
	var baseline sql.NullString
	err := row.Scan(&sale.ID, &sale.ProviderAccountID, &started, &deadline, &sale.StartedBy, &baseline)
	if errors.Is(err, sql.ErrNoRows) {
		return sale, ErrNotFound
	}
	if err != nil {
		return sale, err
	}
	if sale.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
		return sale, err
	}
	if sale.Deadline, err = time.Parse(time.RFC3339Nano, deadline); err != nil {
		return sale, err
	}
	if baseline.Valid {
		reset, err := time.Parse(time.RFC3339Nano, baseline.String)
		if err != nil {
			return sale, err
		}
		sale.BaselineWeeklyResetsAt = &reset
	}
	return sale, nil
}

func (d *DB) ActiveManualFiresales(ctx context.Context) ([]ManualFiresale, error) {
	rows, err := d.db.QueryContext(ctx, manualFiresaleSelect+` WHERE ended_at IS NULL ORDER BY provider_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sales := []ManualFiresale{}
	for rows.Next() {
		sale, err := scanManualFiresale(rows)
		if err != nil {
			return nil, err
		}
		sales = append(sales, sale)
	}
	return sales, rows.Err()
}

func (d *DB) ActiveManualFiresaleFor(ctx context.Context, account string) (ManualFiresale, error) {
	return scanManualFiresale(d.db.QueryRowContext(ctx, manualFiresaleSelect+` WHERE provider_account_id=? AND ended_at IS NULL`, account))
}
