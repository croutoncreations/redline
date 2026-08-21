//go:build darwin

package nativeusage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMacKeychainStoreCannotWriteSharedClaudeCredential(t *testing.T) {
	store := macKeychainStore{Service: "Claude Code-credentials", Account: "redline-test"}
	if _, writable := any(store).(writableSecretStore); writable {
		t.Fatal("macOS Claude credential store must remain read-only")
	}
}

func TestClaudeRefreshFailsBeforeRequestWhenCredentialStoreIsReadOnly(t *testing.T) {
	now := time.Date(2026, 7, 31, 22, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	store := readOnlySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"refresh","expiresAt":1785535200000}}`)}
	credentials := &DefaultCredentials{
		HTTPClient: server.Client(), Now: func() time.Time { return now },
		ClaudeStore: store, ClaudeRefreshURL: server.URL,
	}

	_, err := credentials.Access(context.Background(), "claude")
	if err == nil || !strings.Contains(err.Error(), "will not modify Claude Code's shared credential") {
		t.Fatalf("error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("refresh requests = %d, want 0", requests)
	}
}

type readOnlySecretStore struct{ value []byte }

func (s readOnlySecretStore) Read(context.Context) ([]byte, error) {
	return append([]byte(nil), s.value...), nil
}
