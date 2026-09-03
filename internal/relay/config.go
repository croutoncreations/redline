package relay

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// Config holds the optional relay configuration for the desktop service.
//
// The relay is opt-in and disabled by default. A desktop that never enables it
// is in exactly the position it was before the relay existed: the field is a
// zero value and Validate passes unconditionally.
// NewSessionID returns a cryptographically random session identifier that
// satisfies the relay's own pattern ^[A-Za-z0-9_-]{16,128}$.
//
// 32 random bytes encoded with RawURLEncoding produces 43 characters, all in
// the base64url alphabet (A-Z, a-z, 0-9, -, _), which is exactly what the
// relay accepts. A session id that is short or predictable would let someone
// else's phone find this desktop's session on a shared relay.
func NewSessionID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
