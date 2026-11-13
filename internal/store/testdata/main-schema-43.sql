-- Schema of a database created by main at 51d0fec (schema_migrations max 43),
-- before the mobile/multi-account migrations 28-31 were merged. Generated with
-- sqlite3 .dump of store.Open on that revision plus one snapshot.
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO schema_migrations VALUES(2,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(3,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(4,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(5,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(6,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(7,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(8,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(9,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(10,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(11,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(12,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(13,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(14,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(15,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(16,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(17,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(18,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(19,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(20,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(21,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(22,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(23,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(24,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(26,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(27,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(32,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(33,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(34,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(35,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(36,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(37,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(38,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(39,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(40,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(41,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(42,'2026-11-11 18:48:23');
INSERT INTO schema_migrations VALUES(43,'2026-11-11 18:48:23');
CREATE TABLE IF NOT EXISTS "usage_snapshots" (
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
, banked_resets INTEGER, banked_resets_expire_at TEXT, banked_reset_details TEXT);
INSERT INTO usage_snapshots VALUES(1,'claude','2026-11-10T12:00:00.000000000Z',NULL,NULL,0.5,'2026-11-12T12:00:00.000000000Z','native','high',NULL,'2026-11-11 18:48:23',NULL,NULL,NULL);
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
, harness_command TEXT NOT NULL DEFAULT '', harness_args_json TEXT NOT NULL DEFAULT '[]', require_clean INTEGER NOT NULL DEFAULT 0, cleanup_policy TEXT NOT NULL DEFAULT '', workspace_args_json TEXT NOT NULL DEFAULT '[]', budget_model_group TEXT NOT NULL DEFAULT '', agent_context_id TEXT NOT NULL DEFAULT '');
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
, runtime_job_id TEXT NOT NULL DEFAULT '', firesale_min_interval_ns INTEGER NOT NULL DEFAULT 0, dispatch_tier TEXT NOT NULL DEFAULT 'behind' CHECK(dispatch_tier IN ('behind', 'well_behind', 'expiring', 'firesale')));
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
, workspace_directory TEXT NOT NULL DEFAULT '', workspace_metadata TEXT NOT NULL DEFAULT '{}', source_revision TEXT NOT NULL DEFAULT '', error_file TEXT NOT NULL DEFAULT '', finalize_state TEXT NOT NULL DEFAULT '', finalize_error TEXT NOT NULL DEFAULT '', runtime_connection_id TEXT NOT NULL DEFAULT '', external_run_id TEXT NOT NULL DEFAULT '', external_session_id TEXT NOT NULL DEFAULT '', agent_context_id TEXT NOT NULL DEFAULT '', activity_summary TEXT NOT NULL DEFAULT '', activity_outcome TEXT NOT NULL DEFAULT '', activity_artifacts_json TEXT NOT NULL DEFAULT '[]', activity_warnings_json TEXT NOT NULL DEFAULT '[]', actual_provider TEXT NOT NULL DEFAULT '', actual_model TEXT NOT NULL DEFAULT '', activity_read_at TEXT, completion_sequence INTEGER);
CREATE TABLE scheduler_decisions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_account_id TEXT NOT NULL,
    decision_json BLOB NOT NULL,
    selected_task_id TEXT REFERENCES tasks(id),
    created_at TEXT NOT NULL
);
CREATE TABLE provider_controls (
    provider_account_id TEXT PRIMARY KEY,
    paused INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
, policy_name TEXT NOT NULL DEFAULT '', max_concurrent_runs INTEGER NOT NULL DEFAULT 0);
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
, requested_task_id TEXT REFERENCES tasks(id));
CREATE TABLE notification_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending', 'delivered', 'failed')),
    payload_json BLOB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE run_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES runs(id),
    event_type TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    payload_json BLOB NOT NULL DEFAULT '{}'
);
CREATE TABLE token_observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    source TEXT NOT NULL,
    source_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(output_tokens >= 0),
    cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK(cache_read_tokens >= 0),
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0 CHECK(cache_creation_tokens >= 0),
    confidence TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, source, source_id)
);
CREATE TABLE usage_allowance_windows (
    snapshot_id INTEGER NOT NULL REFERENCES usage_snapshots(id) ON DELETE CASCADE,
    pool_key TEXT NOT NULL,
    source_label TEXT NOT NULL,
    scope TEXT NOT NULL CHECK(scope IN ('account', 'model')),
    role TEXT NOT NULL CHECK(role IN ('short', 'weekly')),
    remaining REAL NOT NULL CHECK(remaining >= 0 AND remaining <= 1),
    resets_at TEXT NOT NULL,
    period_duration_s INTEGER NOT NULL CHECK(period_duration_s > 0), reset_inferred INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(snapshot_id, pool_key)
);
INSERT INTO usage_allowance_windows VALUES(1,'weekly','Weekly','account','weekly',0.5,'2026-11-12T12:00:00.000000000Z',604800,0);
CREATE TABLE run_allowance_pool_claims (
    run_id TEXT NOT NULL REFERENCES runs(id),
    provider_account_id TEXT NOT NULL,
    pool_key TEXT NOT NULL,
    PRIMARY KEY(run_id, pool_key)
);
CREATE TABLE runtime_connections (
    id TEXT PRIMARY KEY,
    runtime TEXT NOT NULL,
    transport TEXT NOT NULL,
    url TEXT NOT NULL DEFAULT '',
    credential_source TEXT NOT NULL DEFAULT '',
    credential_ref TEXT NOT NULL DEFAULT '',
    desktop_config_path TEXT NOT NULL DEFAULT '',
    max_concurrent_runs INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);
CREATE TABLE agent_contexts (
    id TEXT PRIMARY KEY,
    runtime_connection_id TEXT NOT NULL REFERENCES runtime_connections(id),
    profile TEXT NOT NULL DEFAULT '',
    agent TEXT NOT NULL DEFAULT '',
    project TEXT NOT NULL DEFAULT '',
    working_directory TEXT NOT NULL DEFAULT '',
    session_mode TEXT NOT NULL DEFAULT 'isolated',
    max_concurrent_runs INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);
CREATE TABLE banked_reset_notices (
    provider_account_id TEXT NOT NULL,
    reset_id TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    kind TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(provider_account_id, expires_at, kind)
);
CREATE TABLE banked_reset_redemptions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_account_id TEXT NOT NULL,
    reset_id TEXT NOT NULL,
    request_id TEXT NOT NULL UNIQUE,
    trigger TEXT NOT NULL CHECK(trigger IN ('manual', 'automatic', 'dry_run')),
    state TEXT NOT NULL CHECK(state IN ('pending', 'done')),
    outcome TEXT NOT NULL DEFAULT '',
    code TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    completed_at TEXT
, resets_left_before INTEGER);
CREATE TABLE automatic_banked_reset_redemptions (
    provider_account_id TEXT NOT NULL,
    reset_id TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    last_attempt_at TEXT NOT NULL,
    retry_at TEXT NOT NULL,
    outcome TEXT NOT NULL,
    PRIMARY KEY(provider_account_id, reset_id)
);
CREATE TABLE reset_announcements (
    announcement_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    notified_at TEXT,
    retracted_at TEXT,
    payload_json BLOB NOT NULL,
    PRIMARY KEY (announcement_key, kind)
);
CREATE TABLE reset_signal_firesales (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    announcement_key TEXT NOT NULL,
    provider_account_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    deadline TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    end_reason TEXT NOT NULL DEFAULT '',
    announcement_json BLOB NOT NULL
, baseline_weekly_remaining REAL, baseline_weekly_resets_at TEXT, baseline_observed_at TEXT);
CREATE TABLE device_sessions (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('browser', 'mobile')),
    created_at TEXT NOT NULL,
    last_used_at TEXT NOT NULL,
    revoked_at TEXT,
    issuer TEXT NOT NULL DEFAULT ''
);
CREATE TABLE push_registrations (
    device_session_id TEXT PRIMARY KEY REFERENCES device_sessions(id),
    platform TEXT NOT NULL CHECK(platform IN ('android','ios')),
    provider TEXT NOT NULL,
    token TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE manual_firesales (
    id INTEGER PRIMARY KEY,
    provider_account_id TEXT NOT NULL,
    started_at TEXT NOT NULL,
    deadline TEXT NOT NULL,
    started_by TEXT NOT NULL,
    ended_at TEXT,
    end_reason TEXT,
    baseline_weekly_resets_at TEXT
);
INSERT INTO sqlite_sequence VALUES('usage_snapshots',1);
CREATE INDEX idx_usage_snapshots_provider_observed
ON usage_snapshots(provider, observed_at DESC, id DESC);
CREATE INDEX idx_execution_profiles_provider ON execution_profiles(provider_account_id);
CREATE INDEX idx_tasks_queue ON tasks(state, enabled, priority DESC, queue_sequence ASC);
CREATE INDEX idx_scheduler_decisions_provider_created
ON scheduler_decisions(provider_account_id, created_at DESC, id DESC);
CREATE INDEX idx_dispatch_attempts_provider_completed
ON dispatch_attempts(provider_account_id, completed_at DESC, id DESC);
CREATE INDEX idx_notification_deliveries_updated
ON notification_deliveries(updated_at DESC, id DESC);
CREATE INDEX idx_run_events_run_id ON run_events(run_id, id);
CREATE INDEX idx_token_observations_provider_observed
ON token_observations(provider, observed_at, id);
CREATE INDEX idx_allowance_windows_pool_reset
ON usage_allowance_windows(pool_key, resets_at);
CREATE UNIQUE INDEX idx_usage_snapshots_identity
ON usage_snapshots(provider, observed_at, source);
CREATE INDEX idx_run_pool_claims_provider_pool
ON run_allowance_pool_claims(provider_account_id, pool_key);
CREATE INDEX idx_runs_activity_unread
ON runs(completed_at DESC) WHERE activity_read_at IS NULL AND state IN ('completed', 'failed');
CREATE UNIQUE INDEX idx_runs_completion_sequence
ON runs(completion_sequence) WHERE completion_sequence IS NOT NULL;
CREATE UNIQUE INDEX idx_banked_reset_notices_deadline
ON banked_reset_notices(provider_account_id, expires_at, kind);
CREATE INDEX idx_banked_reset_redemptions_provider
ON banked_reset_redemptions(provider_account_id, id DESC);
CREATE UNIQUE INDEX reset_signal_firesales_active ON reset_signal_firesales(provider_account_id) WHERE ended_at IS NULL;
CREATE UNIQUE INDEX manual_firesales_active ON manual_firesales(provider_account_id) WHERE ended_at IS NULL;
COMMIT;
