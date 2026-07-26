package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/store"
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
ALTER TABLE usage_allowance_windows DROP COLUMN reset_inferred;
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
