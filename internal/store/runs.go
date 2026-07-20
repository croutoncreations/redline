package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jfox/redline/internal/domain"
)

func (d *DB) HasActiveRun(ctx context.Context, providerAccountID string) (bool, error) {
	var active bool
	if err := d.db.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM runs WHERE provider_account_id = ? AND state IN ('preparing', 'running')
)`, providerAccountID).Scan(&active); err != nil {
		return false, fmt.Errorf("check active provider run: %w", err)
	}
	return active, nil
}

func (d *DB) AdmitTask(
	ctx context.Context,
	runID, taskID, providerAccountID, sourceRevision string,
	now time.Time,
) (domain.Run, error) {
	if runID == "" || taskID == "" || providerAccountID == "" {
		return domain.Run{}, fmt.Errorf("run, task, and provider ids are required")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, fmt.Errorf("begin run admission: %w", err)
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs
WHERE provider_account_id = ? AND state IN ('preparing', 'running')`, providerAccountID).Scan(&active); err != nil {
		return domain.Run{}, fmt.Errorf("check active provider runs: %w", err)
	}
	if active > 0 {
		return domain.Run{}, fmt.Errorf("%w: provider %q already has an active run", ErrConflict, providerAccountID)
	}
	row := tx.QueryRowContext(ctx, taskSelect+`
JOIN execution_profiles p ON p.id = t.execution_profile_id
WHERE t.id = ? AND p.provider_account_id = ?`, taskID, providerAccountID)
	task, err := scanTask(row)
	if err != nil {
		return domain.Run{}, err
	}
	if !task.Enabled || task.State != domain.Queued {
		return domain.Run{}, fmt.Errorf("task %q is not queued and enabled", taskID)
	}
	if task.LastCompletedAt != nil && task.MinInterval > 0 && now.Before(task.LastCompletedAt.Add(task.MinInterval)) {
		return domain.Run{}, fmt.Errorf("task %q minimum interval has not elapsed", taskID)
	}
	if task.RequireRepoChange &&
		(sourceRevision == "" || sourceRevision == task.LastSuccessfulSourceRevision) {
		return domain.Run{}, fmt.Errorf("task %q requires a changed repository revision", taskID)
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = 'running', last_started_at = ?, updated_at = ?
WHERE id = ? AND state = 'queued' AND enabled = 1`, formatTime(now), formatTime(now), taskID)
	if err != nil {
		return domain.Run{}, fmt.Errorf("claim task: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return domain.Run{}, fmt.Errorf("task %q was claimed concurrently", taskID)
	}
	run := domain.Run{
		ID: runID, TaskID: taskID, ProviderAccountID: providerAccountID,
		State: domain.RunPreparing, SourceRevision: sourceRevision, StartedAt: now.UTC(),
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs (
id, task_id, provider_account_id, state, source_revision, started_at
) VALUES (?, ?, ?, ?, ?, ?)`,
		run.ID, run.TaskID, run.ProviderAccountID, run.State, run.SourceRevision, formatTime(run.StartedAt))
	if err != nil {
		return domain.Run{}, fmt.Errorf("create run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, fmt.Errorf("commit run admission: %w", err)
	}
	return run, nil
}

