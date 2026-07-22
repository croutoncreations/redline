//go:build !darwin

package nativeusage

import "path/filepath"

func newClaudeSecretStore(home, _ string) secretStore {
	return firstFileStore{Paths: []string{filepath.Join(home, ".claude/.credentials.json")}}
}
