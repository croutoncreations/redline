package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AutomaticBankedResetRedemption retains the retry deadline even if a reset
// disappears from telemetry, or the daemon restarts after a transport failure.
type AutomaticBankedResetRedemption struct {
	ProviderAccountID string
	ResetID           string
	ExpiresAt         time.Time
	LastAttemptAt     time.Time
	RetryAt           time.Time
	Outcome           string
}

// LastBankedResetExpiry recovers metadata for a pending request that vanished
// from telemetry, returning only the last matching detail rather than snapshots.
func (d *DB) LastBankedResetExpiry(ctx context.Context, account, provider, resetID string) (*time.Time, error) {
	var raw string
	err := d.db.QueryRowContext(ctx, `SELECT json_extract(detail.value, '$.expires_at')
FROM usage_snapshots AS snapshot, json_each(snapshot.banked_reset_details) AS detail
WHERE snapshot.provider_account_id=? AND snapshot.provider=? AND json_extract(detail.value, '$.id')=?
AND json_extract(detail.value, '$.expires_at') IS NOT NULL
ORDER BY snapshot.observed_at DESC, snapshot.id DESC LIMIT 1`, account, provider, resetID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	return &at, err
}

func (d *DB) SaveAutomaticBankedResetRedemption(ctx context.Context, a AutomaticBankedResetRedemption) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO automatic_banked_reset_redemptions
(provider_account_id,reset_id,expires_at,last_attempt_at,retry_at,outcome) VALUES(?,?,?,?,?,?)
ON CONFLICT(provider_account_id,reset_id) DO UPDATE SET
expires_at=excluded.expires_at,last_attempt_at=excluded.last_attempt_at,retry_at=excluded.retry_at,outcome=excluded.outcome`,
		a.ProviderAccountID, a.ResetID, formatTime(a.ExpiresAt), formatTime(a.LastAttemptAt), formatTime(a.RetryAt), a.Outcome)
	return err
}

func (d *DB) AutomaticBankedResetRedemptions(ctx context.Context, providerID string) ([]AutomaticBankedResetRedemption, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT reset_id,expires_at,last_attempt_at,retry_at,outcome FROM automatic_banked_reset_redemptions WHERE provider_account_id=? ORDER BY expires_at`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []AutomaticBankedResetRedemption
	for rows.Next() {
		a := AutomaticBankedResetRedemption{ProviderAccountID: providerID}
		var expiry, last, retry string
		if err := rows.Scan(&a.ResetID, &expiry, &last, &retry, &a.Outcome); err != nil {
			return nil, err
		}
		for _, field := range []struct {
			raw string
			at  *time.Time
		}{{expiry, &a.ExpiresAt}, {last, &a.LastAttemptAt}, {retry, &a.RetryAt}} {
			*field.at, err = time.Parse(time.RFC3339Nano, field.raw)
			if err != nil {
				return nil, err
			}
		}
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}
