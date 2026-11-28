package storetest_test

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/store/storetest"
	_ "modernc.org/sqlite"
)

// schemaOf returns every schema object plus the recorded migration versions,
// so a drift between a seeded store and a really migrated one is caught.
func schemaOf(t *testing.T, path string) (objects [][3]string, versions []int) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rows, err := raw.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var o [3]string
		if err := rows.Scan(&o[0], &o[1], &o[2]); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, o)
	}
	vrows, err := raw.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer vrows.Close()
	for vrows.Next() {
		var v int
		if err := vrows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	return objects, versions
}

func TestOpenMatchesRealFreshMigration(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.db")
	seededPath := filepath.Join(dir, "seeded.db")

	real, err := store.Open(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := real.Close(); err != nil {
		t.Fatal(err)
	}
	seeded, err := storetest.Open(seededPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := seeded.Close(); err != nil {
		t.Fatal(err)
	}

	wantObjects, wantVersions := schemaOf(t, realPath)
	gotObjects, gotVersions := schemaOf(t, seededPath)
	if len(wantObjects) == 0 || len(wantVersions) == 0 {
		t.Fatalf("real migration produced no schema: %d objects, %d versions", len(wantObjects), len(wantVersions))
	}
	if !reflect.DeepEqual(gotObjects, wantObjects) {
		t.Fatalf("seeded schema differs from a fresh migration:\n got %v\nwant %v", gotObjects, wantObjects)
	}
	if !reflect.DeepEqual(gotVersions, wantVersions) {
		t.Fatalf("seeded migration versions %v differ from fresh migration %v", gotVersions, wantVersions)
	}
}

func TestOpenFallsBackToStoreOpen(t *testing.T) {
	if _, err := storetest.Open(""); err == nil {
		t.Fatal("empty path must still be rejected")
	}
	missingDir := filepath.Join(t.TempDir(), "no-such-dir", "redline.db")
	if db, err := storetest.Open(missingDir); err == nil {
		db.Close()
		t.Fatal("a path in a missing directory must fail exactly as store.Open does")
	}
}

func TestOpenReusesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	first, err := storetest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := storetest.Open(path)
	if err != nil {
		t.Fatalf("reopening an existing database: %v", err)
	}
	second.Close()
}