func (d *DB) MarkRunRunning(ctx context.Context, runID string, workspace domain.Workspace) error {
	metadata, err := json.Marshal(workspace)
	if err != nil {
		return fmt.Errorf("encode workspace: %w", err)
	}
	result, err := d.db.ExecContext(ctx, `UPDATE runs SET state = 'running',
workspace_directory = ?, workspace_metadata = ? WHERE id = ? AND state = 'preparing'`,
		workspace.Directory, string(metadata), runID)
	if err != nil {
		return fmt.Errorf("mark run running: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return fmt.Errorf("%w: preparing run %q", ErrNotFound, runID)
	}
	return nil
}

func (d *DB) CompleteRun(
	ctx context.Context,
	runID string,
	completion domain.RunCompletion,
	now time.Time,
) error {
	if completion.State != domain.RunCompleted && completion.State != domain.RunFailed {
		return fmt.Errorf("completion state must be completed or failed")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin run completion: %w", err)
	}
	defer tx.Rollback()
	var taskID, sourceRevision string
	if err := tx.QueryRowContext(ctx, `SELECT task_id, source_revision FROM runs
WHERE id = ? AND state IN ('preparing', 'running')`, runID).Scan(&taskID, &sourceRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: active run %q", ErrNotFound, runID)
		}
		return fmt.Errorf("read active run: %w", err)
	}
	var taskType domain.TaskType
	if err := tx.QueryRowContext(ctx, `SELECT task_type FROM tasks WHERE id = ?`, taskID).Scan(&taskType); err != nil {
		return fmt.Errorf("read run task: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET state = ?, completed_at = ?, exit_code = ?,
output_file = ?, error_file = ?, error = ?, finalize_state = ?, finalize_error = ? WHERE id = ?`,
		completion.State, formatTime(now), completion.ExitCode, completion.OutputFile,
		completion.ErrorFile, completion.Error, completion.FinalizeState, completion.FinalizeError, runID)
	if err != nil {
		return fmt.Errorf("complete run: %w", err)
	}
	if completion.State == domain.RunCompleted {
		state := domain.Completed
		var sequence any
		if taskType == domain.Recurring {
			state = domain.Queued
			var next int64
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(queue_sequence), 0) + 1 FROM tasks`).Scan(&next); err != nil {
				return fmt.Errorf("allocate recurring queue position: %w", err)
			}
			sequence = next
		} else {
			if err := tx.QueryRowContext(ctx, `SELECT queue_sequence FROM tasks WHERE id = ?`, taskID).Scan(&sequence); err != nil {
				return fmt.Errorf("retain task queue position: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET state = ?, queue_sequence = ?,
last_completed_at = ?, last_successful_source_revision = ?, updated_at = ? WHERE id = ?`,
			state, sequence, formatTime(now), sourceRevision, formatTime(now), taskID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET state = 'failed', updated_at = ? WHERE id = ?`,
			formatTime(now), taskID)
	}
	if err != nil {
		return fmt.Errorf("complete run task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run completion: %w", err)
	}
	return nil
}

func (d *DB) GetRun(ctx context.Context, runID string) (domain.Run, error) {
	return scanRun(d.db.QueryRowContext(ctx, runSelect+` WHERE id = ?`, runID))
}

func (d *DB) ListRuns(ctx context.Context, limit int) ([]domain.Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, runSelect+` ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
	runs := make([]domain.Run, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (d *DB) RecoverInterruptedRuns(ctx context.Context, now time.Time) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin interrupted-run recovery: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, task_id FROM runs WHERE state IN ('preparing', 'running')`)
	if err != nil {
		return fmt.Errorf("find interrupted runs: %w", err)
	}
	type interrupted struct{ runID, taskID string }
	var items []interrupted
	for rows.Next() {
		var item interrupted
		if err := rows.Scan(&item.runID, &item.taskID); err != nil {
			rows.Close()
			return fmt.Errorf("scan interrupted run: %w", err)
		}
		items = append(items, item)
	}
	rows.Close()
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET state = 'failed', completed_at = ?,
exit_code = -1, error = 'service restarted while run was active',
finalize_state = CASE WHEN workspace_directory <> '' THEN 'preserved' ELSE 'skipped' END,
finalize_error = CASE WHEN workspace_directory <> ''
    THEN 'service restarted; workspace preserved for manual recovery' ELSE '' END
WHERE id = ?`, formatTime(now), item.runID); err != nil {
			return fmt.Errorf("recover interrupted run: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state = 'failed', updated_at = ? WHERE id = ?`,
			formatTime(now), item.taskID); err != nil {
			return fmt.Errorf("recover interrupted task: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit interrupted-run recovery: %w", err)
	}
	return nil
}

const runSelect = `SELECT id, task_id, provider_account_id, state,
workspace_directory, workspace_metadata, source_revision, started_at, completed_at,
exit_code, output_file, error_file, error, finalize_state, finalize_error FROM runs`

func scanRun(row scanner) (domain.Run, error) {
	var run domain.Run
	var workspaceJSON, started string
	var completed sql.NullString
	var exitCode sql.NullInt64
	err := row.Scan(
		&run.ID, &run.TaskID, &run.ProviderAccountID, &run.State,
		&run.Workspace.Directory, &workspaceJSON, &run.SourceRevision, &started, &completed,
		&exitCode, &run.OutputFile, &run.ErrorFile, &run.Error, &run.FinalizeState, &run.FinalizeError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, ErrNotFound
	}
	if err != nil {
		return domain.Run{}, fmt.Errorf("scan run: %w", err)
	}
	if workspaceJSON != "" && workspaceJSON != "{}" {
		if err := json.Unmarshal([]byte(workspaceJSON), &run.Workspace); err != nil {
			return domain.Run{}, fmt.Errorf("decode run workspace: %w", err)
		}
	}
	var parseErr error
	if run.StartedAt, parseErr = parseStoredTime(started); parseErr != nil {
		return domain.Run{}, parseErr
	}
	if completed.Valid {
		value, err := parseStoredTime(completed.String)
		if err != nil {
			return domain.Run{}, err
		}
		run.CompletedAt = &value
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		run.ExitCode = &value
	}
	return run, nil
}
