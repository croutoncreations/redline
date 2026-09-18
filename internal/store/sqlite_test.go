package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
	_ "modernc.org/sqlite"
)

func TestSQLiteSavesAndReturnsLatestSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	first := usageSnapshot(time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC), 0.47)
	second := usageSnapshot(first.ObservedAt.Add(time.Minute), 0.46)
	if err := db.SaveSnapshot(ctx, first, []byte(`{"sequence":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSnapshot(ctx, second, []byte(`{"sequence":2}`)); err != nil {
		t.Fatal(err)
	}

	got, raw, err := db.LatestSnapshot(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ObservedAt.Equal(second.ObservedAt) || got.Weekly.Remaining != 0.46 {
		t.Fatalf("latest = %#v, want second snapshot", got)
	}
	if string(raw) != `{"sequence":2}` {
		t.Fatalf("raw = %s", raw)
	}
}

// LatestSnapshot drives scheduler dispatch decisions via ORDER BY observed_at
// DESC on a TEXT column. Under RFC3339Nano a whole-second snapshot encodes to
// "…:00Z" and a later one in the same second to "…:00.5Z"; '.' < 'Z' so the
// stale whole-second row would win and the scheduler would act on old usage.
func TestSQLiteLatestSnapshotOrdersChronologicallyNotLexicographically(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	earlier := usageSnapshot(time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC), 0.50)
	later := usageSnapshot(earlier.ObservedAt.Add(500*time.Millisecond), 0.40)
	if err := db.SaveSnapshot(t.Context(), earlier, []byte(`{"sequence":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSnapshot(t.Context(), later, []byte(`{"sequence":2}`)); err != nil {
		t.Fatal(err)
	}

	got, raw, err := db.LatestSnapshot(t.Context(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ObservedAt.Equal(later.ObservedAt) || got.Weekly.Remaining != 0.40 {
		t.Fatalf("latest = %#v, want the chronologically later snapshot", got)
	}
	if string(raw) != `{"sequence":2}` {
		t.Fatalf("raw = %s, want the later snapshot's payload", raw)
	}
}

// Variable-width fractions are the second failure mode: 120ms encodes to
// ".12Z" and 123ms to ".123Z", and ".12Z" > ".123Z" byte-wise.
func TestSQLiteLatestSnapshotOrdersAcrossVariableWidthFractions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	base := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	older := usageSnapshot(base.Add(120*time.Millisecond), 0.47)
	newer := usageSnapshot(base.Add(123*time.Millisecond), 0.46)
	if err := db.SaveSnapshot(t.Context(), older, []byte(`{"sequence":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSnapshot(t.Context(), newer, []byte(`{"sequence":2}`)); err != nil {
		t.Fatal(err)
	}

	got, _, err := db.LatestSnapshot(t.Context(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ObservedAt.Equal(newer.ObservedAt) || got.Weekly.Remaining != 0.46 {
		t.Fatalf("latest = %#v, want the 123ms snapshot", got)
	}
}

// Migration 24 rewrites timestamps written by earlier versions into the
// fixed-width layout. Without it the formatTime change only helps new rows:
// a legacy "…T18:00:00Z" still sorts above a new "…T18:00:00.000000000Z",
// so every deployed database would stay mis-ordered after upgrading.
func TestMigrationNormalizesLegacyVariableWidthTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Rewind past migration 24 and plant legacy-format rows, mimicking a
	// database written by a version that used time.RFC3339Nano directly.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
DELETE FROM schema_migrations WHERE version >= 24;
INSERT INTO usage_snapshots (
    provider, observed_at, short_remaining, short_resets_at,
    weekly_remaining, weekly_resets_at, source, confidence, raw_payload
) VALUES
    ('codex', '2026-07-16T18:00:00Z',   0.3, '2026-07-16T22:00:00Z',
     0.50, '2026-07-17T05:00:00Z', 'openusage', 'high', '{"sequence":1}'),
    ('codex', '2026-07-16T18:00:00.5Z', 0.3, '2026-07-16T22:00:00.5Z',
     0.40, '2026-07-17T05:00:00.5Z', 'openusage', 'high', '{"sequence":2}');`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening runs migration 24.
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	got, _, err := db.LatestSnapshot(t.Context(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 16, 18, 0, 0, 500_000_000, time.UTC)
	if !got.ObservedAt.Equal(want) || got.Weekly.Remaining != 0.40 {
		t.Fatalf("latest = %#v, want the legacy .5Z snapshot (weekly 0.40) after migration", got)
	}

	// Values must be rewritten in place, not merely parsed correctly on read.
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rows, err := raw.Query(`SELECT observed_at FROM usage_snapshots ORDER BY observed_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stored []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		stored = append(stored, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	expected := []string{"2026-07-16T18:00:00.000000000Z", "2026-07-16T18:00:00.500000000Z"}
	if !slices.Equal(stored, expected) {
		t.Fatalf("stored observed_at = %q, want %q", stored, expected)
	}
}

// Before this change SaveSnapshot called Format(time.RFC3339Nano) directly,
// without formatTime's .UTC() normalization, so a provider reporting a
// numeric offset (openusage.parseTime preserves the parsed zone) was stored
// as e.g. "2026-07-16T20:00:00+05:00". Digits sort below 'Z', so such a row
// outranks every "…Z" value under ORDER BY observed_at DESC and keeps winning
// LatestSnapshot even after newer rows arrive. The migration must normalize
// offset forms to UTC, not just pad Z-form fractions.
func TestMigrationNormalizesLegacyOffsetFormTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// 20:00 at +05:00 is 15:00Z — earlier than the 16:00Z row added below.
	plusFive := time.FixedZone("plus-five", 5*60*60)
	legacy := time.Date(2026, 7, 16, 20, 0, 0, 0, plusFive).Format(time.RFC3339Nano)
	if legacy != "2026-07-16T20:00:00+05:00" {
		t.Fatalf("fixture encoding = %q, want an offset form", legacy)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
DELETE FROM schema_migrations WHERE version >= 24;
INSERT INTO usage_snapshots (
    provider, observed_at, short_remaining, short_resets_at,
    weekly_remaining, weekly_resets_at, source, confidence, raw_payload
) VALUES ('codex', ?, 0.3, ?, 0.99, ?, 'openusage', 'high', '{"sequence":"legacy"}');`,
		legacy, legacy, legacy); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// A genuinely newer snapshot, written after the upgrade.
	newer := usageSnapshot(time.Date(2026, 7, 16, 16, 0, 0, 0, time.UTC), 0.11)
	if err := db.SaveSnapshot(t.Context(), newer, []byte(`{"sequence":"new"}`)); err != nil {
		t.Fatal(err)
	}

	got, _, err := db.LatestSnapshot(t.Context(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ObservedAt.Equal(newer.ObservedAt) || got.Weekly.Remaining != 0.11 {
		t.Fatalf("latest = %#v, want the newer 16:00Z snapshot; a legacy offset row is still winning",
			got)
	}

	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var stored string
	if err := raw.QueryRow(
		`SELECT observed_at FROM usage_snapshots WHERE raw_payload = '{"sequence":"legacy"}'`,
	).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "2026-07-16T15:00:00.000000000Z" {
		t.Fatalf("legacy observed_at = %q, want it normalized to UTC fixed width", stored)
	}
}

// CURRENT_TIMESTAMP columns hold "YYYY-MM-DD HH:MM:SS", not RFC3339. The
// migration must leave values it cannot parse alone rather than corrupt them.
func TestMigrationLeavesNonRFC3339TimestampsUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
DELETE FROM schema_migrations WHERE version >= 24;
INSERT INTO provider_controls (provider_account_id, paused, updated_at)
VALUES ('codex-main', 0, '2026-09-18 13:45:01');`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var updatedAt string
	if err := raw.QueryRow(
		`SELECT updated_at FROM provider_controls WHERE provider_account_id = 'codex-main'`,
	).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != "2026-09-18 13:45:01" {
		t.Fatalf("provider_controls.updated_at = %q, want it left untouched", updatedAt)
	}
}

func TestSQLiteDeduplicatesSnapshotIdentity(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot := usageSnapshot(time.Date(2026, 7, 21, 17, 4, 35, 201000000, time.UTC), .52)
	snapshot.Provider = "claude"
	for range 2 {
		if err := db.SaveSnapshot(t.Context(), snapshot, []byte(`{"same":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListSnapshots(t.Context(), "claude", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("snapshots=%#v err=%v", got, err)
	}
}

func TestSQLiteReturnsLatestSnapshotForSelectedSource(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	older := usageSnapshot(time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC), .52)
	older.Source = "openusage"
	newer := usageSnapshot(older.ObservedAt.Add(time.Minute), .51)
	newer.Source = "native"
	if err := db.SaveSnapshot(t.Context(), older, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSnapshot(t.Context(), newer, nil); err != nil {
		t.Fatal(err)
	}
	got, _, err := db.LatestSnapshotFromSource(t.Context(), older.Provider, "openusage")
	if err != nil || got.Source != "openusage" || got.Weekly.Remaining != .52 {
		t.Fatalf("snapshot=%#v err=%v", got, err)
	}
}

func TestSnapshotIdentityMigrationRemovesExistingDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := usageSnapshot(time.Date(2026, 7, 21, 17, 4, 35, 0, time.UTC), .52)
	if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP INDEX idx_usage_snapshots_identity;
DROP INDEX idx_runs_activity_unread;
DELETE FROM schema_migrations WHERE version >= 15;
DROP TABLE run_allowance_pool_claims;
DROP TABLE agent_contexts;
DROP TABLE runtime_connections;
CREATE UNIQUE INDEX idx_runs_one_active_provider
ON runs(provider_account_id) WHERE state IN ('preparing', 'running');
ALTER TABLE provider_controls DROP COLUMN policy_name;
ALTER TABLE provider_controls DROP COLUMN max_concurrent_runs;
ALTER TABLE execution_profiles DROP COLUMN agent_context_id;
ALTER TABLE tasks DROP COLUMN runtime_job_id;
ALTER TABLE runs DROP COLUMN runtime_connection_id;
ALTER TABLE runs DROP COLUMN agent_context_id;
ALTER TABLE runs DROP COLUMN external_run_id;
ALTER TABLE runs DROP COLUMN external_session_id;
ALTER TABLE runs DROP COLUMN activity_summary;
ALTER TABLE runs DROP COLUMN activity_outcome;
ALTER TABLE runs DROP COLUMN activity_artifacts_json;
ALTER TABLE runs DROP COLUMN activity_warnings_json;
ALTER TABLE runs DROP COLUMN actual_provider;
ALTER TABLE runs DROP COLUMN actual_model;
ALTER TABLE runs DROP COLUMN activity_read_at;
ALTER TABLE usage_allowance_windows DROP COLUMN reset_inferred;
ALTER TABLE dispatch_attempts DROP COLUMN requested_task_id;
INSERT INTO dispatch_attempts (
    provider_account_id, trigger, outcome, decision, mode, reason,
    selected_task_id, run_id, error, started_at, completed_at
) VALUES (
    'codex-main', 'automatic', 'wait', 'WAIT', 'pace_threshold', 'legacy attempt',
    NULL, NULL, '', '2026-07-21T17:00:00Z', '2026-07-21T17:00:01Z'
);
INSERT INTO usage_snapshots (
    provider, observed_at, short_remaining, short_resets_at,
    weekly_remaining, weekly_resets_at, source, confidence, raw_payload
)
SELECT provider, observed_at, short_remaining, short_resets_at,
       weekly_remaining, weekly_resets_at, source, confidence, raw_payload
FROM usage_snapshots LIMIT 1;`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.ListSnapshots(t.Context(), "codex", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("snapshots=%#v err=%v", got, err)
	}
	attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
	if err != nil || len(attempts) != 1 || attempts[0].RequestedTaskID != "" || attempts[0].Reason != "legacy attempt" {
		t.Fatalf("legacy attempts=%#v err=%v", attempts, err)
	}
}

func TestSQLiteRoundTripsSupplementalAllowancePools(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	snapshot := usageSnapshot(time.Date(2026, 7, 19, 3, 27, 53, 0, time.UTC), .73)
	snapshot.Provider = "claude"
	snapshot.Allowances = []decision.AllowanceWindow{{
		Key: "model:fable:weekly", SourceLabel: "Fable", Scope: "model", Role: "weekly",
		Remaining: .48, ResetsAt: snapshot.Weekly.ResetsAt, PeriodDurationSeconds: 7 * 24 * 60 * 60,
		ResetInferred: true,
	}}
	if err := db.SaveSnapshot(t.Context(), snapshot, []byte(`{"providerId":"claude"}`)); err != nil {
		t.Fatal(err)
	}

	got, _, err := db.LatestSnapshot(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	fable, ok := got.Allowance("model:fable:weekly")
	if !ok || fable.Remaining != .48 || fable.SourceLabel != "Fable" || !fable.ResetInferred {
		t.Fatalf("allowances = %#v", got.Allowances)
	}
	if _, ok := got.Allowance("session"); !ok {
		t.Fatalf("legacy session was not normalized: %#v", got.Allowances)
	}
	if _, ok := got.Allowance("weekly"); !ok {
		t.Fatalf("legacy weekly was not normalized: %#v", got.Allowances)
	}
}

func TestOpenMigratesExistingVersionTwoDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT INTO schema_migrations(version) VALUES (2);`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("migrate v2 database: %v", err)
	}
	_ = db.Close()
}

func TestSQLitePreservesSnapshotWithoutShortWindow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	snapshot := usageSnapshot(time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC), 0.67)
	snapshot.Provider = "codex"
	snapshot.Short = nil
	if err := db.SaveSnapshot(context.Background(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	got, _, err := db.LatestSnapshot(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.Short != nil || got.Weekly.Remaining != 0.67 {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestListSnapshotsReturnsRecentHistoryChronologically(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		snapshot := decision.UsageSnapshot{
			Provider: "claude", ObservedAt: base.Add(time.Duration(index) * time.Minute),
			Short:  &decision.UsageWindow{Remaining: 1 - float64(index)*0.1, ResetsAt: base.Add(5 * time.Hour)},
			Weekly: decision.UsageWindow{Remaining: 1 - float64(index)*0.01, ResetsAt: base.Add(7 * 24 * time.Hour)},
			Source: "test",
		}
		if err := db.SaveSnapshot(context.Background(), snapshot, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListSnapshots(context.Background(), "claude", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].ObservedAt.Equal(base.Add(time.Minute)) || !got[1].ObservedAt.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("snapshots = %#v", got)
	}
}

func TestSQLiteReportsMissingProvider(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, _, err := db.LatestSnapshot(context.Background(), "claude"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func usageSnapshot(observed time.Time, weekly float64) decision.UsageSnapshot {
	return decision.UsageSnapshot{
		Provider:   "codex",
		ObservedAt: observed,
		Short: &decision.UsageWindow{
			Remaining: 0.3,
			ResetsAt:  observed.Add(4 * time.Hour),
		},
		Weekly: decision.UsageWindow{
			Remaining: weekly,
			ResetsAt:  observed.Add(11 * time.Hour),
		},
		Source:     "openusage",
		Confidence: "high",
	}
}
