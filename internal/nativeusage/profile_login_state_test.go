package nativeusage

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// The keep-alive reads a profile's expiry and nothing else, and never
// writes: the credential is byte-for-byte unchanged afterwards.
func TestProfileLoginStateReadsOnlyTheExpiry(t *testing.T) {
	dir := t.TempDir()
	original := []byte(`{"claudeAiOauth":{"accessToken":"secret","refreshToken":"refresh","expiresAt":1794458656000,"scopes":["user:inference"]}}`)
	store := &memorySecretStore{value: append([]byte(nil), original...)}
	credentials := &DefaultCredentials{ClaudeProfileStore: func(string) secretStore { return store }}
	state, err := credentials.ProfileLoginState(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.SignedOut || !state.HasRefreshToken || !state.ExpiresAt.Equal(time.UnixMilli(1794458656000).UTC()) {
		t.Fatalf("state = %+v", state)
	}
	if bytes.Contains([]byte(fmt.Sprintf("%+v", state)), []byte("secret")) {
		t.Fatal("state must not carry a token")
	}
	if store.swaps != 0 || !bytes.Equal(store.value, original) {
		t.Fatal("reading the login state touched the credential")
	}
}

func TestProfileLoginStateSignedOut(t *testing.T) {
	missing := &DefaultCredentials{ClaudeProfileStore: func(string) secretStore { return &missingStore{} }}
	for _, dir := range []string{filepath.Join(t.TempDir(), "gone"), t.TempDir()} {
		if state, err := missing.ProfileLoginState(context.Background(), dir); err != nil || !state.SignedOut {
			t.Fatalf("%s: state = %+v, err = %v", dir, state, err)
		}
	}
	noRefresh := &DefaultCredentials{ClaudeProfileStore: func(string) secretStore {
		return &memorySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"a","expiresAt":1794458656000}}`)}
	}}
	if state, err := noRefresh.ProfileLoginState(context.Background(), t.TempDir()); err != nil || state.SignedOut || state.HasRefreshToken {
		t.Fatalf("state = %+v, err = %v", state, err)
	}
	if _, err := noRefresh.ProfileLoginState(context.Background(), "relative"); err == nil {
		t.Fatal("a relative directory must be refused")
	}
}

// The collector's expired-token error is recognisable through the usage
// manager's wrapping, so it can trigger a renewal.
func TestNeedsSessionIsRecognisedThroughWrapping(t *testing.T) {
	err := fmt.Errorf("native usage source: %w", profileNeedsSessionError{dir: "~/.claude-cc-a"})
	if !IsProfileNeedsSession(err) {
		t.Fatal("wrapped needs_session not recognised")
	}
	if IsProfileNeedsSession(profileSignedOutError{dir: "~/.claude-cc-a"}) || IsProfileNeedsSession(nil) {
		t.Fatal("only needs_session counts")
	}
}
