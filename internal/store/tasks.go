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

func (d *DB) CreateProfile(ctx context.Context, p domain.ExecutionProfile, now time.Time) error {
	if p.ID == "" || p.ProviderAccountID == "" || p.HarnessType == "" || p.WorkspaceProvider == "" {
		return fmt.Errorf("profile id, provider account, harness, and workspace provider are required")
	}
	argsJSON, err := json.Marshal(p.HarnessArgs)
	if err != nil {
		return fmt.Errorf("encode harness arguments: %w", err)
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO execution_profiles (
id, provider_account_id, harness_type, model, harness_command, harness_args_json,
workspace_provider, repository, base_branch, require_clean, cleanup_policy,
prepare_command, finalize_command, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.ProviderAccountID, p.HarnessType, p.Model, p.HarnessCommand, string(argsJSON),
		p.WorkspaceProvider, p.Repository, p.BaseBranch, p.RequireClean, p.CleanupPolicy,
		p.PrepareCommand, p.FinalizeCommand, formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("create execution profile: %w", err)
	}
	return nil
}

func (d *DB) ListProfiles(ctx context.Context) ([]domain.ExecutionProfile, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, provider_account_id, harness_type, model,
harness_command, harness_args_json, workspace_provider, repository, base_branch,
require_clean, cleanup_policy, prepare_command, finalize_command, created_at
FROM execution_profiles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list execution profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]domain.ExecutionProfile, 0)
	for rows.Next() {
		var p domain.ExecutionProfile
		var created, argsJSON string
		if err := rows.Scan(&p.ID, &p.ProviderAccountID, &p.HarnessType, &p.Model,
			&p.HarnessCommand, &argsJSON, &p.WorkspaceProvider, &p.Repository, &p.BaseBranch,
			&p.RequireClean, &p.CleanupPolicy, &p.PrepareCommand, &p.FinalizeCommand, &created); err != nil {
			return nil, fmt.Errorf("scan execution profile: %w", err)
		}
		if err := json.Unmarshal([]byte(argsJSON), &p.HarnessArgs); err != nil {
			return nil, fmt.Errorf("decode harness arguments: %w", err)
		}
		p.CreatedAt, err = parseStoredTime(created)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

func (d *DB) GetProfile(ctx context.Context, id string) (domain.ExecutionProfile, error) {
	profiles, err := d.ListProfiles(ctx)
	if err != nil {
		return domain.ExecutionProfile{}, err
	}
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	return domain.ExecutionProfile{}, ErrNotFound
}

func (d *DB) CreateTask(ctx context.Context, task domain.Task, now time.Time) error {
	if task.ID == "" || task.Name == "" || task.ExecutionProfileID == "" {
		return fmt.Errorf("task id, name, and execution profile are required")
	}
	if task.Type != domain.OneOff && task.Type != domain.Recurring {
		return fmt.Errorf("task type must be one_off or recurring")
	}
	if task.MinInterval < 0 {
		return fmt.Errorf("minimum interval cannot be negative")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task creation: %w", err)
	}
	defer tx.Rollback()
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(queue_sequence), 0) + 1 FROM tasks`).Scan(&sequence); err != nil {
		return fmt.Errorf("allocate queue sequence: %w", err)
	}
	state := task.State
	if state == "" {
		state = domain.Queued
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks (
id, name, prompt, prompt_file, priority, queue_sequence, execution_profile_id,
task_type, min_interval_ns, require_repo_change, enabled, state,
last_started_at, last_completed_at, last_successful_source_revision, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.Name, task.Prompt, task.PromptFile, task.Priority, sequence,
		task.ExecutionProfileID, task.Type, int64(task.MinInterval), task.RequireRepoChange,
		state, nullableTime(task.LastStartedAt), nullableTime(task.LastCompletedAt),
		task.LastSuccessfulSourceRevision, formatTime(now), formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task creation: %w", err)
	}
	return nil
}

func (d *DB) GetTask(ctx context.Context, id string) (domain.Task, error) {
	row := d.db.QueryRowContext(ctx, taskSelect+` WHERE t.id = ?`, id)
	return scanTask(row)
}

func (d *DB) SetTaskControl(ctx context.Context, id, action string, now time.Time) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task control: %w", err)
	}
	defer tx.Rollback()
	var result sql.Result
	switch action {
	case "disable":
		result, err = tx.ExecContext(ctx, `UPDATE tasks SET enabled = 0, state = 'disabled', updated_at = ?
WHERE id = ? AND state != 'running'`, formatTime(now), id)
	case "enable":
		result, err = tx.ExecContext(ctx, `UPDATE tasks SET enabled = 1,
state = CASE WHEN state = 'disabled' THEN 'queued' ELSE state END, updated_at = ?
WHERE id = ?`, formatTime(now), id)
	case "retry":
		var sequence int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(queue_sequence), 0) + 1 FROM tasks`).Scan(&sequence); err == nil {
			result, err = tx.ExecContext(ctx, `UPDATE tasks SET enabled = 1, state = 'queued',
queue_sequence = ?, updated_at = ? WHERE id = ? AND state = 'failed'`, sequence, formatTime(now), id)
		}
	default:
		return fmt.Errorf("unknown task control %q", action)
	}
	if err != nil {
		return fmt.Errorf("control task: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read task control result: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: task %q cannot be %s", ErrNotFound, id, action)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task control: %w", err)
	}
	return nil
}

