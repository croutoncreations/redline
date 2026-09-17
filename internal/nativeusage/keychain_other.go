//go:build !darwin

package nativeusage

import "path/filepath"

// Claude Code stores its OAuth credentials at ~/.claude/.credentials.json on
// Linux and Windows (os.UserHomeDir resolves %USERPROFILE% on Windows); macOS
// keeps the credential in the login keychain instead (see keychain_darwin.go).
func newClaudeSecretStore(home, _ string) secretStore {
	return firstFileStore{Paths: []string{filepath.Join(home, ".claude", ".credentials.json")}}
}
