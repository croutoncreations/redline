package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/google/uuid"
)

func migrateIssues(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS issue_sources (id TEXT PRIMARY KEY, definition TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS issue_items (id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES issue_sources(id), host TEXT NOT NULL, external_id INTEGER NOT NULL, snapshot TEXT NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', UNIQUE(host,external_id));
CREATE TABLE IF NOT EXISTS issue_attempts (id TEXT PRIMARY KEY, item_id TEXT NOT NULL REFERENCES issue_items(id), task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id), run_id TEXT REFERENCES runs(id), state TEXT NOT NULL, definition TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS issue_attempt_items ON issue_attempts(item_id);
CREATE UNIQUE INDEX IF NOT EXISTS issue_one_active_attempt ON issue_attempts(item_id) WHERE state IN ('queued','running','publishing','delivery_failed','interrupted');
INSERT INTO schema_migrations(version) SELECT 44 WHERE NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version=44);`)
	return err
}

func (d *DB) CreateIssueSource(ctx context.Context, s domain.IssueSource) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO issue_sources(id,definition) VALUES (?,?)`, s.ID, string(data))
	return err
}
func (d *DB) SaveIssueSource(ctx context.Context, s domain.IssueSource) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	result, err := d.db.ExecContext(ctx, `UPDATE issue_sources SET definition=? WHERE id=?`, string(data), s.ID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
func (d *DB) IssueSource(ctx context.Context, id string) (domain.IssueSource, error) {
	var s domain.IssueSource
	var data string
	err := d.db.QueryRowContext(ctx, `SELECT definition FROM issue_sources WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal([]byte(data), &s)
	return s, err
}
func (d *DB) IssueSources(ctx context.Context) ([]domain.IssueSource, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT definition FROM issue_sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.IssueSource{}
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var s domain.IssueSource
		if err = json.Unmarshal([]byte(data), &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (d *DB) IssueItems(ctx context.Context, source string) ([]domain.IssueItem, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id,source_id,host,snapshot,state,reason FROM issue_items WHERE (?='' OR source_id=?) ORDER BY rowid DESC`, source, source)
	if err != nil {
		return nil, err
	}
	out := []domain.IssueItem{}
	for rows.Next() {
		var i domain.IssueItem
		var data string
		if err = rows.Scan(&i.ID, &i.SourceID, &i.Host, &data, &i.State, &i.Reason); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &i.Snapshot); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for n := range out {
		out[n].Attempts, err = d.issueAttempts(ctx, out[n].ID)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (d *DB) issueAttempts(ctx context.Context, item string) ([]domain.IssueAttempt, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT definition, state, COALESCE(run_id,'') FROM issue_attempts WHERE item_id=? ORDER BY rowid`, item)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.IssueAttempt{}
	for rows.Next() {
		var data, state, run string
		if err = rows.Scan(&data, &state, &run); err != nil {
			return nil, err
		}
		var a domain.IssueAttempt
		if err = json.Unmarshal([]byte(data), &a); err != nil {
			return nil, err
		}
		a.State = state
		a.RunID = run
		out = append(out, a)
	}
	return out, rows.Err()
}
func (d *DB) IssueAttemptForTask(ctx context.Context, task string) (domain.IssueAttempt, error) {
	var a domain.IssueAttempt
	var data, state, run string
	err := d.db.QueryRowContext(ctx, `SELECT definition,state,COALESCE(run_id,'') FROM issue_attempts WHERE task_id=?`, task).Scan(&data, &state, &run)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal([]byte(data), &a)
	a.State = state
	a.RunID = run
	return a, err
}
func (d *DB) SaveIssueAttempt(ctx context.Context, a domain.IssueAttempt) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE issue_attempts SET definition=?,state=? WHERE id=?`, string(data), a.State, a.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `UPDATE issue_items SET state=?,reason=? WHERE id=?`, a.State, a.Error, a.ItemID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// QueueIssue atomically deduplicates discovery and creates a normal one-off task.
// retry names an existing item and is the only path to another coding attempt.
func (d *DB) QueueIssue(ctx context.Context, s domain.IssueSource, i domain.IssueSnapshot, prompt, retry string, now time.Time) (string, error) {
	profile, err := d.GetProfile(ctx, s.ExecutionProfileID)
	if err != nil {
		return "", err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var item, state string
	err = tx.QueryRowContext(ctx, `SELECT id,state FROM issue_items WHERE host=? AND external_id=?`, s.Host, i.ID).Scan(&item, &state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	snapshot, _ := json.Marshal(i)
	if err == nil {
		if _, err = tx.ExecContext(ctx, `UPDATE issue_items SET snapshot=? WHERE id=?`, string(snapshot), item); err != nil {
			return "", err
		}
		if retry == "" {
			return item, tx.Commit()
		}
		if retry != item || state == "queued" || state == "running" || state == "publishing" || state == "delivery_failed" || state == "interrupted" {
			return "", fmt.Errorf("%w: reconcile or retry delivery before another coding attempt", ErrConflict)
		}
	} else {
		if retry != "" {
			return "", ErrNotFound
		}
		item = uuid.NewString()
		if _, err = tx.ExecContext(ctx, `INSERT INTO issue_items(id,source_id,host,external_id,snapshot,state) VALUES (?,?,?,?,?,'queued')`, item, s.ID, s.Host, i.ID, string(snapshot)); err != nil {
			return "", err
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_attempts a JOIN issue_items i ON i.id=a.item_id WHERE i.source_id=? AND a.state='queued'`, s.ID).Scan(&count); err != nil {
		return "", err
	}
	if count >= s.MaxQueued {
		return "", fmt.Errorf("%w: source queue is full", ErrConflict)
	}
	var sequence int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(queue_sequence),0)+1 FROM tasks`).Scan(&sequence); err != nil {
		return "", err
	}
	a := domain.IssueAttempt{ID: uuid.NewString(), ItemID: item, TaskID: "issue-" + uuid.NewString(), Source: s, Profile: profile, Snapshot: i, PromptVersion: 1, State: "queued", CreatedAt: now, UpdatedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,name,prompt,priority,queue_sequence,execution_profile_id,task_type,dispatch_tier,enabled,state,created_at,updated_at) VALUES (?,?,?,?,?,?,'one_off',?,1,'queued',?,?)`, a.TaskID, fmt.Sprintf("%s #%d: %s", s.Repository, i.Number, i.Title), prompt, s.Priority, sequence, s.ExecutionProfileID, s.DispatchTier, formatTime(now), formatTime(now))
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(a)
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_attempts(id,item_id,task_id,state,definition) VALUES (?,?,?,'queued',?)`, a.ID, item, a.TaskID, string(data))
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE issue_items SET state='queued',reason='' WHERE id=?`, item)
	if err != nil {
		return "", err
	}
	return item, tx.Commit()
}

func (d *DB) WithdrawIssue(ctx context.Context, a domain.IssueAttempt, reason string, now time.Time) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE issue_attempts SET state='withdrawn' WHERE id=? AND state='queued'`, a.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET state='disabled',enabled=0,updated_at=? WHERE id=? AND state='queued'`, formatTime(now), a.TaskID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE issue_items SET state='withdrawn',reason=? WHERE id=?`, reason, a.ItemID); err != nil {
		return err
	}
	return tx.Commit()
}

// RefreshIssueRunDelivery updates the delivery description after a retry while
// retaining the original execution state, logs, and completion sequence.
func (d *DB) RefreshIssueRunDelivery(ctx context.Context, a domain.IssueAttempt) error {
	if a.RunID == "" {
		return nil
	}
	run, err := d.GetRun(ctx, a.RunID)
	if err != nil {
		return err
	}
	if run.State == domain.RunPreparing || run.State == domain.RunRunning {
		return nil
	}
	artifacts := run.Artifacts
	if a.PRURL != "" {
		found := false
		for _, artifact := range artifacts {
			if artifact.URL == a.PRURL {
				found = true
			}
		}
		if !found {
			artifacts = append(artifacts, domain.RunArtifact{Type: "pull_request", Label: "Draft PR", URL: a.PRURL})
		}
	}
	data, err := json.Marshal(artifacts)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `UPDATE runs SET activity_outcome=?,activity_summary=?,activity_artifacts_json=?,activity_read_at=NULL WHERE id=? AND state IN ('completed','failed')`, a.State, a.Summary, string(data), a.RunID)
	return err
}

// Sync health is updated independently so an in-flight poll cannot undo a pause
// or replace credentials edited by the user.
func (d *DB) RecordIssueSync(ctx context.Context, s domain.IssueSource) error {
	var last, next any
	if s.LastSyncedAt != nil {
		last = formatTime(*s.LastSyncedAt)
	}
	if s.NextSyncAt != nil {
		next = formatTime(*s.NextSyncAt)
	}
	_, err := d.db.ExecContext(ctx, `UPDATE issue_sources SET definition=json_set(definition,'$.last_synced_at',?,'$.next_sync_at',?,'$.last_error',?) WHERE id=?`, last, next, s.LastError, s.ID)
	return err
}
func (d *DB) SetIssueSourceEnabled(ctx context.Context, id string, enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	_, err := d.db.ExecContext(ctx, `UPDATE issue_sources SET definition=json_set(definition,'$.enabled',json(?),'$.next_sync_at',NULL) WHERE id=?`, value, id)
	return err
}
func (d *DB) UpdateIssueSourceConfig(ctx context.Context, s domain.IssueSource) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `UPDATE issue_sources SET definition=json_set(?,'$.last_synced_at',json_extract(definition,'$.last_synced_at'),'$.last_error',COALESCE(json_extract(definition,'$.last_error'),''),'$.next_sync_at',NULL) WHERE id=?`, string(data), s.ID)
	return err
}
