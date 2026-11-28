package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/store"
)

func TestBankedResetRedemptionsMigration35SchemaGuardAndLimit(t *testing.T) {
	for _, stamp := range []int{34, 35, 99} {
		t.Run(fmt.Sprint(stamp), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "schema.db")
			db, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`DROP TABLE banked_reset_redemptions; DELETE FROM schema_migrations WHERE version >= 35`); err != nil {
				t.Fatal(err)
			}
			if stamp >= 35 {
				if _, err := raw.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, stamp); err != nil {
					t.Fatal(err)
				}
			}
			db, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			for i := range 25 {
				_, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{ProviderAccountID: "main", ResetID: fmt.Sprint(i), RequestID: fmt.Sprint(i), Trigger: "dry_run", CreatedAt: time.Now()})
				if err != nil {
					t.Fatal(err)
				}
			}
			rows, err := db.BankedResetRedemptions(ctx, "main", 100)
			if err != nil || len(rows) != 20 || rows[0].ResetID != "24" || rows[0].ResetsLeftBefore != nil {
				t.Fatalf("%+v %v", rows, err)
			}
			if _, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{ProviderAccountID: "other", ResetID: "other", RequestID: "24", Trigger: "manual", CreatedAt: time.Now()}); err == nil {
				t.Fatal("request ID not unique")
			}
			var version int
			if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != max(stamp, 44) {
				t.Fatalf("version=%d err=%v", version, err)
			}
		})
	}
}

