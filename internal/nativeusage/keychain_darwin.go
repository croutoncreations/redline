//go:build darwin

package nativeusage

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

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

func (s macKeychainStore) CompareAndSwap(ctx context.Context, old, updated []byte) error {
	current, err := s.Read(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(old)) {
		return errCredentialsChanged
	}
	args := []string{"add-generic-password", "-U", "-s", s.Service}
	if s.Account != "" {
		args = append(args, "-a", s.Account)
	}
	// Omitting the password argument keeps credential data out of the process list.
	// The security tool reads and confirms it from stdin when -w is last.
	args = append(args, "-w")
	command := exec.CommandContext(ctx, "/usr/bin/security", args...)
	input := make([]byte, 0, len(updated)*2+2)
	input = append(input, updated...)
	input = append(input, '\n')
	input = append(input, updated...)
	input = append(input, '\n')
	command.Stdin = bytes.NewReader(input)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("update keychain item %q: %w (%s)", s.Service, err, bytes.TrimSpace(output))
	}
	return nil
}
