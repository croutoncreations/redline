package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DeviceSession deliberately excludes the credential hash from its public shape.
type DeviceSession struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt time.Time  `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// CreateDeviceSession stores a session issued under the master token whose
// fingerprint is issuer; see AuthenticateDeviceSession.
func (d *DB) CreateDeviceSession(ctx context.Context, session DeviceSession, tokenHash, issuer string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO device_sessions(id,token_hash,name,kind,created_at,last_used_at,issuer) VALUES(?,?,?,?,?,?,?)`, session.ID, tokenHash, session.Name, session.Kind, formatTime(session.CreatedAt), formatTime(session.LastUsedAt), issuer)
	return err
}

// AuthenticateDeviceSession accepts only unrevoked sessions issued under the
// current master token (issuer), so rotating that token invalidates every
// session, including any minted after the rotation by the old process.
// It throttles last_used_at writes in SQL, including concurrent requests.
func (d *DB) AuthenticateDeviceSession(ctx context.Context, tokenHash, issuer string, at time.Time) (string, error) {
	var id string
	err := d.db.QueryRowContext(ctx, `SELECT id FROM device_sessions WHERE token_hash=? AND issuer=? AND revoked_at IS NULL`, tokenHash, issuer).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	result, err := d.db.ExecContext(ctx, `UPDATE device_sessions SET last_used_at=? WHERE id=? AND revoked_at IS NULL AND last_used_at<=?`, formatTime(at), id, formatTime(at.Add(-time.Minute)))
	if err != nil {
		return "", err
	}
	// Recheck revocation when no write was due, so a racing revoke cannot be
	// mistaken for a successful refresh.
	if n, err := result.RowsAffected(); err != nil {
		return "", err
	} else if n == 0 {
		if err := d.db.QueryRowContext(ctx, `SELECT id FROM device_sessions WHERE id=? AND revoked_at IS NULL`, id).Scan(&id); errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		} else if err != nil {
			return "", err
		}
	}
	return id, nil
}

func (d *DB) DeviceSessions(ctx context.Context) ([]DeviceSession, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id,name,kind,created_at,last_used_at,revoked_at FROM device_sessions ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []DeviceSession{}
	for rows.Next() {
		var session DeviceSession
		var created, used string
		var revoked sql.NullString
		if err := rows.Scan(&session.ID, &session.Name, &session.Kind, &created, &used, &revoked); err != nil {
			return nil, err
		}
		if session.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, err
		}
		if session.LastUsedAt, err = time.Parse(time.RFC3339Nano, used); err != nil {
			return nil, err
		}
		if revoked.Valid {
			at, err := time.Parse(time.RFC3339Nano, revoked.String)
			if err != nil {
				return nil, err
			}
			session.RevokedAt = &at
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (d *DB) RevokeDeviceSession(ctx context.Context, id string, at time.Time) error {
	result, err := d.db.ExecContext(ctx, `UPDATE device_sessions SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, formatTime(at), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}
