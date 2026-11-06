// Package storetest opens fresh SQLite stores for tests without paying for a
// full schema migration each time.
//
// store.Open on a new file replays every migration. That is cheap normally but
// roughly 30x slower under the race detector (the pure-Go SQLite is
// instrumented), and the Go suite opens a fresh store in well over a hundred
// tests. Open here migrates once per test binary through the real store.Open,
// keeps the resulting file image, and seeds each later fresh path with a copy
// before handing it to store.Open, which then takes its normal path for an
// already-migrated database.
//
// Tests that exercise Open itself on an empty path (migration, file
// permissions) should call store.Open directly.
package storetest

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/croutoncreations/redline/internal/store"
)

var (
	templateOnce  sync.Once
	templateImage []byte
)

// migratedImage returns the bytes of a database freshly migrated by the real
// store.Open, or nil if that could not be produced.
func migratedImage() []byte {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "redline-storetest-")
		if err != nil {
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "template.db")
		db, err := store.Open(path)
		if err != nil {
			return
		}
		// Closing the last connection checkpoints the WAL into the main file,
		// so the image alone is a complete database.
		if err := db.Close(); err != nil {
			return
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if info, err := os.Stat(path + suffix); err == nil && info.Size() > 0 {
				return
			}
		}
		image, err := os.ReadFile(path)
		if err != nil {
			return
		}
		templateImage = image
	})
	return templateImage
}

// Open behaves like store.Open. When path does not exist yet it first seeds
// the file with an already-migrated schema; every other case, including any
// failure to seed, falls through to store.Open unchanged.
func Open(path string) (*store.DB, error) {
	if path != "" {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			if image := migratedImage(); image != nil {
				// O_EXCL so a concurrent creator is never overwritten; any
				// error just means store.Open does the full migration.
				if file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
					_, writeErr := file.Write(image)
					closeErr := file.Close()
					if writeErr != nil || closeErr != nil {
						_ = os.Remove(path)
					}
				}
			}
		}
	}
	return store.Open(path)
}
