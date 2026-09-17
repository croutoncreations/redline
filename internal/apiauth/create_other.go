//go:build !windows

package apiauth

import "os"

// createPrivateFile creates path exclusively with mode 0600. POSIX
// filesystems apply the mode atomically at creation, so the file is never
// observable with broader permissions.
func createPrivateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}
