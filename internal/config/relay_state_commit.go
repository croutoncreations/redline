package config

import "errors"

// errRelayStateUnsupported is returned by every managed relay state operation
// on platforms without the Unix file primitives it relies on.
var errRelayStateUnsupported = errors.New("managed relay state is not supported on this platform")

// RelayStateCommitError means rename published the new state, but a later
// durability or close operation failed. Callers must not assume the old value
// remains in place and should reload before retrying a mutation.
type RelayStateCommitError struct{ Err error }

func (e *RelayStateCommitError) Error() string {
	return "relay state commit is uncertain: " + e.Err.Error()
}
func (e *RelayStateCommitError) Unwrap() error { return e.Err }
