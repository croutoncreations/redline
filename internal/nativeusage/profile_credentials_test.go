package nativeusage

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
)

// profileServer answers the usage endpoint and records which token asked.
func profileServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":10,"resets_at":"2026-10-10T20:00:00Z"},"seven_day":{"utilization":30,"resets_at":"2026-10-14T20:00:00Z"}}`))
	}))
	t.Cleanup(server.Close)
	return server, &tokens
}

func TestProfileAccountReadsItsOwnLoginAndNeverTheDefault(t *testing.T) {
	now := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	dirA, dirB := t.TempDir(), t.TempDir()
	stores := map[string]*memorySecretStore{
		dirA: {value: []byte(`{"claudeAiOauth":{"accessToken":"token-a","refreshToken":"r","expiresAt":1791763200000}}`)},
		dirB: {value: []byte(`{"claudeAiOauth":{"accessToken":"token-b","refreshToken":"r","expiresAt":1791763200000}}`)},
	}
	defaultStore := &memorySecretStore{value: []byte(`{"claudeAiOauth":{"accessToken":"token-default","expiresAt":1791763200000}}`)}
	var opened []string
	credentials := &DefaultCredentials{Now: func() time.Time { return now }, ClaudeStore: defaultStore,
		ClaudeProfileStore: func(dir string) secretStore { opened = append(opened, dir); return stores[dir] }}
	server, tokens := profileServer(t)
	client := Client{Credentials: credentials, ClaudeUsageURL: server.URL, Now: func() time.Time { return now }}

	for _, dir := range []string{dirA, dirB} {
		if _, _, err := client.Fetch(context.Background(), config.Provider{Provider: "claude", ClaudeConfigDir: dir}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.BankedResets(context.Background(), config.Provider{Provider: "claude", ClaudeConfigDir: dirB}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*tokens, ",") != "token-a,token-b,token-b" {
		t.Fatalf("tokens used = %v; each profile must use only its own login", *tokens)
	}
	if strings.Join(opened, ",") != dirA+","+dirB+","+dirB {
		t.Fatalf("stores opened = %v", opened)
	}
}

func TestProfileLoginIsNeverRefreshedOrWritten(t *testing.T) {
	now := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	// Expires in one minute, with a refresh token: the default login would
	// be refreshed here; a profile must not be.
	original := []byte(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"refresh","expiresAt":1791655260000}}`)
	store := &memorySecretStore{value: append([]byte(nil), original...)}
	refreshCalls := 0
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshCalls++
		_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"next","expires_in":3600}`))
	}))
	defer refresh.Close()
	credentials := &DefaultCredentials{HTTPClient: refresh.Client(), Now: func() time.Time { return now }, ClaudeRefreshURL: refresh.URL,
		ClaudeProfileStore: func(string) secretStore { return store }}
	_, err := credentials.AccessClaudeProfile(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "will not modify that profile's credential") || failureReasonOf(err) != "needs_session" {
		t.Fatalf("err = %v", err)
	}
	if refreshCalls != 0 || store.swaps != 0 || !bytes.Equal(store.value, original) {
		t.Fatalf("refresh calls=%d swaps=%d: the profile's credential was touched", refreshCalls, store.swaps)
	}
	if errors.Is(err, ErrSignedOut) {
		t.Fatal("a profile with a refresh token is not signed out; Claude Code refreshes it on next use")
	}
}

func TestMissingProfileIsSignedOutWithItsOwnSignInCommand(t *testing.T) {
	credentials := &DefaultCredentials{ClaudeProfileStore: func(string) secretStore {
		return &missingStore{}
	}}
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")
	presentDir := t.TempDir()
	for _, dir := range []string{missingDir, presentDir} {
		_, err := credentials.AccessClaudeProfile(context.Background(), dir)
		if !errors.Is(err, ErrSignedOut) || failureReasonOf(err) != "signed_out" ||
			!strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR=") || !strings.Contains(err.Error(), "claude auth login") {
			t.Fatalf("%s: err = %v", dir, err)
		}
	}
	if _, err := credentials.AccessClaudeProfile(context.Background(), "relative"); err == nil {
		t.Fatal("a relative profile directory must be refused")
	}
}

func TestProfileStoresCannotWrite(t *testing.T) {
	dir := t.TempDir()
	if _, writable := newClaudeProfileStore(dir).(writableSecretStore); writable {
		t.Fatal("a Claude Code profile's credential store must be read-only")
	}
}

func TestInspectProfileReadsNoPersonalDetails(t *testing.T) {
	dir := t.TempDir()
	metadata := `{"oauthAccount":{"accountUuid":"11111111-2222-3333-4444-555555555555","emailAddress":"person@example.com",
	"displayName":"Person","fullName":"Full Person","organizationName":"Org Name","organizationType":"claude_team",
	"organizationRole":"user","userRateLimitTier":"default_claude_max_5x"},"userID":"abc"}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := InspectClaudeProfile(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Plan != "Team · Max 5x" || profile.Role != "member" || len(profile.Fingerprint) != 12 {
		t.Fatalf("profile = %#v", profile)
	}
	text := strings.ToLower(profile.Dir + profile.Fingerprint + profile.Plan + profile.Role)
	for _, leak := range []string{"person", "example.com", "org name", "11111111"} {
		if strings.Contains(strings.ReplaceAll(text, strings.ToLower(dir), ""), leak) {
			t.Fatalf("profile carries %q: %#v", leak, profile)
		}
	}
	same := t.TempDir()
	_ = os.WriteFile(filepath.Join(same, ".claude.json"), []byte(metadata), 0o600)
	again, _ := InspectClaudeProfile(context.Background(), same)
	if again.Fingerprint != profile.Fingerprint {
		t.Fatal("two directories signed in to one account must share a fingerprint")
	}
	if _, err := InspectClaudeProfile(context.Background(), t.TempDir()); err == nil {
		t.Fatal("a directory without .claude.json is not a profile")
	}
}

type missingStore struct{}

func (missingStore) Read(context.Context) ([]byte, error) { return nil, errNoCredential }

func failureReasonOf(err error) string {
	var reasoned interface{ Reason() string }
	if errors.As(err, &reasoned) {
		return reasoned.Reason()
	}
	return ""
}

type failingStore struct{ err error }

func (s failingStore) Read(context.Context) ([]byte, error) { return nil, s.err }

func TestProfileReadErrorsCarryNoFullPath(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, ".credentials.json")
	credentials := &DefaultCredentials{ClaudeProfileStore: func(string) secretStore {
		return failingStore{err: &os.PathError{Op: "open", Path: full, Err: os.ErrPermission}}
	}}
	_, err := credentials.AccessClaudeProfile(context.Background(), dir)
	if err == nil || strings.Contains(err.Error(), full) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadOnlyStoreHidesWrites(t *testing.T) {
	var store secretStore = readOnlyStore{&memorySecretStore{}}
	if _, writable := store.(writableSecretStore); writable {
		t.Fatal("readOnlyStore must not expose CompareAndSwap")
	}
}
