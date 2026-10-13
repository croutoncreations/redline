package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

const primerSchema = `
CREATE TABLE IF NOT EXISTS primer_settings (
    provider_account_id TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 0,
    mode TEXT NOT NULL CHECK(mode IN ('schedule', 'reset')),
    times_json TEXT NOT NULL DEFAULT '[]',
    days_json TEXT NOT NULL DEFAULT '[]',
    timezone TEXT NOT NULL DEFAULT '',
    prompt TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    catch_up_seconds INTEGER NOT NULL DEFAULT 0 CHECK(catch_up_seconds >= 0),
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS primer_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_account_id TEXT NOT NULL,
    trigger TEXT NOT NULL,
    slot_key TEXT NOT NULL,
    target_at TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK(outcome IN ('running', 'fired', 'skipped', 'failed')),
    reason TEXT NOT NULL DEFAULT '',
    output TEXT NOT NULL DEFAULT '',
    verification TEXT NOT NULL DEFAULT '',
    window_resets_at TEXT,
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    verified_at TEXT,
    UNIQUE(provider_account_id, slot_key)
);
DROP INDEX IF EXISTS idx_primer_attempts_provider_completed;
CREATE INDEX IF NOT EXISTS idx_primer_attempts_provider_started
ON primer_attempts(provider_account_id, started_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_primer_attempts_pending
ON primer_attempts(provider_account_id, started_at, id)
WHERE outcome = 'fired' AND verification = 'pending';
`

// PrimerSettings returns the stored primer settings for a provider, or
// ErrNotFound when none were ever saved.
func (d *DB) PrimerSettings(ctx context.Context, provider string) (domain.PrimerSettings, error) {
	row := d.db.QueryRowContext(ctx, `SELECT provider_account_id, enabled, mode, times_json, days_json,
timezone, prompt, model, catch_up_seconds, updated_at FROM primer_settings WHERE provider_account_id = ?`, provider)
	var settings domain.PrimerSettings
	var mode, timesJSON, daysJSON, updatedAt string
	err := row.Scan(&settings.ProviderAccountID, &settings.Enabled, &mode, &timesJSON, &daysJSON,
		&settings.Timezone, &settings.Prompt, &settings.Model, &settings.CatchUpSeconds, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PrimerSettings{}, ErrNotFound
	}
	if err != nil {
		return domain.PrimerSettings{}, fmt.Errorf("read primer settings: %w", err)
	}
	settings.Mode = domain.PrimerMode(mode)
	if err := json.Unmarshal([]byte(timesJSON), &settings.Times); err != nil {
		return domain.PrimerSettings{}, fmt.Errorf("decode primer times: %w", err)
	}
	if err := json.Unmarshal([]byte(daysJSON), &settings.Days); err != nil {
		return domain.PrimerSettings{}, fmt.Errorf("decode primer days: %w", err)
	}
	if settings.UpdatedAt, err = parseStoredTimeField("primer settings update time", updatedAt); err != nil {
		return domain.PrimerSettings{}, err
	}
	return settings, nil
}

