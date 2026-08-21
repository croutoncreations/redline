//go:build darwin

package nativeusage

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

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
	if err != nil {
		return nil, fmt.Errorf("read keychain item %q: %w", s.Service, err)
	}
	return bytes.TrimSpace(output), nil
}
