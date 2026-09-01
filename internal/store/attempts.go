package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jfox/redline/internal/domain"
)

func (d *DB) RecordDispatchAttempt(ctx context.Context, attempt domain.DispatchAttempt) (int64, error) {
	if attempt.ProviderAccountID == "" || attempt.Trigger == "" || attempt.Outcome == "" ||
		attempt.StartedAt.IsZero() || attempt.CompletedAt.IsZero() {
		return 0, fmt.Errorf("dispatch attempt provider, trigger, outcome, and timestamps are required")
	}
	var requestedTaskID, selectedTaskID, runID any
	if attempt.RequestedTaskID != "" {
		requestedTaskID = attempt.RequestedTaskID
	}
	if attempt.SelectedTaskID != "" {
		selectedTaskID = attempt.SelectedTaskID
	}
	if attempt.RunID != "" {
		runID = attempt.RunID
	}
	result, err := d.db.ExecContext(ctx, `INSERT INTO dispatch_attempts (
provider_account_id, trigger, outcome, decision, mode, reason,
requested_task_id, selected_task_id, run_id, error, started_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ProviderAccountID, attempt.Trigger, attempt.Outcome, attempt.Decision,
		attempt.Mode, attempt.Reason, requestedTaskID, selectedTaskID, runID, attempt.Error,
		formatTime(attempt.StartedAt), formatTime(attempt.CompletedAt),
	)
	if err != nil {
		return 0, fmt.Errorf("record dispatch attempt: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read dispatch attempt id: %w", err)
	}
	return id, nil
}

func (d *DB) ListDispatchAttempts(ctx context.Context, providerAccountID string, limit int) ([]domain.DispatchAttempt, error) {
	if providerAccountID == "" {
		return nil, fmt.Errorf("provider account id is required")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, provider_account_id, trigger, outcome,
decision, mode, reason, requested_task_id, selected_task_id, run_id, error, started_at, completed_at
FROM dispatch_attempts WHERE provider_account_id = ?
ORDER BY completed_at DESC, id DESC LIMIT ?`, providerAccountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list dispatch attempts: %w", err)
	}
	defer rows.Close()
	attempts := make([]domain.DispatchAttempt, 0)
	for rows.Next() {
		attempt, err := scanDispatchAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func (d *DB) ListDispatchAttemptsRange(
	ctx context.Context, trigger string, since, until time.Time,
) ([]domain.DispatchAttempt, error) {
	if trigger == "" || since.IsZero() || until.IsZero() || !until.After(since) {
		return nil, fmt.Errorf("dispatch attempt trigger and valid time range are required")
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, provider_account_id, trigger, outcome,
decision, mode, reason, requested_task_id, selected_task_id, run_id, error, started_at, completed_at
FROM dispatch_attempts WHERE trigger = ? AND completed_at >= ? AND completed_at < ?
ORDER BY completed_at, id`, trigger, formatTime(since), formatTime(until))
	if err != nil {
		return nil, fmt.Errorf("list dispatch attempts in range: %w", err)
	}
	defer rows.Close()
	attempts := make([]domain.DispatchAttempt, 0)
	for rows.Next() {
		attempt, err := scanDispatchAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func scanDispatchAttempt(row scanner) (domain.DispatchAttempt, error) {
	var attempt domain.DispatchAttempt
	var requestedTaskID, taskID, runID sql.NullString
	var startedAt, completedAt string
	if err := row.Scan(
		&attempt.ID, &attempt.ProviderAccountID, &attempt.Trigger, &attempt.Outcome,
		&attempt.Decision, &attempt.Mode, &attempt.Reason, &requestedTaskID, &taskID, &runID,
		&attempt.Error, &startedAt, &completedAt,
	); err != nil {
		return domain.DispatchAttempt{}, fmt.Errorf("scan dispatch attempt: %w", err)
	}
	attempt.SelectedTaskID = taskID.String
	attempt.RequestedTaskID = requestedTaskID.String
	attempt.RunID = runID.String
	var err error
	if attempt.StartedAt, err = parseStoredTime(startedAt); err != nil {
		return domain.DispatchAttempt{}, err
	}
	if attempt.CompletedAt, err = parseStoredTime(completedAt); err != nil {
		return domain.DispatchAttempt{}, err
	}
	return attempt, nil
}
