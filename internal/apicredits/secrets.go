package apicredits

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/croutoncreations/redline/internal/apiauth"
)

// Secret kinds an API credits account can hold.
const (
	// SecretSessionKey is the platform browser session key (sess-…) used
	// only to read credit grants.
	SecretSessionKey = "session-key"
	// SecretAPIKey is the organization API key (sk-…) runs are given as
	// OPENAI_API_KEY.
	SecretAPIKey = "api-key"
)

// SessionKeyEnv supplies a session key to a service with nowhere to store
// one, for example a container. It applies to every credits account.
const SessionKeyEnv = "REDLINE_OPENAI_SESSION_KEY"

// maxSecretBytes bounds what is read from a secret file.
const maxSecretBytes = 16 << 10

var ErrSecretNotFound = errors.New("secret not found")

var secretAccountPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// SecretStore holds an account's credentials. Implementations never log or
// echo a value.
type SecretStore interface {
	Load(ctx context.Context, account, kind string) (string, error)
	Replace(ctx context.Context, account, kind, value string) error
	Clear(ctx context.Context, account, kind string) error
}

func validSecretItem(account, kind string) error {
	if !secretAccountPattern.MatchString(account) {
		return fmt.Errorf("invalid account id for a secret")
	}
	if kind != SecretSessionKey && kind != SecretAPIKey {
		return fmt.Errorf("unknown secret kind")
	}
	return nil
}

// FileSecretStore keeps each secret in an owner-only file (mode 0600 inside
// a 0700 directory on POSIX; an owner-only DACL on Windows). It is the
// store on Linux, Windows and macOS builds without Keychain access, and the
// same trust model Claude Code and the Codex CLI use for their logins.
type FileSecretStore struct{ Dir string }

func (s FileSecretStore) path(account, kind string) string {
	return filepath.Join(s.Dir, account+"."+kind)
}

func (s FileSecretStore) Load(ctx context.Context, account, kind string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validSecretItem(account, kind); err != nil {
		return "", err
	}
	value, err := readPrivateFile(s.path(account, kind))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrSecretNotFound
	}
	return value, err
}

func (s FileSecretStore) Replace(ctx context.Context, account, kind, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validSecretItem(account, kind); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("secret is empty; use Clear to remove it")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("create secret directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(s.Dir, 0o700); err != nil {
			return fmt.Errorf("protect secret directory: %w", err)
		}
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temp := filepath.Join(s.Dir, "."+account+"."+kind+"."+hex.EncodeToString(suffix))
	file, err := apiauth.CreatePrivateFile(temp)
	if err != nil {
		return fmt.Errorf("create secret file: %w", err)
	}
	_, writeErr := file.WriteString(value + "\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("write secret file")
	}
	if err := os.Rename(temp, s.path(account, kind)); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("save secret file: %w", err)
	}
	return nil
}

func (s FileSecretStore) Clear(ctx context.Context, account, kind string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validSecretItem(account, kind); err != nil {
		return err
	}
	if err := os.Remove(s.path(account, kind)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove secret file: %w", err)
	}
	return nil
}

// readPrivateFile reads a small secret file that only its owner can read.
func readPrivateFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("secret file %s is not a regular file", path)
	}
	// Windows reports fixed permission bits regardless of its ACLs.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("secret file %s must not be readable by group or other users", path)
	}
	if info.Size() > maxSecretBytes {
		return "", fmt.Errorf("secret file %s is too large", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret file %s", path)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", ErrSecretNotFound
	}
	return value, nil
}

// ResolveSecret finds an account's secret: a configured file first (re-read
// every time, so a secret manager can rotate it), then the store, then (for
// the session key only) the REDLINE_OPENAI_SESSION_KEY environment variable.
func ResolveSecret(ctx context.Context, store SecretStore, account, kind, file string) (string, error) {
	if file = strings.TrimSpace(file); file != "" {
		return readPrivateFile(file)
	}
	if store != nil {
		value, err := store.Load(ctx, account, kind)
		if err == nil {
			return value, nil
		}
		if !errors.Is(err, ErrSecretNotFound) {
			return "", err
		}
	}
	if kind == SecretSessionKey {
		if value := strings.TrimSpace(os.Getenv(SessionKeyEnv)); value != "" {
			return value, nil
		}
	}
	return "", ErrSecretNotFound
}
