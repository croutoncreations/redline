package nativeusage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeCredentialsRefreshAndPersistWithCompareAndSwap(t *testing.T) {
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	store := &memorySecretStore{value: []byte(`{"unknown":{"keep":true},"claudeAiOauth":{"accessToken":"old","refreshToken":"refresh","expiresAt":1784743200000,"subscriptionType":"max","scopes":["user:profile"],"futureField":"preserve"}}`)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content type=%q", r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"next","expires_in":3600}`))
	}))
	defer server.Close()
	credentials := &DefaultCredentials{HTTPClient: server.Client(), Now: func() time.Time { return now }, ClaudeStore: store, ClaudeRefreshURL: server.URL}
	got, err := credentials.Access(context.Background(), "claude")
	if err != nil || got.AccessToken != "new" || store.swaps != 1 || !bytes.Contains(store.value, []byte(`"refreshToken":"next"`)) ||
		!bytes.Contains(store.value, []byte(`"futureField":"preserve"`)) || !bytes.Contains(store.value, []byte(`"keep":true`)) {
		t.Fatalf("credential=%#v err=%v swaps=%d stored=%s", got, err, store.swaps, store.value)
	}
}

func TestAccessWithoutRefreshNeverRotatesSharedClaudeToken(t *testing.T) {
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	original := []byte(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"refresh","expiresAt":1784743200000}}`)
	store := &memorySecretStore{value: append([]byte(nil), original...)}
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshCalls++
		_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"next","expires_in":3600}`))
	}))
	defer server.Close()
	credentials := &DefaultCredentials{HTTPClient: server.Client(), Now: func() time.Time { return now }, ClaudeStore: store, ClaudeRefreshURL: server.URL}
	if _, err := credentials.AccessWithoutRefresh(context.Background(), "claude"); err == nil {
		t.Fatal("a near-expiry token should be reported unavailable, not refreshed")
	}
	if refreshCalls != 0 || store.swaps != 0 || !bytes.Equal(store.value, original) {
		t.Fatalf("refresh calls=%d swaps=%d: the shared credential was touched", refreshCalls, store.swaps)
	}
	fresh := &memorySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"live","expiresAt":1784757600000}}`)}
	credentials.ClaudeStore = fresh
	got, err := credentials.AccessWithoutRefresh(context.Background(), "claude")
	if err != nil || got.AccessToken != "live" {
		t.Fatalf("credential=%#v err=%v", got, err)
	}
}

// When Claude Code is signed out, the error is what a person sees on every
// Redline surface, so it must say what happened and the one command that
// fixes it -- not "credentials are invalid", which reads like Redline's bug.
func TestSignedOutClaudeSaysSoAndHowToSignIn(t *testing.T) {
	cases := map[string]secretStore{
		// What a sign-out actually leaves: the keychain item stays, emptied.
		"emptied credential": &memorySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,"subscriptionType":"max"}}`)},
		// What the keychain and file stores report for an item that is not there.
		"missing keychain item": failingSecretStore{err: fmt.Errorf("keychain item %q: %w", "Claude Code-credentials", errNoCredential)},
		"missing file":          failingSecretStore{err: fmt.Errorf("open: %w", os.ErrNotExist)},
		"expired, no refresh":   &memorySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"","expiresAt":1}}`)},
	}
	for name, store := range cases {
		t.Run(name, func(t *testing.T) {
			credentials := &DefaultCredentials{Now: func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }, ClaudeStore: store}
			for access, call := range map[string]func() (Credential, error){
				"Access":               func() (Credential, error) { return credentials.Access(context.Background(), "claude") },
				"AccessWithoutRefresh": func() (Credential, error) { return credentials.AccessWithoutRefresh(context.Background(), "claude") },
			} {
				_, err := call()
				if err == nil || !errors.Is(err, ErrSignedOut) {
					t.Fatalf("%s: err = %v, want ErrSignedOut", access, err)
				}
				for _, want := range []string{"Claude Code is signed out", "claude auth login"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("%s: %q does not say %q", access, err.Error(), want)
					}
				}
			}
		})
	}
}

// A keychain that exists but cannot be read -- locked, or access denied -- is
// not a sign-out, and logging in again would not fix it.
func TestUnreadableKeychainIsNotReportedSignedOut(t *testing.T) {
	credentials := &DefaultCredentials{ClaudeStore: failingSecretStore{err: errors.New("read keychain item: exit status 51")}}
	_, err := credentials.Access(context.Background(), "claude")
	if err == nil || errors.Is(err, ErrSignedOut) {
		t.Fatalf("err = %v, want a non-signed-out error", err)
	}
}

// A near-expiry token that still has a refresh token is not signed out:
// AccessWithoutRefresh declines to rotate it, but the next Claude Code
// session will. Calling that "signed out" would send people to log in for
// nothing.
func TestNearExpiryWithRefreshTokenIsNotReportedSignedOut(t *testing.T) {
	store := &memorySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"refresh","expiresAt":1}}`)}
	credentials := &DefaultCredentials{Now: func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }, ClaudeStore: store}
	_, err := credentials.AccessWithoutRefresh(context.Background(), "claude")
	if err == nil || errors.Is(err, ErrSignedOut) {
		t.Fatalf("err = %v, want a non-signed-out error", err)
	}
}

