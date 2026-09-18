//go:build !windows

package store

import (
	"fmt"
	"os"
)

// restrictToOwner makes path readable and writable only by its owner.
//
// The file is created if absent so the permissions are in place before SQLite
// writes anything, and an existing file is tightened too, so upgrading from a
// version that created the database under the process umask (commonly 0644,
// i.e. world-readable) fixes it on the next start.
func restrictToOwner(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create database file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("create database file %q: %w", path, err)
	}
	// O_CREATE applies the mode only when creating, and it is masked by the
	// process umask even then, so set it explicitly.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict database file %q: %w", path, err)
	}
	return nil
}
