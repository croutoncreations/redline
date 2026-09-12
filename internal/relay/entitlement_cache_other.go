//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package relay

import "errors"

type entitlementCacheOperation struct{}

func beginEntitlementCacheOperation(string) (*entitlementCacheOperation, error) {
	return nil, errors.New("secure entitlement cache is unavailable on this platform")
}
func (*entitlementCacheOperation) close() {}
func (*entitlementCacheOperation) read() ([]byte, bool, error) {
	return nil, false, errors.New("unavailable")
}
func (*entitlementCacheOperation) write([]byte) error { return errors.New("unavailable") }
