//go:build !windows

package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

// The database holds task prompts, prepare/finalize shell commands, and
// credential references. Under the default 0022 umask it was created 0644 —
// world-readable — so any local account could read it off disk and bypass the
// HTTP bearer-token boundary entirely.
func TestOpenRestrictsDatabaseFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatalf("stat %s%s: %v", path, suffix, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s%s mode = %04o, want 0600", filepath.Base(path), suffix, perm)
		}
	}
}

// Upgrading from a version that created the database under the umask must
// tighten the existing file rather than leaving it world-readable forever.
func TestOpenTightensPreExistingWorldReadableDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a database written before this change.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %04o after reopening a 0644 database, want 0600", perm)
	}
}

// Tightening permissions must not cost the caller their data.
func TestOpenPreservesDataWhenTighteningPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	profile := domain.ExecutionProfile{
		ID: "profile-perm", ProviderAccountID: "codex-main",
		HarnessType: "codex-cli", WorkspaceProvider: "existing-directory",
	}
	if err := db.CreateProfile(
		t.Context(), profile, time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	profiles, err := db.ListProfiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].ID != "profile-perm" {
		t.Fatalf("profiles = %#v, want the row written before tightening", profiles)
	}
}
