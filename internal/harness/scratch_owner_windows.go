//go:build windows

package harness

import "os"

// ownedByCurrentUser is always true on Windows, where os.TempDir() is the
// user's own profile directory rather than a shared one.
func ownedByCurrentUser(os.FileInfo) bool { return true }
