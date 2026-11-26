//go:build !windows

package apicredits

import (
	"os"
	"syscall"
)

// openNoFollow opens path read-only, refusing a final symlink. O_NONBLOCK
// keeps a FIFO with no writer from blocking the open, so the caller's
// regular-file check can refuse it; it has no effect on regular files.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
