package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PushRegistration never serializes its send token. APIs expose only a suffix.
type PushRegistration struct {
	DeviceSessionID string    `json:"device_session_id"`
	DeviceName      string    `json:"device_name"`
	Platform        string    `json:"platform"`
	Provider        string    `json:"provider"`
	Token           string    `json:"-"`
	TokenSuffix     string    `json:"token_suffix"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (d *DB) UpsertPushRegistration(ctx context.Context, r PushRegistration, at time.Time) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO push_registrations(device_session_id,platform,provider,token,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(device_session_id) DO UPDATE SET platform=excluded.platform,provider=excluded.provider,token=excluded.token,updated_at=excluded.updated_at`, r.DeviceSessionID, r.Platform, r.Provider, r.Token, formatTime(at), formatTime(at))
	return err
}
func (d *DB) DeletePushRegistration(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM push_registrations WHERE device_session_id=?`, id)
	return err
}

// DeletePushRegistrationToken cannot remove a replacement registered during a send.
func (d *DB) DeletePushRegistrationToken(ctx context.Context, id, token string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM push_registrations WHERE device_session_id=? AND token=?`, id, token)
	return err
}

// ActivePushRegistration returns a device's current registration if its
// session is unrevoked and was issued under the current master token. Senders
// use it at send time so a queued wake follows a token refresh.
func (d *DB) ActivePushRegistration(ctx context.Context, issuer, id string) (PushRegistration, bool, error) {
	r := PushRegistration{DeviceSessionID: id}
	err := d.db.QueryRowContext(ctx, `SELECT p.platform,p.provider,p.token FROM push_registrations p JOIN device_sessions d ON d.id=p.device_session_id WHERE p.device_session_id=? AND d.revoked_at IS NULL AND d.issuer=?`, id, issuer).Scan(&r.Platform, &r.Provider, &r.Token)
	if errors.Is(err, sql.ErrNoRows) {
		return PushRegistration{}, false, nil
	}
	if err != nil {
		return PushRegistration{}, false, err
	}
	return r, true, nil
}

func (d *DB) ActivePushRegistrations(ctx context.Context, issuer string) ([]PushRegistration, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT p.device_session_id,d.name,p.platform,p.provider,p.token,p.updated_at FROM push_registrations p JOIN device_sessions d ON d.id=p.device_session_id WHERE d.revoked_at IS NULL AND d.issuer=? ORDER BY p.device_session_id`, issuer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PushRegistration{}
	for rows.Next() {
		var r PushRegistration
		var updated string
		if err := rows.Scan(&r.DeviceSessionID, &r.DeviceName, &r.Platform, &r.Provider, &r.Token, &updated); err != nil {
			return nil, err
		}
		r.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, err
		}
		// Never disclose the entire token, even when it is shorter than the suffix.
		token := []rune(r.Token)
		n := 4
		if len(token) <= n {
			n = len(token) / 2
		}
		r.TokenSuffix = string(token[len(token)-n:])
		result = append(result, r)
	}
	return result, rows.Err()
}