type failingSecretStore struct{ err error }

func (s failingSecretStore) Read(context.Context) ([]byte, error) { return nil, s.err }

func TestCredentialRefreshRejectsConcurrentReplacement(t *testing.T) {
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	store := &memorySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"refresh","expiresAt":1784743200000}}`), changeBeforeSwap: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new","expires_in":3600}`))
	}))
	defer server.Close()
	credentials := &DefaultCredentials{HTTPClient: server.Client(), Now: func() time.Time { return now }, ClaudeStore: store, ClaudeRefreshURL: server.URL}
	if _, err := credentials.Access(context.Background(), "claude"); err == nil {
		t.Fatal("expected concurrent credential replacement error")
	}
}

func TestFirstFileStoreCompareAndSwap(t *testing.T) {
	t.Run("replaces matching credential and preserves permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.json")
		original := []byte(`{"tokens":{"access_token":"old"}}`)
		updated := []byte(`{"tokens":{"access_token":"new"}}`)
		if err := os.WriteFile(path, original, 0o640); err != nil {
			t.Fatal(err)
		}
		store := firstFileStore{Paths: []string{path}}

		if err := store.CompareAndSwap(t.Context(), original, updated); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, updated) {
			t.Fatalf("credential = %s, want %s", got, updated)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o640); got != want {
			t.Fatalf("credential permissions = %v, want %v", got, want)
		}
	})

	t.Run("rejects concurrent replacement without overwriting it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.json")
		original := []byte(`{"tokens":{"access_token":"old"}}`)
		replacement := []byte(`{"tokens":{"access_token":"other-process"}}`)
		updated := []byte(`{"tokens":{"access_token":"redline-refresh"}}`)
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		store := firstFileStore{Paths: []string{path}}
		observed, err := store.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, replacement, 0o600); err != nil {
			t.Fatal(err)
		}

		err = store.CompareAndSwap(t.Context(), observed, updated)
		if !errors.Is(err, errCredentialsChanged) {
			t.Fatalf("CompareAndSwap error = %v, want %v", err, errCredentialsChanged)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, replacement) {
			t.Fatalf("credential = %s, want concurrent replacement %s", got, replacement)
		}
	})
}

type memorySecretStore struct {
	value            []byte
	swaps            int
	changeBeforeSwap bool
}

func (s *memorySecretStore) Read(context.Context) ([]byte, error) {
	return append([]byte(nil), s.value...), nil
}
func (s *memorySecretStore) CompareAndSwap(_ context.Context, old, updated []byte) error {
	if s.changeBeforeSwap {
		s.value = []byte(`{"replacement":true}`)
	}
	if !bytes.Equal(s.value, old) {
		return errCredentialsChanged
	}
	s.value = append([]byte(nil), updated...)
	s.swaps++
	return nil
}
