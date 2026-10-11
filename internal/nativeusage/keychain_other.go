//go:build !darwin

package nativeusage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// Claude Code stores its OAuth credentials at ~/.claude/.credentials.json on
// Linux and Windows (os.UserHomeDir resolves %USERPROFILE% on Windows); macOS
// keeps the credential in the login keychain instead (see keychain_darwin.go).
// Cursor keeps its session only in its state database off macOS.
func newCursorKeychainStore(string) secretStore { return nil }

func newClaudeSecretStore(home, _ string) secretStore {
	return firstFileStore{Paths: []string{filepath.Join(home, ".claude", ".credentials.json")}}
}

// newClaudeProfileStore reads a profile directory's own credential file.
// readOnlyStore hides the file store's write method, so a profile's login is
// never refreshed or rewritten by Redline.
// ClaudeProfileLoginPresent reports whether the profile has a credential
// file, without reading it.
func ClaudeProfileLoginPresent(_ context.Context, dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func newClaudeProfileStore(dir string) secretStore {
	return readOnlyStore{firstFileStore{Paths: []string{filepath.Join(dir, ".credentials.json")}}}
}
