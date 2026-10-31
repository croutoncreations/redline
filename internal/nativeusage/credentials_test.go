package nativeusage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// Plan identifiers never gate access: Redline reads limits from usage windows,
// so renamed (Ultra, Max), legacy (max_20x, max_5x), unknown, or retyped plan
// fields must all yield the token and survive a refresh untouched.
func TestClaudeCredentialsIgnorePlanIdentifiers(t *testing.T) {
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	for _, plan := range []string{
		`"subscriptionType":"ultra","rateLimitTier":"default_claude_ultra"`,
		`"subscriptionType":"max","rateLimitTier":"default_claude_max_20x"`,
		`"subscriptionType":"max","rateLimitTier":"default_claude_max_5x"`,
		`"subscriptionType":"max","rateLimitTier":"default_claude_max"`,
		`"subscriptionType":"pro","rateLimitTier":"default_claude_pro"`,
		`"subscriptionType":"galaxy_brain","rateLimitTier":"tier_from_the_future"`,
		`"subscriptionType":null,"rateLimitTier":null`,
		`"subscriptionType":{"name":"Ultra"},"rateLimitTier":20`,
	} {
		t.Run(plan, func(t *testing.T) {
			live := &memorySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"live","expiresAt":1793469600000,` + plan + `}}`)}
			credentials := &DefaultCredentials{Now: func() time.Time { return now }, ClaudeStore: live}
			if got, err := credentials.AccessWithoutRefresh(context.Background(), "claude"); err != nil || got.AccessToken != "live" {
				t.Fatalf("read-only credential=%#v err=%v", got, err)
			}

			expiring := &memorySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"refresh","expiresAt":1793383260000,` + plan + `}}`)}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"next","expires_in":3600}`))
			}))
			defer server.Close()
			credentials = &DefaultCredentials{HTTPClient: server.Client(), Now: func() time.Time { return now }, ClaudeStore: expiring, ClaudeRefreshURL: server.URL}
			if got, err := credentials.Access(context.Background(), "claude"); err != nil || got.AccessToken != "new" {
				t.Fatalf("refreshed credential=%#v err=%v", got, err)
			}
			if !bytes.Contains(expiring.value, []byte(`"refreshToken":"next"`)) {
				t.Fatalf("refresh not persisted: %s", expiring.value)
			}
			var stored, original map[string]map[string]any
			if err := json.Unmarshal(expiring.value, &stored); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(`{"claudeAiOauth":{`+plan+`}}`), &original); err != nil {
				t.Fatal(err)
			}
			for key, want := range original["claudeAiOauth"] {
				if got := fmt.Sprint(stored["claudeAiOauth"][key]); got != fmt.Sprint(want) {
					t.Fatalf("%s = %s, want %v preserved", key, got, want)
				}
			}
		})
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

func TestAccessWithoutRefreshNeverRotatesSharedCodexToken(t *testing.T) {
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	for _, remaining := range []time.Duration{-time.Minute, 5 * time.Minute, time.Hour} {
		t.Run(remaining.String(), func(t *testing.T) {
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, now.Add(remaining).Unix()))) + ".signature"
			original := []byte(fmt.Sprintf(`{"tokens":{"access_token":%q,"refresh_token":"refresh","account_id":"account"}}`, token))
			store := &memorySecretStore{value: append([]byte(nil), original...)}
			refreshCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				refreshCalls++
				_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"next"}`))
			}))
			defer server.Close()
			credentials := &DefaultCredentials{HTTPClient: server.Client(), Now: func() time.Time { return now }, CodexStore: store, CodexRefreshURL: server.URL}
			got, err := credentials.AccessWithoutRefresh(context.Background(), "codex")
			if refreshCalls != 0 || store.swaps != 0 || !bytes.Equal(store.value, original) {
				t.Fatalf("refresh calls=%d swaps=%d: the shared credential was touched", refreshCalls, store.swaps)
			}
			if remaining <= 5*time.Minute {
				if err == nil || got.AccessToken != "" {
					t.Fatalf("near-expiry token should be unavailable: credential=%#v err=%v", got, err)
				}
			} else if err != nil || got.AccessToken != token || got.AccountID != "account" {
				t.Fatalf("credential=%#v err=%v", got, err)
			}
		})
	}
}

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
