package nativeusage

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // read-only access to Cursor's local state database
)

// Cursor keeps its desktop session in a VS Code style state database and, on
// macOS, mirrors it into the login keychain. These are the same locations
// OpenUsage's Cursor provider reads.
const (
	cursorAccessTokenKey      = "cursorAuth/accessToken"
	cursorMembershipKey       = "cursorAuth/stripeMembershipType"
	cursorKeychainAccessToken = "cursor-access-token"
)

// ErrCursorSignedOut means no Cursor session was found on this machine.
var ErrCursorSignedOut = errors.New("cursor is not signed in on this Mac; open Cursor and sign in")

// CursorCredentials reads Cursor's current access token without ever
// refreshing or rewriting it. Cursor owns its session: a token near expiry is
// reported as unavailable, and the next time Cursor runs it refreshes itself.
// It satisfies ReadOnlyCredentials and deliberately does not satisfy
// Credentials, so it can never be handed to a path that refreshes.
type CursorCredentials struct {
	// StateDB holds Cursor's state database values; Keychain the macOS
	// keychain mirror. Either may be nil.
	StateDB  CursorStateReader
	Keychain secretStore
	Now      func() time.Time
}

// CursorStateReader reads one value from Cursor's state database. A missing
// key returns "" with a nil error.
type CursorStateReader interface {
	Value(ctx context.Context, key string) (string, error)
}

// NewCursorCredentials returns a reader over the signed-in Cursor app's
// default locations on this machine.
func NewCursorCredentials() *CursorCredentials {
	credentials := &CursorCredentials{Now: time.Now, Keychain: newCursorKeychainStore(cursorKeychainAccessToken)}
	if configDir, err := os.UserConfigDir(); err == nil {
		credentials.StateDB = CursorStateDB{Path: filepath.Join(configDir, "Cursor", "User", "globalStorage", "state.vscdb")}
	}
	return credentials
}

// AccessWithoutRefresh returns the Cursor access token. provider must be
// "cursor".
func (c *CursorCredentials) AccessWithoutRefresh(ctx context.Context, provider string) (Credential, error) {
	if !strings.EqualFold(strings.TrimSpace(provider), "cursor") {
		return Credential{}, fmt.Errorf("cursor credentials cannot serve %q", provider)
	}
	stateToken, membership := "", ""
	if c.StateDB != nil {
		// A missing or locked database is the same as no session there;
		// the keychain may still have one.
		stateToken, _ = c.StateDB.Value(ctx, cursorAccessTokenKey)
		membership, _ = c.StateDB.Value(ctx, cursorMembershipKey)
	}
	keychainToken := ""
	if c.Keychain != nil {
		if raw, err := c.Keychain.Read(ctx); err == nil {
			keychainToken = strings.TrimSpace(string(raw))
		}
	}
	stateToken = strings.TrimSpace(stateToken)
	token := stateToken
	// Mirror Cursor's own precedence: the state database wins, except when it
	// still holds a signed-out free session for a different user than the
	// keychain's.
	if token == "" {
		token = keychainToken
	} else if keychainToken != "" && strings.EqualFold(strings.TrimSpace(membership), "free") {
		stateSubject, keychainSubject := jwtSubject(stateToken), jwtSubject(keychainToken)
		if stateSubject != "" && keychainSubject != "" && stateSubject != keychainSubject {
			token = keychainToken
		}
	}
	if token == "" {
		return Credential{}, ErrCursorSignedOut
	}
	if expiresAt, ok := jwtExpiry(token); ok && expiresAt.Sub(c.now()) <= 5*time.Minute {
		return Credential{}, fmt.Errorf("cursor session needs refreshing; open Cursor so it can renew its own sign-in (Redline does not modify Cursor's credentials)")
	}
	return Credential{AccessToken: token}, nil
}

func (c *CursorCredentials) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func jwtSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return strings.TrimSpace(claims.Subject)
}

// CursorStateDB reads Cursor's state database strictly read-only. It never
// writes, so Cursor's own session handling is never disturbed.
type CursorStateDB struct{ Path string }

func (s CursorStateDB) Value(ctx context.Context, key string) (string, error) {
	if _, err := os.Stat(s.Path); err != nil {
		return "", nil
	}
	dsn := (&url.URL{Scheme: "file", Path: s.Path, RawQuery: "mode=ro&_pragma=busy_timeout(2000)&_pragma=query_only(1)"}).String()
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", fmt.Errorf("open Cursor state database: %w", err)
	}
	defer database.Close()
	var value sql.NullString
	err = database.QueryRowContext(ctx, `SELECT CAST(value AS TEXT) FROM ItemTable WHERE key = ? LIMIT 1`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		// Never include the query result; the error alone is safe to log.
		return "", fmt.Errorf("read Cursor state database: %w", err)
	}
	return strings.TrimSpace(value.String), nil
}