func TestBankedResetRedemptionsMigration37SchemaGuard(t *testing.T) {
	for _, stamp := range []int{36, 37, 99} {
		t.Run(fmt.Sprint(stamp), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "migration37.db")
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
			defer raw.Close()
			// Model an old schema even if a colliding branch already stamped 37+.
			var columns int
			if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('banked_reset_redemptions') WHERE name='resets_left_before'`).Scan(&columns); err != nil {
				t.Fatal(err)
			}
			if columns != 0 {
				if _, err := raw.Exec(`ALTER TABLE banked_reset_redemptions DROP COLUMN resets_left_before`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := raw.Exec(`DELETE FROM schema_migrations WHERE version >= 37`); err != nil {
				t.Fatal(err)
			}
			if stamp >= 37 {
				if _, err := raw.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, stamp); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				db, err = store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				var typ string
				var notNull int
				if err := raw.QueryRow(`SELECT type, "notnull" FROM pragma_table_info('banked_reset_redemptions') WHERE name='resets_left_before'`).Scan(&typ, &notNull); err != nil || typ != "INTEGER" || notNull != 0 {
					t.Fatalf("column type=%q notnull=%d err=%v", typ, notNull, err)
				}
				var version, stamps int
				wantStamps := 1
				if stamp > 37 {
					wantStamps = 0
				}
				if err := raw.QueryRow(`SELECT MAX(version), COUNT(CASE WHEN version=37 THEN 1 END) FROM schema_migrations`).Scan(&version, &stamps); err != nil || version != max(stamp, 44) || stamps != wantStamps {
					t.Fatalf("version=%d stamps37=%d err=%v", version, stamps, err)
				}
			}
		})
	}
}

func TestBankedResetRedemptionsPersistPendingRetryAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redemptions.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	before := 2
	a, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "grant", RequestID: "first", Trigger: "manual", CreatedAt: now, ResetsLeftBefore: &before})
	if err != nil || a.State != "pending" {
		t.Fatalf("%+v %v", a, err)
	}
	_ = db.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	changed := 1
	b, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "grant", RequestID: "second", Trigger: "manual", CreatedAt: now, ResetsLeftBefore: &changed})
	if err != nil || b.ID != a.ID || b.RequestID != "first" || b.ResetsLeftBefore == nil || *b.ResetsLeftBefore != before {
		t.Fatalf("%+v %v", b, err)
	}
	if err := db.CompleteBankedResetRedemption(ctx, b.ID, "redeemed", "reset", "ok", now); err != nil {
		t.Fatal(err)
	}
	rows, err := db.BankedResetRedemptions(ctx, "main", 20)
	if err != nil || len(rows) != 1 || rows[0].State != "done" || rows[0].Outcome != "redeemed" || rows[0].CompletedAt == nil || rows[0].ResetsLeftBefore == nil || *rows[0].ResetsLeftBefore != before {
		t.Fatalf("%+v %v", rows, err)
	}
	rows, err = db.BankedResetRedemptions(ctx, "other", 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
	// Deterministic Codex keys may identify an already completed operation.
	c, err := db.BeginBankedResetRedemption(ctx, a)
	if err != nil || c.ID != a.ID || c.State != "done" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestBankedResetRedemptionAmbiguousErrorPreservesPendingRetry(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redemptions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := t.Context()
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	attempt, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{
		ProviderAccountID: "main",
		ResetID:           "weekly-reset",
		RequestID:         "original-key",
		Trigger:           "manual",
		CreatedAt:         now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordBankedResetRedemptionError(ctx, attempt.ID, "response lost"); err != nil {
		t.Fatal(err)
	}

	pending, err := db.PendingBankedResetRedemption(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if pending.ID != attempt.ID || pending.State != "pending" || pending.Outcome != "failed" || pending.Reason != "response lost" {
		t.Fatalf("pending=%+v", pending)
	}

	retry, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{
		ProviderAccountID: "main",
		ResetID:           "weekly-reset",
		RequestID:         "replacement-key",
		Trigger:           "manual",
		CreatedAt:         now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != attempt.ID || retry.RequestID != "original-key" {
		t.Fatalf("retry=%+v, want persisted id=%d request_id=original-key", retry, attempt.ID)
	}

	completedAt := now.Add(2 * time.Minute)
	if err := db.CompleteBankedResetRedemption(ctx, attempt.ID, "redeemed", "reset", "confirmed", completedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordBankedResetRedemptionError(ctx, attempt.ID, "late transport error"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PendingBankedResetRedemption(ctx, "main"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pending lookup error=%v, want ErrNotFound", err)
	}

	rows, err := db.BankedResetRedemptions(ctx, "main", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Outcome != "redeemed" || rows[0].Reason != "confirmed" || rows[0].CompletedAt == nil || !rows[0].CompletedAt.Equal(completedAt) {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestBankedResetRedemptionQueriesExcludeDryRunsAndFailures(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redemptions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := t.Context()
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	begin := func(resetID, trigger string, offset time.Duration) store.BankedResetRedemption {
		t.Helper()
		attempt, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{
			ProviderAccountID: "main",
			ResetID:           resetID,
			RequestID:         resetID + "-key",
			Trigger:           trigger,
			CreatedAt:         now.Add(offset),
		})
		if err != nil {
			t.Fatal(err)
		}
		return attempt
	}
	complete := func(attempt store.BankedResetRedemption, outcome string, offset time.Duration) {
		t.Helper()
		if err := db.CompleteBankedResetRedemption(ctx, attempt.ID, outcome, "", "", now.Add(offset)); err != nil {
			t.Fatal(err)
		}
	}

	dryRun := begin("dry-run", "dry_run", 0)
	if _, err := db.PendingBankedResetRedemption(ctx, "main"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dry-run pending lookup error=%v, want ErrNotFound", err)
	}
	complete(dryRun, "redeemed", time.Minute)
	complete(begin("failed-real", "manual", 2*time.Minute), "failed", 3*time.Minute)
	complete(begin("older-real", "manual", 4*time.Minute), "redeemed", 5*time.Minute)
	complete(begin("newer-real", "automatic", 6*time.Minute), "redeemed", 7*time.Minute)

	redeemed, err := db.RedeemedBankedResetRedemptions(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(redeemed) != 2 || redeemed[0].ResetID != "newer-real" || redeemed[1].ResetID != "older-real" {
		t.Fatalf("redeemed=%+v, want only successful real attempts newest first", redeemed)
	}
}
