package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type BankedResetRedemption struct {
	ID                int64      `json:"id"`
	ProviderAccountID string     `json:"provider_account_id"`
	ResetID           string     `json:"reset_id"`
	RequestID         string     `json:"request_id"`
	Trigger           string     `json:"trigger"`
	State             string     `json:"state"`
	Outcome           string     `json:"outcome"`
	Code              string     `json:"code"`
	Reason            string     `json:"reason"`
	CreatedAt         time.Time  `json:"created_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

const redemptionColumns = `id, provider_account_id, reset_id, request_id, trigger, state, outcome, code, reason, created_at, completed_at`

// BeginBankedResetRedemption commits the idempotency key before any provider
// request. An interrupted operation keeps its key even after a process restart.
func (d *DB) BeginBankedResetRedemption(ctx context.Context, attempt BankedResetRedemption) (BankedResetRedemption, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return attempt, err
	}
	defer tx.Rollback()
	existing, err := scanRedemption(tx.QueryRowContext(ctx, `SELECT `+redemptionColumns+` FROM banked_reset_redemptions WHERE provider_account_id=? AND reset_id=? AND (state='pending' OR request_id=?) ORDER BY id DESC LIMIT 1`, attempt.ProviderAccountID, attempt.ResetID, attempt.RequestID))
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return attempt, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO banked_reset_redemptions(provider_account_id,reset_id,request_id,trigger,state,created_at) VALUES(?,?,?,?,'pending',?)`, attempt.ProviderAccountID, attempt.ResetID, attempt.RequestID, attempt.Trigger, formatTime(attempt.CreatedAt))
	if err != nil {
		return attempt, err
	}
	attempt.ID, err = res.LastInsertId()
	if err != nil {
		return attempt, err
	}
	attempt.State = "pending"
	return attempt, tx.Commit()
}

// PendingBankedResetRedemption finds an unresolved real redemption, regardless
// of which reset the latest telemetry would select.
func (d *DB) PendingBankedResetRedemption(ctx context.Context, providerID string) (BankedResetRedemption, error) {
	a, err := scanRedemption(d.db.QueryRowContext(ctx, `SELECT `+redemptionColumns+` FROM banked_reset_redemptions WHERE provider_account_id=? AND state='pending' AND trigger!='dry_run' ORDER BY id LIMIT 1`, providerID))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return a, err
}

// RecordBankedResetRedemptionError preserves the pending key after an ambiguous
// transport failure, while making the failure visible in the attempt history.
func (d *DB) RecordBankedResetRedemptionError(ctx context.Context, id int64, reason string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE banked_reset_redemptions SET outcome='failed',reason=? WHERE id=? AND state='pending'`, reason, id)
	return err
}

func (d *DB) CompleteBankedResetRedemption(ctx context.Context, id int64, outcome, code, reason string, at time.Time) error {
	res, err := d.db.ExecContext(ctx, `UPDATE banked_reset_redemptions SET state='done',outcome=?,code=?,reason=?,completed_at=? WHERE id=?`, outcome, code, reason, formatTime(at), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (d *DB) BankedResetRedemptions(ctx context.Context, providerID string, limit int) ([]BankedResetRedemption, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	return d.redemptions(ctx, `SELECT `+redemptionColumns+` FROM banked_reset_redemptions WHERE provider_account_id=? ORDER BY id DESC LIMIT ?`, providerID, limit)
}

// RedeemedBankedResetRedemptions includes all consumed resets, not just the
// recent history, so stale telemetry cannot make an old credit spendable again.
func (d *DB) RedeemedBankedResetRedemptions(ctx context.Context, providerID string) ([]BankedResetRedemption, error) {
	return d.redemptions(ctx, `SELECT `+redemptionColumns+` FROM banked_reset_redemptions WHERE provider_account_id=? AND state='done' AND outcome='redeemed' AND trigger!='dry_run' ORDER BY id DESC`, providerID)
}

func (d *DB) redemptions(ctx context.Context, query string, args ...any) ([]BankedResetRedemption, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := make([]BankedResetRedemption, 0)
	for rows.Next() {
		a, err := scanRedemption(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}

func scanRedemption(row interface{ Scan(...any) error }) (BankedResetRedemption, error) {
	var a BankedResetRedemption
	var created string
	var completed sql.NullString
	if err := row.Scan(&a.ID, &a.ProviderAccountID, &a.ResetID, &a.RequestID, &a.Trigger, &a.State, &a.Outcome, &a.Code, &a.Reason, &created, &completed); err != nil {
		return a, err
	}
	var err error
	a.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return a, fmt.Errorf("parse redemption timestamp: %w", err)
	}
	if completed.Valid {
		at, err := time.Parse(time.RFC3339Nano, completed.String)
		if err != nil {
			return a, err
		}
		a.CompletedAt = &at
	}
	return a, nil
}
