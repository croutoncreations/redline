package relay

import "testing"

// The relay is opt-in. A user who never enables it must be in exactly the
// position they were before this feature existed.
func TestDisabledByDefault(t *testing.T) {
	var cfg Config
	if cfg.Enabled {
		t.Fatal("remote access must be off unless explicitly enabled")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an empty, disabled config is valid: %v", err)
	}
}

func TestEnabledRequiresARelayURL(t *testing.T) {
	cfg := Config{Enabled: true}
	if err := cfg.Validate(); err == nil {
		t.Fatal("enabling the relay without a URL must be rejected")
	}
}

// The relay lives on the public internet, so its URL has none of the loopback
// exceptions the local API has.
func TestRelayURLMustBeHTTPS(t *testing.T) {
	for _, bad := range []string{
		"http://relay.example.com",
		"ws://relay.example.com",
		"relay.example.com",
		"https://",
		"https://192.0.2.1",
	} {
		cfg := Config{Enabled: true, URL: bad}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted a bad relay URL %q", bad)
		}
	}

	cfg := Config{Enabled: true, URL: "https://redline-relay.example.com"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rejected a good relay URL: %v", err)
	}
}

// A session id that is short or predictable would let someone else's phone
// find this desktop's session on a shared relay.
func TestGeneratedSessionIDsAreOpaqueAndUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("new session id: %v", err)
		}
		if len(id) < 16 {
			t.Fatalf("session id %q is too short to be unguessable", id)
		}
		// Must satisfy the relay's own pattern, or the desktop cannot connect.
		for _, r := range id {
			isAllowed := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_'
			if !isAllowed {
				t.Fatalf("session id %q contains %q, which the relay rejects", id, r)
			}
		}
		if seen[id] {
			t.Fatalf("session id %q was generated twice", id)
		}
		seen[id] = true
	}
}
