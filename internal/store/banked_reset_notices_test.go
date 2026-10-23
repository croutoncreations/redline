package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/store"
)

func TestBankedResetNoticesMigrationUpgradesV28(t *testing.T) {
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
	defer raw.Close()
	if _, err := raw.Exec(`DROP TABLE banked_reset_notices; DELETE FROM schema_migrations WHERE version >= 33`); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 35 {
		t.Fatalf("version=%d", version)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	inserted, err := db.RecordBankedResetNotice(t.Context(), "claude-main", "grant", now.Add(time.Hour), "expiry_action", now)
	if err != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	var createdAt string
	if err := raw.QueryRow(`SELECT created_at FROM banked_reset_notices`).Scan(&createdAt); err != nil {
		t.Fatal(err)
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || !created.Equal(now) {
		t.Fatalf("created_at=%q error=%v", createdAt, err)
	}
}

func TestRecordBankedResetNoticeConcurrentClaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	first, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	var inserted atomic.Int32
	var workers sync.WaitGroup
	for i := range 10 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			db := first
			if i%2 == 0 {
				db = second
			}
			claimed, err := db.RecordBankedResetNotice(t.Context(), "claude-main", "grant", now.Add(time.Hour), "expiry_action", now)
			if err != nil {
				t.Error(err)
				return
			}
			if claimed {
				inserted.Add(1)
			}
		}()
	}
	workers.Wait()
	if got := inserted.Load(); got != 1 {
		t.Fatalf("claims=%d", got)
	}
}

func TestRecordBankedResetNoticeDeduplicatesDeadlineAndKind(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	for i, tc := range []struct {
		provider, reset, kind string
		expiry                time.Time
		want                  bool
	}{
		{"claude-main", "grant", "remind:1h0m0s", expires, true},
		{"claude-main", "grant", "remind:1h0m0s", expires.In(time.FixedZone("other", 3600)), false},
		{"claude-main", "grant", "expiry_action", expires, true},
		{"claude-other", "grant", "remind:1h0m0s", expires, true},
		{"claude-main", "other-grant", "remind:1h0m0s", expires, false},
		{"claude-main", "grant", "remind:1h0m0s", expires.Add(time.Hour), true},
	} {
		got, err := db.RecordBankedResetNotice(ctx, tc.provider, tc.reset, tc.expiry, tc.kind, now)
		if err != nil || got != tc.want {
			t.Fatalf("case %d: inserted=%v error=%v", i, got, err)
		}
	}
}
