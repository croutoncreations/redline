package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
	_ "modernc.org/sqlite"
)

// The snapshot table has explicit columns, so a new field is silently dropped
// unless it is written and read on every path. This happened to banked resets
// once already on the mobile branch.
func TestBankedResetsSurviveEveryReadPath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	base := time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)
	expires := time.Date(2026, 10, 22, 23, 59, 59, 0, time.UTC)

	with := usageSnapshot(base, .5)
	with.ApplyBankedResets(&decision.BankedResets{Available: 1, NextExpiresAt: &expires})
	zero := usageSnapshot(base.Add(time.Minute), .5)
	zero.ApplyBankedResets(&decision.BankedResets{Available: 0})
	absent := usageSnapshot(base.Add(2*time.Minute), .5)
	for _, s := range []decision.UsageSnapshot{with, zero, absent} {
		if err := db.SaveSnapshot(ctx, s, nil); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := db.ListSnapshots(ctx, "codex", 10)
	if err != nil || len(listed) != 3 {
		t.Fatalf("listed=%d err=%v", len(listed), err)
	}
	if listed[0].BankedResets == nil || *listed[0].BankedResets != 1 || listed[0].BankedResetsExpireAt == nil || !listed[0].BankedResetsExpireAt.Equal(expires) {
		t.Fatalf("with resets: %v %v", listed[0].BankedResets, listed[0].BankedResetsExpireAt)
	}
	if listed[1].BankedResets == nil || *listed[1].BankedResets != 0 {
		t.Fatalf("zero must stay zero, got %v", listed[1].BankedResets)
	}
	if listed[2].BankedResets != nil {
		t.Fatalf("absent must stay absent, got %v", *listed[2].BankedResets)
	}

	latest, _, err := db.LatestSnapshot(ctx, "codex")
	if err != nil || latest.BankedResets != nil {
		t.Fatalf("latest absent: %v %v", latest.BankedResets, err)
	}
	if err := db.SaveSnapshot(ctx, func() decision.UsageSnapshot { s := with; s.ObservedAt = base.Add(time.Hour); return s }(), nil); err != nil {
		t.Fatal(err)
	}
	latest, _, err = db.LatestSnapshot(ctx, "codex")
	if err != nil || latest.BankedResets == nil || *latest.BankedResets != 1 || latest.BankedResetsExpireAt == nil {
		t.Fatalf("latest with resets: %v %v %v", latest.BankedResets, latest.BankedResetsExpireAt, err)
	}
}

func TestBankedResetDetailsSurviveEveryReadPath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	expires, cooldown := now.Add(time.Hour), now.Add(time.Minute)
	usable, requires := false, true
	s := usageSnapshot(now, .5)
	s.ApplyBankedResets(&decision.BankedResets{Available: 3, Details: []decision.BankedReset{
		{ID: "g", ExpiresAt: &expires, Clears: []string{"five_hour", "weekly"}, UsableNow: &usable, RequiresLimit: &requires, CooldownUntil: &cooldown, ResetsLeft: 2, Claimable: true},
		{ID: "no-expiry", ResetsLeft: 1},
	}})
	if err := db.SaveSnapshot(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	latest, _, err := db.LatestSnapshot(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	fromSource, _, err := db.LatestSnapshotFromSource(ctx, "codex", s.Source)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := db.ListSnapshots(ctx, "codex", 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v %v", listed, err)
	}
	for _, got := range []decision.UsageSnapshot{latest, fromSource, listed[0]} {
		if !reflect.DeepEqual(got.BankedResetDetails, s.BankedResetDetails) {
			t.Fatalf("details = %#v, want %#v", got.BankedResetDetails, s.BankedResetDetails)
		}
	}
}

func TestCorruptBankedResetDetailsDoNotBlockSnapshotReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for i := range 2 {
		s := usageSnapshot(now.Add(time.Duration(i)*time.Minute), .5)
		s.ApplyBankedResets(&decision.BankedResets{Available: 1, Details: []decision.BankedReset{{ID: "g", ResetsLeft: 1}}})
		if err := db.SaveSnapshot(ctx, s, nil); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// A type error after a valid item also tests that partially decoded details
	// are discarded instead of being used as a trustworthy report.
	if _, err := raw.Exec(`UPDATE usage_snapshots SET banked_reset_details = ? WHERE id = (SELECT MAX(id) FROM usage_snapshots)`, `[{"id":"partial","resets_left":1},{"id":42}]`); err != nil {
		t.Fatal(err)
	}
	listed, err := db.ListSnapshots(ctx, "codex", 10)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list: rows=%d err=%v", len(listed), err)
	}
	if len(listed[0].BankedResetDetails) != 1 || listed[0].BankedResetDetails[0].ID != "g" {
		t.Fatalf("uncorrupted details lost: %#v", listed[0])
	}
	latest, _, err := db.LatestSnapshot(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	fromSource, _, err := db.LatestSnapshotFromSource(ctx, "codex", latest.Source)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []decision.UsageSnapshot{listed[1], latest, fromSource} {
		if got.BankedResetDetails != nil || got.BankedResets == nil || *got.BankedResets != 1 || got.Weekly.Remaining != .5 || !got.ObservedAt.Equal(now.Add(time.Minute)) {
			t.Fatalf("corrupt details must not affect usage or aggregate: %#v", got)
		}
	}
}

// A database that ran the mobile branch already has banked_resets at
// schema 24/25; migrating must add only what is missing.
func TestBankedResetsMigrationToleratesMobileBranchSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Mobile-branch history: v24 added banked_resets (not main's timestamp
	// rewrite), v25 another column, and rows still carry legacy RFC3339.
	for _, stmt := range []string{
		`DELETE FROM schema_migrations WHERE version >= 24`,
		`ALTER TABLE usage_snapshots DROP COLUMN banked_resets_expire_at`,
		`INSERT INTO schema_migrations(version) VALUES (24), (25)`,
		// One nanosecond older than the snapshot saved below, but unless
		// the rewrite runs, legacy "…T14:00:00Z" sorts after fixed-width
		// "…T14:00:00.000000001Z" byte-wise and would read as the latest.
		`INSERT INTO usage_snapshots (provider, observed_at, weekly_remaining, weekly_resets_at, source, confidence)
		 VALUES ('codex', '2026-09-24T14:00:00Z', 0.5, '2026-09-30T00:00:00Z', 'openusage', 'high')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	raw.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen after mobile-branch schema: %v", err)
	}
	defer db.Close()
	expires := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	s := usageSnapshot(time.Date(2026, 9, 24, 14, 0, 0, 1, time.UTC), .5)
	s.ApplyBankedResets(&decision.BankedResets{Available: 1, NextExpiresAt: &expires})
	if err := db.SaveSnapshot(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}
	got, _, err := db.LatestSnapshot(context.Background(), "codex")
	if err != nil || got.BankedResetsExpireAt == nil {
		t.Fatalf("expiry lost after migration: %v %v", got.BankedResetsExpireAt, err)
	}
	// The skipped v24 rewrite must have run, or the legacy row would be
	// returned as the latest.
	if got.BankedResets == nil || got.ObservedAt.Nanosecond() != 1 {
		t.Fatalf("legacy row sorted as latest; timestamps were not normalized: %#v", got)
	}
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var stored string
	if err := raw.QueryRow(`SELECT observed_at FROM usage_snapshots ORDER BY id LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "2026-09-24T14:00:00Z" {
		t.Fatalf("legacy observed_at %q was not rewritten to the fixed-width layout", stored)
	}
}
