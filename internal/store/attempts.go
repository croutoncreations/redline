package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jfox/redline/internal/domain"
)

func (d *DB) RecordDispatchAttempt(ctx context.Context, attempt domain.DispatchAttempt) (int64, error) {
	if attempt.ProviderAccountID == "" || attempt.Trigger == "" || attempt.Outcome == "" ||
		attempt.StartedAt.IsZero() || attempt.CompletedAt.IsZero() {
		return 0, fmt.Errorf("dispatch attempt provider, trigger, outcome, and timestamps are required")
	}
	var selectedTaskID, runID any
	if attempt.SelectedTaskID != "" {
		selectedTaskID = attempt.SelectedTaskID
	}
	if attempt.RunID != "" {
		runID = attempt.RunID
	}
	result, err := d.db.ExecContext(ctx, `INSERT INTO dispatch_attempts (
provider_account_id, trigger, outcome, decision, mode, reason,
selected_task_id, run_id, error, started_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ProviderAccountID, attempt.Trigger, attempt.Outcome, attempt.Decision,
		attempt.Mode, attempt.Reason, selectedTaskID, runID, attempt.Error,
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
decision, mode, reason, selected_task_id, run_id, error, started_at, completed_at
FROM dispatch_attempts WHERE provider_account_id = ?
ORDER BY completed_at DESC, id DESC LIMIT ?`, providerAccountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list dispatch attempts: %w", err)
	}
	defer rows.Close()
	attempts := make([]domain.DispatchAttempt, 0)
	for rows.Next() {
		var attempt domain.DispatchAttempt
		var taskID, runID sql.NullString
		var startedAt, completedAt string
		if err := rows.Scan(
			&attempt.ID, &attempt.ProviderAccountID, &attempt.Trigger, &attempt.Outcome,
			&attempt.Decision, &attempt.Mode, &attempt.Reason, &taskID, &runID,
			&attempt.Error, &startedAt, &completedAt,
		); err != nil {
			return nil, fmt.Errorf("scan dispatch attempt: %w", err)
		}
		attempt.SelectedTaskID = taskID.String
		attempt.RunID = runID.String
		if attempt.StartedAt, err = parseStoredTime(startedAt); err != nil {
			return nil, err
		}
		if attempt.CompletedAt, err = parseStoredTime(completedAt); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}
