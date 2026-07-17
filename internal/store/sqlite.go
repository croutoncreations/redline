package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jfox/redline/internal/decision"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("snapshot not found")

type DB struct{ db *sql.DB }

func Open(path string) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is required")
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;`); err != nil {
		database.Close()
		return nil, fmt.Errorf("configure SQLite database: %w", err)
	}
	store := &DB{db: database}
	if err := store.migrate(context.Background()); err != nil {
		database.Close()
		return nil, err
	}
	return store, nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version < 2 {
		if err := migrateSnapshotsV2(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (2)`); err != nil {
			return fmt.Errorf("record snapshot migration: %w", err)
		}
		version = 2
	}
	if version < 3 {
		if err := migrateOperationsV3(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (3)`); err != nil {
			return fmt.Errorf("record operations migration: %w", err)
		}
		version = 3
	}
	if version < 4 {
		if _, err := tx.ExecContext(ctx, `
CREATE TABLE provider_controls (
    provider_account_id TEXT PRIMARY KEY,
    paused INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
			return fmt.Errorf("create provider controls: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (4)`); err != nil {
			return fmt.Errorf("record provider controls migration: %w", err)
		}
		version = 4
	}
	if version < 5 {
		if _, err := tx.ExecContext(ctx, `
ALTER TABLE execution_profiles ADD COLUMN harness_command TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_profiles ADD COLUMN harness_args_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE execution_profiles ADD COLUMN require_clean INTEGER NOT NULL DEFAULT 0;
ALTER TABLE execution_profiles ADD COLUMN cleanup_policy TEXT NOT NULL DEFAULT '';
`); err != nil {
			return fmt.Errorf("extend execution profile schema: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (5)`); err != nil {
			return fmt.Errorf("record execution profile migration: %w", err)
		}
		version = 5
	}
	if version < 6 {
		if _, err := tx.ExecContext(ctx, `
ALTER TABLE runs ADD COLUMN workspace_directory TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN workspace_metadata TEXT NOT NULL DEFAULT '{}';
ALTER TABLE runs ADD COLUMN source_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN error_file TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN finalize_state TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN finalize_error TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_runs_one_active_provider
ON runs(provider_account_id) WHERE state IN ('preparing', 'running');
`); err != nil {
			return fmt.Errorf("extend run schema: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (6)`); err != nil {
			return fmt.Errorf("record run migration: %w", err)
		}
		version = 6
	}
	if version < 7 {
		if _, err := tx.ExecContext(ctx, `
CREATE TABLE dispatch_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_account_id TEXT NOT NULL,
    trigger TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK(outcome IN ('admitted', 'wait', 'no_task', 'error')),
    decision TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    selected_task_id TEXT REFERENCES tasks(id),
    run_id TEXT REFERENCES runs(id),
    error TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL
);
CREATE INDEX idx_dispatch_attempts_provider_completed
ON dispatch_attempts(provider_account_id, completed_at DESC, id DESC);
`); err != nil {
			return fmt.Errorf("create dispatch attempt schema: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (7)`); err != nil {
			return fmt.Errorf("record dispatch attempt migration: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func migrateOperationsV3(ctx context.Context, tx *sql.Tx) error {
	const schema = `
CREATE TABLE execution_profiles (
    id TEXT PRIMARY KEY,
    provider_account_id TEXT NOT NULL,
    harness_type TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    workspace_provider TEXT NOT NULL,
    repository TEXT NOT NULL DEFAULT '',
    base_branch TEXT NOT NULL DEFAULT '',
    prepare_command TEXT NOT NULL DEFAULT '',
    finalize_command TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX idx_execution_profiles_provider ON execution_profiles(provider_account_id);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    prompt TEXT NOT NULL DEFAULT '',
    prompt_file TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL,
    queue_sequence INTEGER NOT NULL,
    execution_profile_id TEXT NOT NULL REFERENCES execution_profiles(id),
    task_type TEXT NOT NULL CHECK(task_type IN ('one_off', 'recurring')),
    min_interval_ns INTEGER NOT NULL DEFAULT 0,
    require_repo_change INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    state TEXT NOT NULL CHECK(state IN ('queued', 'running', 'completed', 'failed', 'disabled')),
    last_started_at TEXT,
    last_completed_at TEXT,
    last_successful_source_revision TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_tasks_queue ON tasks(state, enabled, priority DESC, queue_sequence ASC);

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    provider_account_id TEXT NOT NULL,
    state TEXT NOT NULL,
    started_at TEXT NOT NULL,
    completed_at TEXT,
    exit_code INTEGER,
    output_file TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE scheduler_decisions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_account_id TEXT NOT NULL,
    decision_json BLOB NOT NULL,
    selected_task_id TEXT REFERENCES tasks(id),
    created_at TEXT NOT NULL
);
CREATE INDEX idx_scheduler_decisions_provider_created
ON scheduler_decisions(provider_account_id, created_at DESC, id DESC);`
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create operational schema: %w", err)
	}
	return nil
}

func migrateSnapshotsV2(ctx context.Context, tx *sql.Tx) error {
	exists, err := tableExists(ctx, tx, "usage_snapshots")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE usage_snapshots_v2 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    short_remaining REAL,
    short_resets_at TEXT,
    weekly_remaining REAL NOT NULL,
    weekly_resets_at TEXT NOT NULL,
    source TEXT NOT NULL,
    confidence TEXT NOT NULL DEFAULT '',
    raw_payload BLOB,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		return fmt.Errorf("create snapshot schema: %w", err)
	}
	if exists {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO usage_snapshots_v2 (
    id, provider, observed_at, short_remaining, short_resets_at,
    weekly_remaining, weekly_resets_at, source, confidence, raw_payload, created_at
)
SELECT id, provider, observed_at, short_remaining, short_resets_at,
       weekly_remaining, weekly_resets_at, source, confidence, raw_payload, created_at
FROM usage_snapshots`); err != nil {
			return fmt.Errorf("copy existing snapshots: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE usage_snapshots`); err != nil {
			return fmt.Errorf("replace snapshot schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE usage_snapshots_v2 RENAME TO usage_snapshots`); err != nil {
		return fmt.Errorf("activate snapshot schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
CREATE INDEX idx_usage_snapshots_provider_observed
ON usage_snapshots(provider, observed_at DESC, id DESC)`); err != nil {
		return fmt.Errorf("index snapshot schema: %w", err)
	}
	return nil
}

func tableExists(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect schema: %w", err)
	}
	return count > 0, nil
}

func (d *DB) SaveSnapshot(ctx context.Context, s decision.UsageSnapshot, raw []byte) error {
	if err := s.Validate(); err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	var shortRemaining any
	var shortReset any
	if s.Short != nil {
		shortRemaining = s.Short.Remaining
		shortReset = s.Short.ResetsAt.Format(time.RFC3339Nano)
	}
	const query = `INSERT INTO usage_snapshots (
provider, observed_at, short_remaining, short_resets_at,
weekly_remaining, weekly_resets_at, source, confidence, raw_payload
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := d.db.ExecContext(
		ctx,
		query,
		s.Provider,
		s.ObservedAt.Format(time.RFC3339Nano),
		shortRemaining,
		shortReset,
		s.Weekly.Remaining,
		s.Weekly.ResetsAt.Format(time.RFC3339Nano),
		s.Source,
		s.Confidence,
		raw,
	)
	if err != nil {
		return fmt.Errorf("save usage snapshot: %w", err)
	}
	return nil
}

func (d *DB) LatestSnapshot(
	ctx context.Context,
	provider string,
) (decision.UsageSnapshot, []byte, error) {
	const query = `SELECT provider, observed_at, short_remaining, short_resets_at,
weekly_remaining, weekly_resets_at, source, confidence, raw_payload
FROM usage_snapshots WHERE provider = ? ORDER BY observed_at DESC, id DESC LIMIT 1`
	var s decision.UsageSnapshot
	var observedAt, weeklyReset string
	var shortRemaining sql.NullFloat64
	var shortReset sql.NullString
	var raw []byte
	err := d.db.QueryRowContext(ctx, query, provider).Scan(
		&s.Provider,
		&observedAt,
		&shortRemaining,
		&shortReset,
		&s.Weekly.Remaining,
		&weeklyReset,
		&s.Source,
		&s.Confidence,
		&raw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("%w for provider %q", ErrNotFound, provider)
	}
	if err != nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("query latest snapshot: %w", err)
	}
	if s.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt); err != nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("parse stored observation time: %w", err)
	}
	if s.Weekly.ResetsAt, err = time.Parse(time.RFC3339Nano, weeklyReset); err != nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("parse stored weekly reset: %w", err)
	}
	if shortRemaining.Valid != shortReset.Valid {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("stored short window is incomplete")
	}
	if shortRemaining.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, shortReset.String)
		if err != nil {
			return decision.UsageSnapshot{}, nil, fmt.Errorf("parse stored short reset: %w", err)
		}
		s.Short = &decision.UsageWindow{Remaining: shortRemaining.Float64, ResetsAt: parsed}
	}
	return s, raw, nil
}