func (d *DB) SavePrimerSettings(ctx context.Context, settings domain.PrimerSettings) error {
	if settings.ProviderAccountID == "" || settings.UpdatedAt.IsZero() {
		return fmt.Errorf("primer settings provider and update time are required")
	}
	times, days := settings.Times, settings.Days
	if times == nil {
		times = []string{}
	}
	if days == nil {
		days = []string{}
	}
	timesJSON, err := json.Marshal(times)
	if err != nil {
		return fmt.Errorf("encode primer times: %w", err)
	}
	daysJSON, err := json.Marshal(days)
	if err != nil {
		return fmt.Errorf("encode primer days: %w", err)
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO primer_settings (
provider_account_id, enabled, mode, times_json, days_json, timezone, prompt, model, catch_up_seconds, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider_account_id) DO UPDATE SET enabled = excluded.enabled, mode = excluded.mode,
times_json = excluded.times_json, days_json = excluded.days_json, timezone = excluded.timezone,
prompt = excluded.prompt, model = excluded.model, catch_up_seconds = excluded.catch_up_seconds,
updated_at = excluded.updated_at`,
		settings.ProviderAccountID, settings.Enabled, string(settings.Mode), string(timesJSON), string(daysJSON),
		settings.Timezone, settings.Prompt, settings.Model, settings.CatchUpSeconds, formatTime(settings.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save primer settings: %w", err)
	}
	return nil
}

// ClaimPrimerSlot records the start of an attempt for a slot. It returns
// ErrConflict when the slot was already handled, which is how a slot is kept
// from firing twice, including across restarts.
func (d *DB) ClaimPrimerSlot(ctx context.Context, attempt domain.PrimerAttempt) (int64, error) {
	if attempt.ProviderAccountID == "" || attempt.Trigger == "" || attempt.SlotKey == "" ||
		attempt.Outcome == "" || attempt.TargetAt.IsZero() || attempt.StartedAt.IsZero() {
		return 0, fmt.Errorf("primer attempt provider, trigger, slot, outcome, and times are required")
	}
	completedAt := attempt.CompletedAt
	if completedAt.IsZero() {
		completedAt = attempt.StartedAt
	}
	result, err := d.db.ExecContext(ctx, `INSERT INTO primer_attempts (
provider_account_id, trigger, slot_key, target_at, outcome, reason, output, verification,
window_resets_at, started_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider_account_id, slot_key) DO NOTHING`,
		attempt.ProviderAccountID, attempt.Trigger, attempt.SlotKey, formatTime(attempt.TargetAt),
		string(attempt.Outcome), attempt.Reason, attempt.Output, string(attempt.Verification),
		nullableTime(attempt.WindowResetsAt), formatTime(attempt.StartedAt), formatTime(completedAt))
	if err != nil {
		return 0, fmt.Errorf("record primer attempt: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return 0, fmt.Errorf("record primer attempt: %w", err)
	} else if affected == 0 {
		return 0, ErrConflict
	}
	return result.LastInsertId()
}

// FinishPrimerAttempt stores the terminal outcome of a claimed attempt. It
// returns ErrConflict if the attempt is no longer running, for example
// because a restart already marked it interrupted.
func (d *DB) FinishPrimerAttempt(ctx context.Context, attempt domain.PrimerAttempt) error {
	if attempt.ID == 0 || attempt.CompletedAt.IsZero() {
		return fmt.Errorf("primer attempt id and completion time are required")
	}
	result, err := d.db.ExecContext(ctx, `UPDATE primer_attempts SET outcome = ?, reason = ?, output = ?,
verification = ?, window_resets_at = ?, completed_at = ? WHERE id = ? AND outcome = 'running'`,
		string(attempt.Outcome), attempt.Reason, attempt.Output, string(attempt.Verification),
		nullableTime(attempt.WindowResetsAt), formatTime(attempt.CompletedAt), attempt.ID)
	if err != nil {
		return fmt.Errorf("finish primer attempt: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrConflict
	}
	return nil
}

// VerifyPrimerAttempt records whether the ping was seen to open a window.
// The note replaces the attempt's reason. A verdict already recorded is
// never overwritten; that returns ErrConflict.
func (d *DB) VerifyPrimerAttempt(
	ctx context.Context, id int64, verification domain.PrimerVerification, resetsAt *time.Time, note string, at time.Time,
) error {
	result, err := d.db.ExecContext(ctx, `UPDATE primer_attempts SET verification = ?,
window_resets_at = COALESCE(?, window_resets_at), reason = ?, verified_at = ?
WHERE id = ? AND verification = 'pending'`,
		string(verification), nullableTime(resetsAt), note, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("verify primer attempt: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrConflict
	}
	return nil
}

// FailInterruptedPrimerAttempts marks attempts left running by a previous
// process as failed; the ping may or may not have been sent.
func (d *DB) FailInterruptedPrimerAttempts(ctx context.Context, at time.Time) error {
	_, err := d.db.ExecContext(ctx, `UPDATE primer_attempts SET outcome = 'failed', reason = ?, completed_at = ?
WHERE outcome = 'running'`, domain.PrimerInterruptedReason, formatTime(at))
	if err != nil {
		return fmt.Errorf("fail interrupted primer attempts: %w", err)
	}
	return nil
}

// PrunePrimerAttempts deletes settled attempts older than before, so the
// history does not grow without bound.
func (d *DB) PrunePrimerAttempts(ctx context.Context, before time.Time) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM primer_attempts WHERE completed_at < ?
AND outcome <> 'running' AND verification <> 'pending'`, formatTime(before))
	if err != nil {
		return fmt.Errorf("prune primer attempts: %w", err)
	}
	return nil
}

func (d *DB) ListPrimerAttempts(ctx context.Context, provider string, limit int) ([]domain.PrimerAttempt, error) {
	if provider == "" {
		return nil, fmt.Errorf("provider account id is required")
	}
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}
	return d.queryPrimerAttempts(ctx, `WHERE provider_account_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`, provider, limit)
}

func (d *DB) queryPrimerAttempts(ctx context.Context, clause string, args ...any) ([]domain.PrimerAttempt, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, provider_account_id, trigger, slot_key, target_at, outcome,
reason, output, verification, window_resets_at, started_at, completed_at, verified_at
FROM primer_attempts `+clause, args...)
	if err != nil {
		return nil, fmt.Errorf("list primer attempts: %w", err)
	}
	defer rows.Close()
	attempts := make([]domain.PrimerAttempt, 0)
	for rows.Next() {
		var attempt domain.PrimerAttempt
		var outcome, verification, targetAt, startedAt, completedAt string
		var resetsAt, verifiedAt sql.NullString
		if err := rows.Scan(&attempt.ID, &attempt.ProviderAccountID, &attempt.Trigger, &attempt.SlotKey,
			&targetAt, &outcome, &attempt.Reason, &attempt.Output, &verification, &resetsAt,
			&startedAt, &completedAt, &verifiedAt); err != nil {
			return nil, fmt.Errorf("scan primer attempt: %w", err)
		}
		attempt.Outcome = domain.PrimerOutcome(outcome)
		attempt.Verification = domain.PrimerVerification(verification)
		if attempt.TargetAt, err = parseStoredTimeField("primer target", targetAt); err != nil {
			return nil, err
		}
		if attempt.StartedAt, err = parseStoredTimeField("primer start", startedAt); err != nil {
			return nil, err
		}
		if attempt.CompletedAt, err = parseStoredTimeField("primer completion", completedAt); err != nil {
			return nil, err
		}
		if attempt.WindowResetsAt, err = optionalStoredTime("primer window reset", resetsAt); err != nil {
			return nil, err
		}
		if attempt.VerifiedAt, err = optionalStoredTime("primer verification", verifiedAt); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

// PendingPrimerVerifications lists fired attempts still awaiting a usage check.
func (d *DB) PendingPrimerVerifications(ctx context.Context, provider string) ([]domain.PrimerAttempt, error) {
	return d.queryPrimerAttempts(ctx, `WHERE provider_account_id = ? AND outcome = 'fired'
AND verification = 'pending' ORDER BY started_at, id LIMIT 20`, provider)
}

func optionalStoredTime(field string, value sql.NullString) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := parseStoredTimeField(field, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
