package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
	_ "modernc.org/sqlite"
)

// An existing database at schema 28 has allowance rows under the old role
// CHECK, and may hold rows orphaned by a hand-run prune with foreign keys off.
// Migration 29 must keep every live row, drop the orphan instead of failing to
// open, and then accept monthly allowances with their period intact.
func TestMonthlyAllowanceMigrationUpgradesPopulatedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	if err := db.SaveSnapshot(ctx, usageSnapshot(base, .5), nil); err != nil {
		t.Fatal(err)
	}
	db.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var liveRows int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM usage_allowance_windows`).Scan(&liveRows); err != nil || liveRows == 0 {
		t.Fatalf("seed rows=%d err=%v", liveRows, err)
	}
	// Rebuild the table as schema 28 had it, then add an orphan the way the
	// sqlite3 shell would (foreign keys off by default).
	for _, stmt := range []string{
		`DELETE FROM schema_migrations WHERE version >= 29`,
		`ALTER TABLE usage_allowance_windows RENAME TO allowance_v29`,
		`CREATE TABLE usage_allowance_windows (
		    snapshot_id INTEGER NOT NULL REFERENCES usage_snapshots(id) ON DELETE CASCADE,
		    pool_key TEXT NOT NULL,
		    source_label TEXT NOT NULL,
		    scope TEXT NOT NULL CHECK(scope IN ('account', 'model')),
		    role TEXT NOT NULL CHECK(role IN ('short', 'weekly')),
		    remaining REAL NOT NULL CHECK(remaining >= 0 AND remaining <= 1),
		    resets_at TEXT NOT NULL,
		    period_duration_s INTEGER NOT NULL CHECK(period_duration_s > 0),
		    reset_inferred INTEGER NOT NULL DEFAULT 0,
		    PRIMARY KEY(snapshot_id, pool_key))`,
		`INSERT INTO usage_allowance_windows SELECT * FROM allowance_v29`,
		`DROP TABLE allowance_v29`,
		`CREATE INDEX idx_allowance_windows_pool_reset ON usage_allowance_windows(pool_key, resets_at)`,
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO usage_allowance_windows VALUES (9999, 'weekly', 'orphan', 'account', 'weekly', 0.5, '2026-09-30T00:00:00Z', 604800, 0)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	raw.Close()

	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("open after schema 28 with an orphan row: %v", err)
	}
	defer db.Close()

	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var kept, orphans int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM usage_allowance_windows WHERE snapshot_id != 9999`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM usage_allowance_windows WHERE snapshot_id = 9999`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if kept != liveRows || orphans != 0 {
		t.Fatalf("kept=%d want %d, orphans=%d", kept, liveRows, orphans)
	}
	var index string
	if err := raw.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_allowance_windows_pool_reset'`).Scan(&index); err != nil {
		t.Fatalf("index missing after rebuild: %v", err)
	}

	month := int64(30 * 24 * 60 * 60)
	cursor := decision.UsageSnapshot{
		Provider: "cursor", ObservedAt: base.Add(time.Hour),
		Weekly: decision.UsageWindow{Remaining: .6, ResetsAt: base.Add(15 * 24 * time.Hour), PeriodSeconds: month},
		Allowances: []decision.AllowanceWindow{{
			Key: "model:cursor_models:monthly", SourceLabel: "Cursor Models", Scope: "model", Role: "monthly",
			Remaining: .9, ResetsAt: base.Add(15 * 24 * time.Hour), PeriodDurationSeconds: month,
		}},
		Source: "openusage", Confidence: "high",
	}
	if err := db.SaveSnapshot(ctx, cursor, nil); err != nil {
		t.Fatalf("save monthly allowance after upgrade: %v", err)
	}
	got, _, err := db.LatestSnapshot(ctx, "", "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if got.Weekly.Period() != time.Duration(month)*time.Second {
		t.Fatalf("account period = %v, want 30 days", got.Weekly.Period())
	}
	pool, ok := got.Allowance("model:cursor_models:monthly")
	if !ok || pool.Role != "monthly" || pool.PeriodDurationSeconds != month {
		t.Fatalf("monthly pool = %#v ok=%v", pool, ok)
	}
}
