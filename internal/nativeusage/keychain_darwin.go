//go:build darwin

package nativeusage

import (
	"bytes"
	"context"
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
