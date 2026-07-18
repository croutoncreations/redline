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
