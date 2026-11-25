//go:build darwin

package nativeusage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
)

// securityItemNotFound is security(1)'s exit status for errSecItemNotFound:
// the item does not exist. Any other failure -- a locked keychain, a denied
// prompt -- is not a sign-out and keeps its own message.
const securityItemNotFound = 44

// macKeychainStore is deliberately read-only. Claude Code owns this shared
// credential, and writing it through security(1)'s interactive password prompt
// truncates long OAuth documents. Redline must fail closed when Claude's token
// needs refreshing instead of mutating another application's credential.
type macKeychainStore struct{ Service, Account string }

func newClaudeSecretStore(_ string, user string) secretStore {
	return macKeychainStore{Service: "Claude Code-credentials", Account: user}
}

// ClaudeProfileKeychainService is the keychain item Claude Code keeps for a
// profile started with CLAUDE_CONFIG_DIR=dir: the default service name with
// the first 8 hex digits of SHA-256(dir) appended. Only the item's name is
// derived here; its contents stay Claude Code's.
func ClaudeProfileKeychainService(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

// newClaudeProfileStore opens a profile's keychain item, read-only like the
// default one. The account is not pinned: Claude Code writes it as the Mac
// user, and the service name alone already names one profile.
func newClaudeProfileStore(dir string) secretStore {
	return macKeychainStore{Service: ClaudeProfileKeychainService(dir)}
}

// ClaudeProfileLoginPresent reports whether Claude Code has a login stored
// for the profile at dir. It asks only whether the keychain item exists
// (no -w), so the secret is never read and no access prompt is shown.
func ClaudeProfileLoginPresent(ctx context.Context, dir string) (bool, error) {
	err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", ClaudeProfileKeychainService(dir)).Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == securityItemNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check keychain for Claude profile: %w", err)
	}
	return true, nil
}

// newCursorKeychainStore reads Cursor's keychain mirror of its session. Like
// the Claude store it is read-only: Redline never rewrites Cursor's login.
func newCursorKeychainStore(service string) secretStore {
	return macKeychainStore{Service: service}
}

func (s macKeychainStore) Read(ctx context.Context) ([]byte, error) {
	args := []string{"find-generic-password", "-s", s.Service}
	if s.Account != "" {
		args = append(args, "-a", s.Account)
	}
	args = append(args, "-w")
	output, err := exec.CommandContext(ctx, "/usr/bin/security", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == securityItemNotFound {
		return nil, fmt.Errorf("keychain item %q: %w", s.Service, errNoCredential)
	}
	if err != nil {
		return nil, fmt.Errorf("read keychain item %q: %w", s.Service, err)
	}
	return bytes.TrimSpace(output), nil
}
