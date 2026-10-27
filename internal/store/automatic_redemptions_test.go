package store_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
)

func TestLastBankedResetExpiryIsAccountScoped(t *testing.T) {
	db := openTaskDB(t)
	now := time.Now().UTC()
	for i, account := range []string{"first", "second"} {
		expires := now.Add(time.Duration(i+1) * time.Hour)
		snapshot := usageSnapshot(now.Add(time.Duration(i)*time.Minute), .5)
		snapshot.Account = account
		snapshot.ApplyBankedResets(&decision.BankedResets{Available: 1, Details: []decision.BankedReset{{ID: "same-credit", ExpiresAt: &expires, ResetsLeft: 1}}})
		if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
			t.Fatal(err)
		}
	}
	for i, account := range []string{"first", "second"} {
		expires, err := db.LastBankedResetExpiry(t.Context(), account, "codex", "same-credit")
		if err != nil || expires == nil || !expires.Equal(now.Add(time.Duration(i+1)*time.Hour)) {
			t.Fatalf("account=%s expiry=%v err=%v", account, expires, err)
		}
	}
	expires, err := db.LastBankedResetExpiry(t.Context(), "absent", "codex", "same-credit")
	if err != nil || expires != nil {
		t.Fatalf("unconfigured account read another account's deadline: %v %v", expires, err)
	}
}

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
			if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != max(stamp, 38) {
				t.Fatalf("version=%d err=%v", version, err)
			}
		})
	}
}