func (d *DB) SetProviderPaused(ctx context.Context, provider string, paused bool) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO provider_controls(provider_account_id, paused, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(provider_account_id) DO UPDATE SET paused = excluded.paused, updated_at = CURRENT_TIMESTAMP`,
		provider, paused)
	if err != nil {
		return fmt.Errorf("set provider pause state: %w", err)
	}
	return nil
}

func (d *DB) ProviderPaused(ctx context.Context, provider string) (bool, error) {
	var paused bool
	err := d.db.QueryRowContext(ctx, `SELECT paused FROM provider_controls WHERE provider_account_id = ?`, provider).Scan(&paused)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read provider pause state: %w", err)
	}
	return paused, nil
}

func (d *DB) ListTasks(ctx context.Context) ([]domain.Task, error) {
	rows, err := d.db.QueryContext(ctx, taskSelect+` ORDER BY t.priority DESC, t.queue_sequence ASC`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	tasks := make([]domain.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (d *DB) NextEligibleTask(
	ctx context.Context,
	providerAccountID string,
	now time.Time,
	currentRevision string,
) (domain.Task, error) {
	rows, err := d.db.QueryContext(ctx, taskSelect+`
JOIN execution_profiles p ON p.id = t.execution_profile_id
WHERE p.provider_account_id = ? AND t.enabled = 1 AND t.state = 'queued'
ORDER BY t.priority DESC, t.queue_sequence ASC`, providerAccountID)
	if err != nil {
		return domain.Task{}, fmt.Errorf("query eligible tasks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return domain.Task{}, err
		}
		if task.LastCompletedAt != nil && task.MinInterval > 0 &&
			now.Before(task.LastCompletedAt.Add(task.MinInterval)) {
			continue
		}
		if task.RequireRepoChange &&
			(currentRevision == "" || currentRevision == task.LastSuccessfulSourceRevision) {
			continue
		}
		return task, nil
	}
	if err := rows.Err(); err != nil {
		return domain.Task{}, fmt.Errorf("scan eligible tasks: %w", err)
	}
	return domain.Task{}, fmt.Errorf("%w: no eligible task for provider %q", ErrNotFound, providerAccountID)
}

func (d *DB) RecordSchedulerDecision(
	ctx context.Context,
	record domain.SchedulerDecision,
	now time.Time,
) (int64, error) {
	var selected any
	if record.SelectedTaskID != "" {
		selected = record.SelectedTaskID
	}
	result, err := d.db.ExecContext(ctx, `INSERT INTO scheduler_decisions (
provider_account_id, decision_json, selected_task_id, created_at
) VALUES (?, ?, ?, ?)`, record.ProviderAccountID, record.DecisionJSON, selected, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("record scheduler decision: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read scheduler decision id: %w", err)
	}
	return id, nil
}

func (d *DB) ListSchedulerDecisions(
	ctx context.Context,
	providerAccountID string,
	limit int,
) ([]domain.SchedulerDecision, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, provider_account_id, decision_json,
selected_task_id, created_at FROM scheduler_decisions
WHERE provider_account_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, providerAccountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list scheduler decisions: %w", err)
	}
	defer rows.Close()
	decisions := make([]domain.SchedulerDecision, 0)
	for rows.Next() {
		var item domain.SchedulerDecision
		var selected sql.NullString
		var created string
		if err := rows.Scan(&item.ID, &item.ProviderAccountID, &item.DecisionJSON, &selected, &created); err != nil {
			return nil, fmt.Errorf("scan scheduler decision: %w", err)
		}
		item.SelectedTaskID = selected.String
		item.CreatedAt, err = parseStoredTime(created)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, item)
	}
	return decisions, rows.Err()
}

const taskSelect = `SELECT t.id, t.name, t.prompt, t.prompt_file, t.priority,
t.queue_sequence, t.execution_profile_id, t.task_type, t.min_interval_ns,
t.require_repo_change, t.enabled, t.state, t.last_started_at, t.last_completed_at,
t.last_successful_source_revision, t.created_at, t.updated_at FROM tasks t`

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (domain.Task, error) {
	var task domain.Task
	var minInterval int64
	var requireChange, enabled bool
	var lastStarted, lastCompleted sql.NullString
	var created, updated string
	err := row.Scan(
		&task.ID, &task.Name, &task.Prompt, &task.PromptFile, &task.Priority,
		&task.QueueSequence, &task.ExecutionProfileID, &task.Type, &minInterval,
		&requireChange, &enabled, &task.State, &lastStarted, &lastCompleted,
		&task.LastSuccessfulSourceRevision, &created, &updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, ErrNotFound
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("scan task: %w", err)
	}
	task.MinInterval = time.Duration(minInterval)
	task.RequireRepoChange = requireChange
	task.Enabled = enabled
	var parseErr error
	if task.CreatedAt, parseErr = parseStoredTime(created); parseErr != nil {
		return domain.Task{}, parseErr
	}
	if task.UpdatedAt, parseErr = parseStoredTime(updated); parseErr != nil {
		return domain.Task{}, parseErr
	}
	if lastStarted.Valid {
		value, err := parseStoredTime(lastStarted.String)
		if err != nil {
			return domain.Task{}, err
		}
		task.LastStartedAt = &value
	}
	if lastCompleted.Valid {
		value, err := parseStoredTime(lastCompleted.String)
		if err != nil {
			return domain.Task{}, err
		}
		task.LastCompletedAt = &value
	}
	return task, nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseStoredTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored timestamp: %w", err)
	}
	return parsed, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}
