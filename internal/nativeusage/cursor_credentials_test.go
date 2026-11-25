package nativeusage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeCursorState map[string]string

func (f fakeCursorState) Value(_ context.Context, key string) (string, error) { return f[key], nil }

func fakeJWT(subject string, expires time.Time) string {
	payload := `{"sub":"` + subject + `","exp":` + strconv.FormatInt(expires.Unix(), 10) + `}`
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".fake"
}

func TestCursorCredentialsPrefersStateDatabaseThenKeychain(t *testing.T) {
	now := time.Date(2026, 10, 9, 6, 0, 0, 0, time.UTC)
	stateToken := fakeJWT("auth0|state", now.Add(time.Hour))
	keychainToken := fakeJWT("auth0|keychain", now.Add(time.Hour))
	keychain := &memorySecretStore{value: []byte(keychainToken + "\n")}
	credentials := &CursorCredentials{StateDB: fakeCursorState{cursorAccessTokenKey: stateToken}, Keychain: keychain, Now: func() time.Time { return now }}
	got, err := credentials.AccessWithoutRefresh(context.Background(), "cursor")
	if err != nil || got.AccessToken != stateToken {
		t.Fatalf("got=%v err=%v", got.AccessToken == stateToken, err)
	}
	credentials.StateDB = fakeCursorState{}
	got, err = credentials.AccessWithoutRefresh(context.Background(), "cursor")
	if err != nil || got.AccessToken != keychainToken {
		t.Fatalf("keychain fallback failed: %v", err)
	}
	// A leftover free session for another user yields to the keychain.
	credentials.StateDB = fakeCursorState{cursorAccessTokenKey: stateToken, cursorMembershipKey: "free"}
	got, _ = credentials.AccessWithoutRefresh(context.Background(), "cursor")
	if got.AccessToken != keychainToken {
		t.Fatal("free state session for another subject should defer to the keychain")
	}
	if keychain.swaps != 0 {
		t.Fatal("cursor credentials must never be written")
	}
}

// Redline never refreshes Cursor's session: it has no refresh path at all,
// so a nearly expired token is reported, not rotated.
func TestCursorCredentialsNeverRefreshes(t *testing.T) {
	now := time.Date(2026, 10, 9, 6, 0, 0, 0, time.UTC)
	expiring := fakeJWT("auth0|user", now.Add(2*time.Minute))
	keychain := &memorySecretStore{value: []byte(expiring)}
	credentials := &CursorCredentials{Keychain: keychain, Now: func() time.Time { return now }}
	_, err := credentials.AccessWithoutRefresh(context.Background(), "cursor")
	if err == nil || strings.Contains(err.Error(), expiring) || !strings.Contains(err.Error(), "open Cursor") {
		t.Fatalf("err = %v", err)
	}
	if keychain.swaps != 0 || !bytes.Equal(keychain.value, []byte(expiring)) {
		t.Fatal("credential store was modified")
	}
	var _ ReadOnlyCredentials = credentials
	if _, isWritable := any(credentials).(Credentials); isWritable {
		t.Fatal("CursorCredentials must not satisfy the refreshing Credentials interface")
	}
}

func TestCursorCredentialsSignedOutAndWrongProvider(t *testing.T) {
	credentials := &CursorCredentials{StateDB: fakeCursorState{}, Keychain: &memorySecretStore{}}
	if _, err := credentials.AccessWithoutRefresh(context.Background(), "cursor"); !errors.Is(err, ErrCursorSignedOut) {
		t.Fatalf("err = %v", err)
	}
	if _, err := credentials.AccessWithoutRefresh(context.Background(), "claude"); err == nil {
		t.Fatal("cursor credentials must not serve other providers")
	}
}

// The default Claude/Codex credential path must stay unaware of Cursor.
func TestDefaultCredentialsDoNotServeCursor(t *testing.T) {
	if _, err := (&DefaultCredentials{}).Access(context.Background(), "cursor"); err == nil {
		t.Fatal("DefaultCredentials must not hand out Cursor access")
	}
}

func TestCursorStateDBReadsWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);
		INSERT INTO ItemTable VALUES ('cursorAuth/accessToken', ' fake-token '), ('other', 'x');`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	before, _ := os.ReadFile(path)
	store := CursorStateDB{Path: path}
	value, err := store.Value(context.Background(), cursorAccessTokenKey)
	if err != nil || value != "fake-token" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if missing, err := store.Value(context.Background(), "absent"); err != nil || missing != "" {
		t.Fatalf("missing=%q err=%v", missing, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("state database changed")
	}
	if value, err := (CursorStateDB{Path: filepath.Join(t.TempDir(), "absent.vscdb")}).Value(context.Background(), "k"); err != nil || value != "" {
		t.Fatalf("absent database: value=%q err=%v", value, err)
	}
}
