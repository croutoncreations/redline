package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/resetsignal"
)

func TestResetSignalFiresaleBaseline(t *testing.T) {
	for _, withSnapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(withSnapshot), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "baseline.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			deadline := now.Add(time.Hour)
			snapshot := decision.UsageSnapshot{Provider: "main", Account: "main", ObservedAt: now.Add(-time.Minute), Weekly: decision.UsageWindow{Remaining: .4, ResetsAt: now.Add(24 * time.Hour)}}
			if withSnapshot {
				if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
					t.Fatal(err)
				}
			}
			a := resetsignal.Announcement{Key: "announcement:123", WindowEnd: &deadline}
			if ok, err := db.StartResetSignalFiresale(t.Context(), "main", "main", "auto", a, now, false); err != nil || !ok {
				t.Fatalf("start=%v err=%v", ok, err)
			}
			db.Close()
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			sales, err := db.ActiveResetSignalFiresales(t.Context())
			if err != nil || len(sales) != 1 {
				t.Fatalf("sales=%+v err=%v", sales, err)
			}
			sale := sales[0]
			if withSnapshot {
				if sale.BaselineWeeklyRemaining == nil || *sale.BaselineWeeklyRemaining != .4 || sale.BaselineWeeklyResetsAt == nil || !sale.BaselineWeeklyResetsAt.Equal(snapshot.Weekly.ResetsAt) || sale.BaselineObservedAt == nil || !sale.BaselineObservedAt.Equal(snapshot.ObservedAt) {
					t.Fatalf("baseline=%+v", sale)
				}
			} else {
				if sale.BaselineWeeklyRemaining != nil || sale.BaselineWeeklyResetsAt != nil || sale.BaselineObservedAt != nil {
					t.Fatalf("baseline=%+v", sale)
				}
			}
		})
	}
}

func TestResetSignalFiresaleMutationsApplyOnlyWhileActive(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "mutations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := t.Context()
	now := time.Date(2026, time.November, 24, 12, 0, 0, 0, time.UTC)
	resetsAt := now.Add(24 * time.Hour)
	observedAt := now.Add(-time.Minute)
	if err := db.SaveSnapshot(ctx, decision.UsageSnapshot{
		Provider:   "main",
		Account:    "main",
		ObservedAt: observedAt,
		Weekly: decision.UsageWindow{
			Remaining: 0.4,
			ResetsAt:  resetsAt,
		},
	}, nil); err != nil {
		t.Fatal(err)
	}

	deadline := now.Add(time.Hour)
	announcement := resetsignal.Announcement{
		Key:       "announcement:123",
		Kind:      resetsignal.KindPromised,
		Quote:     "original",
		WindowEnd: &deadline,
	}
	if ok, err := db.StartResetSignalFiresale(ctx, "main", "main", "auto", announcement, now, false); err != nil || !ok {
		t.Fatalf("start=%v err=%v", ok, err)
	}
	sales, err := db.ActiveResetSignalFiresales(ctx)
	if err != nil || len(sales) != 1 {
		t.Fatalf("sales=%+v err=%v", sales, err)
	}
	saleID := sales[0].ID

	if err := db.LowerResetSignalFiresaleBaseline(ctx, saleID, 0.2); err != nil {
		t.Fatal(err)
	}
	if err := db.LowerResetSignalFiresaleBaseline(ctx, saleID, 0.3); err != nil {
		t.Fatal(err)
	}
	updatedDeadline := deadline.Add(30 * time.Minute)
	updated := announcement
	updated.Quote = "updated"
	updated.WindowEnd = &updatedDeadline
	if changed, err := db.UpdateResetSignalFiresaleDeadline(ctx, saleID, updated); err != nil || !changed {
		t.Fatalf("update=%v err=%v", changed, err)
	}
	if changed, err := db.UpdateResetSignalFiresaleDeadline(ctx, saleID, updated); err != nil || changed {
		t.Fatalf("unchanged update=%v err=%v", changed, err)
	}

	sales, err = db.ActiveResetSignalFiresales(ctx)
	if err != nil || len(sales) != 1 {
		t.Fatalf("sales=%+v err=%v", sales, err)
	}
	sale := sales[0]
	if sale.BaselineWeeklyRemaining == nil || *sale.BaselineWeeklyRemaining != 0.2 {
		t.Fatalf("baseline=%v, want low-water mark 0.2", sale.BaselineWeeklyRemaining)
	}
	if sale.BaselineWeeklyResetsAt == nil || !sale.BaselineWeeklyResetsAt.Equal(resetsAt) || sale.BaselineObservedAt == nil || !sale.BaselineObservedAt.Equal(observedAt) {
		t.Fatalf("baseline metadata changed: %+v", sale)
	}
	if !sale.Deadline.Equal(updatedDeadline) || sale.Announcement.Quote != "updated" {
		t.Fatalf("updated sale=%+v", sale)
	}

	if ended, err := db.EndResetSignalFiresale(ctx, saleID, "stopped", now); err != nil || !ended {
		t.Fatalf("end=%v err=%v", ended, err)
	}
	if err := db.LowerResetSignalFiresaleBaseline(ctx, saleID, 0.1); err != nil {
		t.Fatal(err)
	}
	staleDeadline := updatedDeadline.Add(time.Hour)
	stale := updated
	stale.Quote = "stale"
	stale.WindowEnd = &staleDeadline
	if changed, err := db.UpdateResetSignalFiresaleDeadline(ctx, saleID, stale); err != nil || changed {
		t.Fatalf("ended update=%v err=%v", changed, err)
	}

	var storedBaseline float64
	var storedDeadline string
	var storedAnnouncement []byte
	if err := db.db.QueryRowContext(ctx, `SELECT baseline_weekly_remaining, deadline, announcement_json FROM reset_signal_firesales WHERE id=?`, saleID).Scan(&storedBaseline, &storedDeadline, &storedAnnouncement); err != nil {
		t.Fatal(err)
	}
	var persisted resetsignal.Announcement
	if err := json.Unmarshal(storedAnnouncement, &persisted); err != nil {
		t.Fatal(err)
	}
	if storedBaseline != 0.2 || storedDeadline != formatTime(updatedDeadline) || persisted.Quote != "updated" {
		t.Fatalf("ended sale mutated: baseline=%v deadline=%q announcement=%+v", storedBaseline, storedDeadline, persisted)
	}
}

func TestResetSignalFiresaleMigration40RepairsStampedSchema(t *testing.T) {
	for _, stamp := range []int{39, 40, 99} {
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
		if _, err := raw.Exec(`ALTER TABLE reset_signal_firesales DROP COLUMN baseline_weekly_remaining; ALTER TABLE reset_signal_firesales DROP COLUMN baseline_weekly_resets_at; ALTER TABLE reset_signal_firesales DROP COLUMN baseline_observed_at; DELETE FROM schema_migrations WHERE version>39; INSERT OR IGNORE INTO schema_migrations(version) VALUES (?)`, stamp); err != nil {
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
			want := max(LatestSchemaVersion, stamp)
			if err := db.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != want {
				t.Fatalf("version=%d want=%d err=%v", version, want, err)
			}
			db.Close()
		}
	}
}

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
	claimConcurrently(func() (bool, error) {
		return db.StartResetSignalFiresale(t.Context(), "main", "main", "auto", a, now, false)
	})
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
			if err := db.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != max(LatestSchemaVersion, stamp) {
				t.Fatalf("version=%d want=%d err=%v", version, max(LatestSchemaVersion, stamp), err)
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
		ok, err := db.StartResetSignalFiresale(t.Context(), "main", "main", "auto", a, now, manual)
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
