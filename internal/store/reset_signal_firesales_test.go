package store

import (
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/resetsignal"
)

func TestResetSignalFiresaleClaimsAreAtomic(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	deadline := now.Add(time.Hour)
	a := resetsignal.Announcement{Key: "announcement:123", Kind: resetsignal.KindPromised, WindowEnd: &deadline}
	claimConcurrently := func(claim func() (bool, error)) {
		t.Helper()
		var claimed atomic.Int32
		var workers sync.WaitGroup
		for range 12 {
			workers.Go(func() {
				ok, err := claim()
				if err != nil {
					t.Error(err)
				}
				if ok {
					claimed.Add(1)
				}
			})
		}
		workers.Wait()
		if claimed.Load() != 1 {
			t.Fatalf("claims=%d", claimed.Load())
		}
	}
	claimConcurrently(func() (bool, error) { return db.StartResetSignalFiresale(t.Context(), "main", "auto", a, now, false) })
	rows, err := db.ActiveResetSignalFiresales(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	claimConcurrently(func() (bool, error) { return db.EndResetSignalFiresale(t.Context(), rows[0].ID, "stopped", now) })
}

func TestResetSignalFiresaleMigration39RepairsStampedSchema(t *testing.T) {
	for _, stamp := range []int{39, 99} {
		path := filepath.Join(t.TempDir(), "migration.db")
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`DROP TABLE reset_signal_firesales; INSERT OR IGNORE INTO schema_migrations(version) VALUES (?)`, stamp); err != nil {
			t.Fatal(err)
		}
		raw.Close()
		for range 2 {
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ActiveResetSignalFiresales(t.Context()); err != nil {
				t.Fatal(err)
			}
			var version int
			if err := db.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != max(stamp, 40) {
				t.Fatalf("version=%d want=%d err=%v", version, max(stamp, 40), err)
			}
			db.Close()
		}
	}
}

func TestResetSignalFiresaleClaimsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firesale.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	deadline := now.Add(time.Hour)
	a := resetsignal.Announcement{Key: "announcement:123", Kind: resetsignal.KindPromised, WindowEnd: &deadline}
	claim := func(manual bool, want bool) {
		t.Helper()
		ok, err := db.StartResetSignalFiresale(t.Context(), "main", "auto", a, now, manual)
		if err != nil || ok != want {
			t.Fatalf("start=%v err=%v want=%v", ok, err, want)
		}
	}
	claim(false, true)
	claim(false, false)
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.ActiveResetSignalFiresales(t.Context())
	if err != nil || len(rows) != 1 || !rows[0].Deadline.Equal(deadline) {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for i := 0; i < 2; i++ {
		ok, err := db.EndResetSignalFiresale(t.Context(), rows[0].ID, "stopped", now)
		if err != nil || ok != (i == 0) {
			t.Fatalf("end=%v err=%v", ok, err)
		}
	}
	claim(false, false)
	claim(true, true)
	rows, _ = db.ActiveResetSignalFiresales(t.Context())
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	if _, err := db.EndResetSignalFiresale(t.Context(), rows[0].ID, "disabled", now); err != nil {
		t.Fatal(err)
	}
	claim(false, true)
	a.Key = "announcement:456"
	claim(true, false) // unique active provider, even across keys
}
