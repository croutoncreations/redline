package store_test

import (
	"database/sql"
	_ "embed"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
)

//go:embed testdata/main-schema-43.sql
var mainSchema43 string

// A database created by main is already at schema 43 but never ran the
// mobile and multi-account migrations 28–31. Opening it must add their
// columns and the monthly allowance role anyway, keep its data, and adopt
// it for the provider's only account.
func TestOpenUpgradesADatabaseCreatedByMain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(mainSchema43); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.AdoptLegacyUsage(t.Context(), map[string][]string{"claude": {"claude-main"}}); err != nil {
		t.Fatalf("adopt legacy usage: %v", err)
	}
	legacy, _, err := db.LatestSnapshot(t.Context(), "claude-main", "claude")
	if err != nil || legacy.Weekly.Remaining != .5 {
		t.Fatalf("legacy snapshot = %+v err=%v", legacy, err)
	}

	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	snapshot := decision.UsageSnapshot{Account: "cursor-main", Provider: "cursor", Source: "openusage", Confidence: "high", ObservedAt: now,
		Weekly:                decision.UsageWindow{Remaining: .7, ResetsAt: now.Add(20 * 24 * time.Hour), PeriodSeconds: 2592000},
		ShortWindowNotStarted: true, WeeklyUnavailable: false, Plan: "Pro",
		Allowances: []decision.AllowanceWindow{{Key: "weekly", SourceLabel: "Monthly", Scope: "account", Role: "monthly",
			Remaining: .7, ResetsAt: now.Add(20 * 24 * time.Hour), PeriodDurationSeconds: 2592000}}}
	if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
		t.Fatalf("save a snapshot using the new columns: %v", err)
	}
	got, _, err := db.LatestSnapshot(t.Context(), "cursor-main", "cursor")
	if err != nil || got.Plan != "Pro" || !got.ShortWindowNotStarted || len(got.Allowances) != 1 || got.Allowances[0].Role != "monthly" {
		t.Fatalf("snapshot = %+v err=%v", got, err)
	}

	// Reopening a migrated database changes nothing.
	db.Close()
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var before, after, versions int
	if err := check.QueryRow(`PRAGMA schema_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version BETWEEN 28 AND 31`).Scan(&versions); err != nil || versions != 4 {
		t.Fatalf("migrations 28-31 recorded %d times, err=%v", versions, err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	if err := check.QueryRow(`PRAGMA schema_version`).Scan(&after); err != nil || before != after {
		t.Fatalf("reopen changed schema: %d -> %d, err=%v", before, after, err)
	}
}
