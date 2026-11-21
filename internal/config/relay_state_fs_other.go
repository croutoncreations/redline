//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package config

// Managed relay state relies on directory-relative, owner-checked, flock'd
// file operations that exist only on Unix. Elsewhere (Windows) every
// operation fails with errRelayStateUnsupported: with no relay configured
// the resolver treats that as "relay off" so the service still starts;
// enabling the relay there is an error.
var (
	relayStateBeforeRename = func() error { return nil }
	relayStateAfterRename  = func() error { return nil }
)

type relayStateOperation struct{}

func beginRelayStateOperation(string) (*relayStateOperation, error) {
	return nil, errRelayStateUnsupported
}

func (op *relayStateOperation) close() error { return nil }

func (op *relayStateOperation) read() ([]byte, bool, error) {
	return nil, false, errRelayStateUnsupported
}

func (op *relayStateOperation) write([]byte) error { return errRelayStateUnsupported }
