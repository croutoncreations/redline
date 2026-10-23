package store_test

import (
	"context"
	"database/sql"
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
			if err != nil || len(rows) != 20 || rows[0].ResetID != "24" {
				t.Fatalf("%+v %v", rows, err)
			}
			if _, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{ProviderAccountID: "other", ResetID: "other", RequestID: "24", Trigger: "manual", CreatedAt: time.Now()}); err == nil {
				t.Fatal("request ID not unique")
			}
			var version int
			if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != max(stamp, 35) {
				t.Fatalf("version=%d err=%v", version, err)
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
	a, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "grant", RequestID: "first", Trigger: "manual", CreatedAt: now})
	if err != nil || a.State != "pending" {
		t.Fatalf("%+v %v", a, err)
	}
	_ = db.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, err := db.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{ProviderAccountID: "main", ResetID: "grant", RequestID: "second", Trigger: "manual", CreatedAt: now})
	if err != nil || b.ID != a.ID || b.RequestID != "first" {
		t.Fatalf("%+v %v", b, err)
	}
	if err := db.CompleteBankedResetRedemption(ctx, b.ID, "redeemed", "reset", "ok", now); err != nil {
		t.Fatal(err)
	}
	rows, err := db.BankedResetRedemptions(ctx, "main", 20)
	if err != nil || len(rows) != 1 || rows[0].State != "done" || rows[0].Outcome != "redeemed" || rows[0].CompletedAt == nil {
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
