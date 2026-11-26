//go:build windows

package apicredits

import (
	"fmt"
	"os"
)

// openNoFollow refuses a symlink (reparse point) and otherwise opens path
// read-only. Ownership is enforced by the file's DACL on Windows.
func openNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("symlink")
	}
	return os.Open(path)
}

func ownedByCurrentUser(os.FileInfo) bool { return true }
