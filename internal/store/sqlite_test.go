package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
	_ "modernc.org/sqlite"
)

func TestSQLiteSavesAndReturnsLatestSnapshot(t *testing.T) {
	t.Parallel()
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

// The rewrite used to issue one UPDATE ... WHERE column = old per distinct
// value, a full scan of an unindexed column each time. A real database with
// ~30k snapshots and ~37k dispatch attempts hung startup for many minutes.
// At this size the quadratic version takes tens of seconds; the rowid-keyed
// one takes well under the bound.
func TestMigrationTimestampRewriteScalesLinearly(t *testing.T) {
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
	const rowCount = 20000
	if _, err := raw.Exec(`DELETE FROM schema_migrations WHERE version >= 24`); err != nil {
		t.Fatal(err)
	}
	// Distinct legacy values on every row, so a per-value rewrite does
	// rowCount full scans.
	if _, err := raw.Exec(`
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
INSERT INTO usage_snapshots (provider, observed_at, weekly_remaining, weekly_resets_at, source, confidence)
SELECT 'codex', strftime('%Y-%m-%dT%H:%M:%SZ', '2026-01-01', '+' || n || ' seconds'),
       0.5, '2026-07-17T05:00:00Z', 'openusage', 'high' FROM seq`, rowCount); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("migrating %d legacy rows took %s; the rewrite is not linear", rowCount, elapsed)
	}

	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var legacy int
	if err := raw.QueryRow(`SELECT count(*) FROM usage_snapshots WHERE length(observed_at) <> 30`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != 0 {
		t.Fatalf("%d rows still carry legacy timestamps", legacy)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// Banked resets must survive being stored and read back.
//
// The snapshot table has explicit columns rather than a JSON blob, so a new
// field is silently dropped on write unless a column is added for it. The
// symptom is subtle: the API's refresh endpoint returns the value, the
// dashboard does not, and the two disagree for no visible reason.
func TestSQLiteRoundTripsBankedResets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resets := 2
	snapshot := decision.UsageSnapshot{
		Provider:     "codex",
		ObservedAt:   time.Now().UTC().Truncate(time.Second),
		Weekly:       decision.UsageWindow{Remaining: 0.5, ResetsAt: time.Now().UTC().Add(48 * time.Hour)},
		Source:       "openusage",
		Confidence:   "high",
		BankedResets: &resets,
	}
	if err := db.SaveSnapshot(context.Background(), snapshot, nil); err != nil {
		t.Fatal(err)
	}

	got, _, err := db.LatestSnapshot(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.BankedResets == nil {
		t.Fatal("banked resets were dropped on the way through the store")
	}
	if *got.BankedResets != 2 {
		t.Fatalf("banked resets = %d, want 2", *got.BankedResets)
	}
}

// Zero and absent are different answers and must stay different across a
// round trip: none banked versus not reported.
func TestSQLiteKeepsZeroAndAbsentBankedResetsDistinct(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	zero := 0
	base := time.Now().UTC().Truncate(time.Second)

	withZero := decision.UsageSnapshot{
		Provider: "codex", ObservedAt: base,
		Weekly: decision.UsageWindow{Remaining: 0.5, ResetsAt: base.Add(48 * time.Hour)},
		Source: "openusage", Confidence: "high", BankedResets: &zero,
	}
	if err := db.SaveSnapshot(context.Background(), withZero, nil); err != nil {
		t.Fatal(err)
	}
	got, _, err := db.LatestSnapshot(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.BankedResets == nil || *got.BankedResets != 0 {
		t.Fatalf("zero must round trip as zero, got %v", got.BankedResets)
	}

	absent := decision.UsageSnapshot{
		Provider: "claude", ObservedAt: base,
		Weekly: decision.UsageWindow{Remaining: 0.5, ResetsAt: base.Add(48 * time.Hour)},
		Source: "openusage", Confidence: "high",
	}
	if err := db.SaveSnapshot(context.Background(), absent, nil); err != nil {
		t.Fatal(err)
	}
	got, _, err = db.LatestSnapshot(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.BankedResets != nil {
		t.Fatalf("absent must stay absent, got %d", *got.BankedResets)
	}
}

// ListSnapshots must carry banked resets too.
//
// The value was written, read by LatestSnapshot, and silently dropped by this
// one query. Nothing failed, because no current caller renders it -- which is
// exactly how the same field went missing end to end earlier in this work.
func TestListSnapshotsCarriesBankedResets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	resets := 3
	base := time.Now().UTC().Truncate(time.Second)
	snapshot := decision.UsageSnapshot{
		Provider:     "codex",
		ObservedAt:   base,
		Weekly:       decision.UsageWindow{Remaining: 0.5, ResetsAt: base.Add(48 * time.Hour)},
		Source:       "openusage",
		Confidence:   "high",
		BankedResets: &resets,
	}
	if err := db.SaveSnapshot(context.Background(), snapshot, nil); err != nil {
		t.Fatal(err)
	}

	listed, err := db.ListSnapshots(context.Background(), "codex", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected one snapshot, got %d", len(listed))
	}
	if listed[0].BankedResets == nil {
		t.Fatal("banked resets were dropped by ListSnapshots")
	}
	if *listed[0].BankedResets != 3 {
		t.Fatalf("banked resets = %d, want 3", *listed[0].BankedResets)
	}
}

// Every field of a snapshot must survive the store.
//
// Three fields have now been added to UsageSnapshot and two of them were
// silently dropped on write, because the table has hand-maintained columns
// rather than a blob: banked_resets was lost until it was noticed on a phone,
// and short_window_unavailable was still being lost after that.
//
// Reflection rather than a hand-written list, so the next field added fails
// here instead of on someone's screen. If this test fails after you add a
// field, the fix is a column, a migration, and a scan -- in SaveSnapshot,
// latestSnapshot, AND ListSnapshots.
func TestEverySnapshotFieldSurvivesTheStore(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	base := time.Now().UTC().Truncate(time.Second)
	resets := 7
	resetsExpire := base.Add(10 * 24 * time.Hour)
	// Every field set to something distinguishable from its zero value.
	full := decision.UsageSnapshot{
		Provider:   "codex",
		ObservedAt: base,
		Short:      &decision.UsageWindow{Remaining: 0.25, ResetsAt: base.Add(3 * time.Hour)},
		Weekly:     decision.UsageWindow{Remaining: 0.5, ResetsAt: base.Add(48 * time.Hour)},
		Allowances: []decision.AllowanceWindow{{
			Key: "weekly", SourceLabel: "Weekly", Scope: "account", Role: "weekly",
			Remaining: 0.5, ResetsAt: base.Add(48 * time.Hour), PeriodDurationSeconds: 604800,
		}},
		Source:                 "openusage",
		Confidence:             "medium",
		ShortWindowUnavailable: true,
		BankedResets:           &resets,
		BankedResetsExpireAt:   &resetsExpire,
	}

	// Fail loudly if a new field is added and this fixture was not updated,
	// rather than quietly testing a zero value.
	value := reflect.ValueOf(full)
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		if value.Field(i).IsZero() {
			t.Fatalf("fixture leaves %s at its zero value; set it so the round trip is actually tested", field.Name)
		}
	}

	if err := db.SaveSnapshot(context.Background(), full, nil); err != nil {
		t.Fatal(err)
	}

	for _, read := range []struct {
		name string
		get  func() (decision.UsageSnapshot, error)
	}{
		{"LatestSnapshot", func() (decision.UsageSnapshot, error) {
			got, _, err := db.LatestSnapshot(context.Background(), "codex")
			return got, err
		}},
		{"ListSnapshots", func() (decision.UsageSnapshot, error) {
			all, err := db.ListSnapshots(context.Background(), "codex", 10)
			if err != nil {
				return decision.UsageSnapshot{}, err
			}
			if len(all) == 0 {
				return decision.UsageSnapshot{}, fmt.Errorf("no snapshots returned")
			}
			return all[0], nil
		}},
	} {
		got, err := read.get()
		if err != nil {
			t.Fatalf("%s: %v", read.name, err)
		}
		gotValue := reflect.ValueOf(got)
		for i := 0; i < gotValue.NumField(); i++ {
			field := gotValue.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			if gotValue.Field(i).IsZero() {
				t.Errorf("%s dropped %s: it was set on write and came back as its zero value",
					read.name, field.Name)
			}
		}
	}
}
