//go:build !windows

package harness

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether this user owns the file, so the
// startup sweep never touches another user's directory in a shared /tmp.
func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
