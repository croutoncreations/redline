//go:build !windows

package apiauth

// protectTokenFile is a no-op outside Windows: the token is created with
// mode 0600, which POSIX filesystems honor directly.
func protectTokenFile(path string) error {
	return nil
}
