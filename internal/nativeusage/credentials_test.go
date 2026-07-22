package nativeusage

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
