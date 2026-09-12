//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRelayStateInterruptedBeforeRenamePreservesOldState(t *testing.T) {
	store := NewRelayStateStore(filepath.Join(t.TempDir(), "relay-state.json"))
	original := RelayManagedState{Mode: RelayModeSelfHosted, URL: "https://old.example.com", SessionID: "session-abcdefghij0123"}
	replacement := RelayManagedState{Mode: RelayModeSelfHosted, URL: "https://new.example.com", SessionID: original.SessionID}
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	previous := relayStateBeforeRename
	relayStateBeforeRename = func() error { return errors.New("synthetic interruption") }
	t.Cleanup(func() { relayStateBeforeRename = previous })
	if err := store.Save(replacement); err == nil {
		t.Fatal("interrupted Save unexpectedly succeeded")
	}
	loaded, exists, err := store.Load()
	if err != nil || !exists || loaded != original {
		t.Fatalf("old state not preserved: loaded=%#v exists=%v err=%v", loaded, exists, err)
	}
}

func TestRelayStateFailureAfterRenameReportsUncertainCommit(t *testing.T) {
	store := NewRelayStateStore(filepath.Join(t.TempDir(), "relay-state.json"))
	original := RelayManagedState{Mode: RelayModeSelfHosted, URL: "https://old.example.com", SessionID: "session-abcdefghij0123"}
	replacement := RelayManagedState{Mode: RelayModeSelfHosted, URL: "https://new.example.com", SessionID: original.SessionID}
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	previous := relayStateAfterRename
	relayStateAfterRename = func() error { return errors.New("synthetic directory failure") }
	t.Cleanup(func() { relayStateAfterRename = previous })
	err := store.Save(replacement)
	var uncertain *RelayStateCommitError
	if !errors.As(err, &uncertain) {
		t.Fatalf("post-rename error = %v, want RelayStateCommitError", err)
	}
	loaded, exists, loadErr := store.Load()
	if loadErr != nil || !exists || loaded != replacement {
		t.Fatalf("published state not observable: loaded=%#v exists=%v err=%v", loaded, exists, loadErr)
	}
}
