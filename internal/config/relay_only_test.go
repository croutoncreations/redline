package config_test

import (
	"strings"
	"testing"

	"github.com/jfox/redline/internal/config"
)

// With remote access on and no tailnet, a user running relay-only has no
// reason to list api.trusted_hosts: the phone never dials the tailnet host at
// all. Requiring a trusted host here would make the relay-only setup
// impossible, because there is no host to list.
//
// The .ts.net check on individual entries still fires when entries are
// present; it is only the zero-entries case that changes.
func TestRelayEnabledWithNoTrustedHostsIsValid(t *testing.T) {
	relayOnly := strings.Replace(validConfig, "active_policy: standard", `active_policy: standard
relay:
  enabled: true
  url: https://redline-relay.example.com`, 1)
	if _, err := config.Load(writeConfig(t, relayOnly)); err != nil {
		t.Fatalf("relay-only config (no trusted_hosts) must be valid: %v", err)
	}
}
