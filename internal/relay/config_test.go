package relay

import "testing"

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
