package apicredits

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"

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

// capturedSessionKey holds REDLINE_OPENAI_SESSION_KEY once taken out of the
// process environment.
var capturedSessionKey atomic.Value

// CaptureSessionKeyEnv moves REDLINE_OPENAI_SESSION_KEY from the process
// environment into memory. Every job, hook, notifier and discovery command
// Redline starts inherits os.Environ(), and the session key carries full
// platform dashboard access, so it must not stay there. The service calls
// this before starting anything; it is safe to call more than once.
func CaptureSessionKeyEnv() {
	if value, present := os.LookupEnv(SessionKeyEnv); present {
		if value = strings.TrimSpace(value); value != "" {
			capturedSessionKey.Store(value)
		}
		_ = os.Unsetenv(SessionKeyEnv)
	}
}

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
	return readPrivateFile(s.path(account, kind))
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
	// The directory must be a real directory this user owns: a symlink or
	// someone else's directory could redirect or expose the key.
	if info, err := os.Lstat(s.Dir); err != nil || !info.IsDir() || (runtime.GOOS != "windows" && !ownedByCurrentUser(info)) {
		return fmt.Errorf("the secret directory must be a directory owned by the user running Redline")
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
// It is opened without following a final symlink and checked through the
// open handle, so the file cannot be swapped between check and read. Errors
// never name the path: hold reasons reach the dashboard and phones.
func readPrivateFile(path string) (string, error) {
	file, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("the key file could not be opened (it must be a regular file, not a symlink)")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("the key file could not be read")
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("the key file is not a regular file")
	}
	// Windows reports fixed permission bits regardless of its ACLs, and
	// Redline does not inspect a configured file's DACL there: the user must
	// keep the file private (docs/scheduling.md says so). Files Redline
	// writes itself get an owner-only DACL from apiauth.CreatePrivateFile.
	if runtime.GOOS != "windows" {
		if info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("the key file must not be readable by group or other users (chmod 600)")
		}
		if !ownedByCurrentUser(info) {
			return "", fmt.Errorf("the key file must be owned by the user running Redline")
		}
	}
	if info.Size() > maxSecretBytes {
		return "", fmt.Errorf("the key file is too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSecretBytes+1))
	if err != nil {
		return "", fmt.Errorf("the key file could not be read")
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
		if value, _ := capturedSessionKey.Load().(string); value != "" {
			return value, nil
		}
	}
	return "", ErrSecretNotFound
}
