package relay

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Config holds the optional relay configuration for the desktop service.
//
// The relay is opt-in and disabled by default. A desktop that never enables it
// is in exactly the position it was before the relay existed: the field is a
// zero value and Validate passes unconditionally.
type Config struct {
	// Enabled controls whether the desktop dials the relay at startup. The
	// flag is separate from the presence of a URL so a user can temporarily
	// disable the relay without losing the pairing URL they have distributed.
	Enabled bool `yaml:"enabled"`

	// URL is the HTTPS address of the relay Worker. It must be a named host
	// with a valid TLS certificate; the relay is on the public internet and
	// there is no loopback exception to make here.
	URL string `yaml:"url"`

	// SessionID is a random opaque value that pairs a desktop with a phone on
	// the relay. It is minted once by the desktop, distributed via QR, and
	// persisted across restarts so a phone does not have to rescan when the
	// desktop reboots.
	SessionID string `yaml:"session_id,omitempty"`

	// KeypairPath is where the desktop stores its Noise static keypair. An
	// empty value means "use the default location next to the config file",
	// which covers users who have not touched this setting.
	KeypairPath string `yaml:"keypair_path,omitempty"`
}

// Validate returns an error if the config is self-contradictory or unsafe.
//
// A disabled config is always valid; nothing in it is used. An enabled config
// must have a URL that is safe to dial from the desktop: HTTPS, a named host,
// no userinfo, no literal IP.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.URL == "" {
		return fmt.Errorf("relay.url is required when relay is enabled")
	}
	if err := validateRelayURL(c.URL); err != nil {
		return fmt.Errorf("relay.url: %w", err)
	}
	return nil
}

// validateRelayURL applies the same rules as safeRelayURL in mobile/core/pairing.go,
// but returns a descriptive error rather than a safe-or-empty string. The two
// must stay in sync: a URL the desktop publishes in a QR code must also be one
// the phone accepts, and vice versa.
func validateRelayURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("must not be empty")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}

	// HTTPS only. The relay is on the public internet by definition, so there
	// is no loopback exception and no ws:// shorthand.
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("scheme must be https, got %q", parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}

	// A literal IP as a relay host is never something we publish, and it is
	// how a hostile QR code would aim the desktop at an internal address.
	if net.ParseIP(host) != nil {
		return fmt.Errorf("host must be a named host, not a literal IP address")
	}

	// Userinfo in a relay URL is either a credential mistake or an injection
	// attempt; neither is welcome.
	if parsed.User != nil {
		return fmt.Errorf("URL must not contain credentials")
	}

	return nil
}

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
