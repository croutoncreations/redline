package store

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManualFiresaleClaimsAndExtension(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "manual.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	reset := now.Add(24 * time.Hour)
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, claimed, err := db.StartManualFiresale(t.Context(), "claude-main", now, now.Add(time.Hour), "owner", &reset)
			if err != nil {
				t.Error(err)
			}
			if claimed {
				claims.Add(1)
			}
		})
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claims=%d", claims.Load())
	}
	sale, err := db.ActiveManualFiresaleFor(t.Context(), "claude-main")
	if err != nil {
		t.Fatal(err)
	}
	later := reset.Add(7 * 24 * time.Hour)
	extended, claimed, err := db.StartManualFiresale(t.Context(), "claude-main", now.Add(time.Minute), now.Add(2*time.Hour), "device:Phone", &later)
	if err != nil || claimed || extended.ID != sale.ID || !extended.Deadline.Equal(now.Add(2*time.Hour)) || extended.StartedBy != "owner" || !extended.StartedAt.Equal(now) || !extended.BaselineWeeklyResetsAt.Equal(reset) {
		t.Fatalf("extended=%+v claimed=%v err=%v", extended, claimed, err)
	}
	if _, err := db.db.Exec(`INSERT INTO manual_firesales(provider_account_id,started_at,deadline,started_by) VALUES ('claude-main','x','x','owner')`); err == nil {
		t.Fatal("active duplicate accepted")
	}
	for i := range 2 {
		claimed, err := db.EndManualFiresale(t.Context(), sale.ID, "stopped", now)
		if err != nil || claimed != (i == 0) {
			t.Fatalf("end claimed=%v err=%v", claimed, err)
		}
	}
	if _, err := db.ActiveManualFiresaleFor(t.Context(), "claude-main"); err != ErrNotFound {
		t.Fatalf("ended lookup=%v", err)
	}
	if _, claimed, err := db.StartManualFiresale(t.Context(), "claude-main", now, reset, "owner", nil); err != nil || !claimed {
		t.Fatalf("restart claimed=%v err=%v", claimed, err)
	}
}

func TestManualFiresaleSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reset := now.Add(24 * time.Hour)
	sale, claimed, err := db.StartManualFiresale(t.Context(), "main", now, now.Add(time.Hour), "device:Phone", &reset)
	if err != nil || !claimed {
		t.Fatalf("start claimed=%v err=%v", claimed, err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.ActiveManualFiresaleFor(t.Context(), "main")
	if err != nil || got.ID != sale.ID || got.StartedBy != "device:Phone" || !got.StartedAt.Equal(now) || !got.Deadline.Equal(sale.Deadline) || !got.BaselineWeeklyResetsAt.Equal(reset) {
		t.Fatalf("sale=%+v err=%v", got, err)
	}
}

func TestManualFiresaleMigration43RepairsCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DROP TABLE manual_firesales; INSERT OR IGNORE INTO schema_migrations(version) VALUES(43),(99)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for range 2 {
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ActiveManualFiresales(t.Context()); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=43`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("count=%d err=%v", count, err)
		}
		db.Close()
	}
}
