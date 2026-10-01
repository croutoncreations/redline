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

func TestCodexCredentialsRefreshNearExpiryAndPreserveFile(t *testing.T) {
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	oldAccessToken := testJWT(now.Add(5 * time.Minute))
	original := []byte(fmt.Sprintf(`{
  "unknown":{"keep":true},
  "tokens":{
    "access_token":%q,
    "refresh_token":"old-refresh",
    "id_token":"old-id",
    "account_id":"account-1",
    "future_field":"preserve"
  },
  "last_refresh":"old-time"
}`, oldAccessToken))
	store := &memorySecretStore{value: original}

	type refreshRequest struct {
		method       string
		contentType  string
		grantType    string
		clientID     string
		refreshToken string
	}
	requests := make(chan refreshRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- refreshRequest{
			method: r.Method, contentType: r.Header.Get("Content-Type"),
			grantType: r.Form.Get("grant_type"), clientID: r.Form.Get("client_id"),
			refreshToken: r.Form.Get("refresh_token"),
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","id_token":"new-id"}`))
	}))
	defer server.Close()

	credentials := &DefaultCredentials{
		HTTPClient: server.Client(), Now: func() time.Time { return now },
		CodexStore: store, CodexRefreshURL: server.URL,
	}
	got, err := credentials.Access(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new-access" || got.AccountID != "account-1" {
		t.Fatalf("credential = %#v, want refreshed token and preserved account", got)
	}
	request := <-requests
	if request.method != http.MethodPost || request.contentType != "application/x-www-form-urlencoded" ||
		request.grantType != "refresh_token" || request.clientID != codexClientID ||
		request.refreshToken != "old-refresh" {
		t.Fatalf("refresh request = %#v", request)
	}
	if store.swaps != 1 {
		t.Fatalf("credential swaps = %d, want 1", store.swaps)
	}

	var stored map[string]any
	if err := json.Unmarshal(store.value, &stored); err != nil {
		t.Fatalf("stored credentials are invalid JSON: %v", err)
	}
	tokens, ok := stored["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("stored tokens = %#v", stored["tokens"])
	}
	if tokens["access_token"] != "new-access" || tokens["refresh_token"] != "new-refresh" ||
		tokens["id_token"] != "new-id" || tokens["account_id"] != "account-1" ||
		tokens["future_field"] != "preserve" {
		t.Fatalf("stored tokens = %#v", tokens)
	}
	if unknown, ok := stored["unknown"].(map[string]any); !ok || unknown["keep"] != true {
		t.Fatalf("unknown fields were not preserved: %#v", stored["unknown"])
	}
	if stored["last_refresh"] != now.Format(time.RFC3339Nano) {
		t.Fatalf("last_refresh = %#v, want %q", stored["last_refresh"], now.Format(time.RFC3339Nano))
	}
}

func TestCodexCredentialsDoNotRefreshFreshToken(t *testing.T) {
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	accessToken := testJWT(now.Add(5*time.Minute + time.Second))
	store := &memorySecretStore{value: []byte(fmt.Sprintf(
		`{"tokens":{"access_token":%q,"refresh_token":"refresh","account_id":"account-1"}}`,
		accessToken,
	))}
	credentials := &DefaultCredentials{
		Now: func() time.Time { return now }, CodexStore: store,
		CodexRefreshURL: "://refresh-must-not-be-called",
	}

	got, err := credentials.Access(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != accessToken || got.AccountID != "account-1" {
		t.Fatalf("credential = %#v", got)
	}
	if store.swaps != 0 {
		t.Fatalf("credential swaps = %d, want 0", store.swaps)
	}
}

func testJWT(expiresAt time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expiresAt.Unix())))
	return "header." + payload + ".signature"
}

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
