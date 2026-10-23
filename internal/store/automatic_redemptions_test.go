package store_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/store"
)

func TestAutomaticBankedResetRedemptionsMigration36AndPersistence(t *testing.T) {
	for _, stamp := range []int{35, 36, 99} {
		t.Run(fmt.Sprint(stamp), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auto.db")
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
			if _, err := raw.Exec(`DROP TABLE automatic_banked_reset_redemptions; DELETE FROM schema_migrations WHERE version >= 36`); err != nil {
				t.Fatal(err)
			}
			if stamp >= 36 {
				if _, err := raw.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, stamp); err != nil {
					t.Fatal(err)
				}
			}
			db, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			a := store.AutomaticBankedResetRedemption{ProviderAccountID: "main", ResetID: "reset", ExpiresAt: now.Add(time.Hour), LastAttemptAt: now, RetryAt: now.Add(5 * time.Minute), Outcome: "failed"}
			if err := db.SaveAutomaticBankedResetRedemption(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			a.RetryAt, a.Outcome = now.Add(30*time.Minute), "nothing_to_reset"
			if err := db.SaveAutomaticBankedResetRedemption(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			got, err := db.AutomaticBankedResetRedemptions(t.Context(), "main")
			if err != nil || len(got) != 1 || got[0] != a {
				t.Fatalf("got=%+v want=%+v err=%v", got, a, err)
			}
			other, err := db.AutomaticBankedResetRedemptions(t.Context(), "other")
			if err != nil || len(other) != 0 {
				t.Fatalf("provider isolation: %+v %v", other, err)
			}
			var version int
			if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != max(stamp, 37) {
				t.Fatalf("version=%d err=%v", version, err)
			}
		})
	}
}
