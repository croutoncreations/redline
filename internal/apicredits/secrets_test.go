package apicredits

import (
	"context"
	"errors"
	"github.com/croutoncreations/redline/internal/config"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileSecretStoreRoundTripsPrivately(t *testing.T) {
	store := FileSecretStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	ctx := context.Background()
	if _, err := store.Load(ctx, "acct", SecretSessionKey); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("initial load = %v", err)
	}
	if err := store.Replace(ctx, "acct", SecretSessionKey, fakeSessionKey); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx, "acct", SecretSessionKey)
	if err != nil || got != fakeSessionKey {
		t.Fatalf("load = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		entries, _ := os.ReadDir(store.Dir)
		for _, entry := range entries {
			info, _ := entry.Info()
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("%s mode = %v", entry.Name(), info.Mode())
			}
		}
		dirInfo, _ := os.Stat(store.Dir)
		if dirInfo.Mode().Perm()&0o077 != 0 {
			t.Fatalf("dir mode = %v", dirInfo.Mode())
		}
	}
	if err := store.Replace(ctx, "acct", SecretSessionKey, "sess-REPLACEMENT"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Load(ctx, "acct", SecretSessionKey); got != "sess-REPLACEMENT" {
		t.Fatalf("replacement = %q", got)
	}
	// Kinds and accounts are separate items.
	if _, err := store.Load(ctx, "acct", SecretAPIKey); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("api key = %v", err)
	}
	if _, err := store.Load(ctx, "other", SecretSessionKey); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("other account = %v", err)
	}
	if err := store.Clear(ctx, "acct", SecretSessionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "acct", SecretSessionKey); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("after clear = %v", err)
	}
	if err := store.Clear(ctx, "acct", SecretSessionKey); err != nil {
		t.Fatalf("clearing a missing item = %v", err)
	}
}

func TestFileSecretStoreRefusesUnsafeInput(t *testing.T) {
	store := FileSecretStore{Dir: t.TempDir()}
	ctx := context.Background()
	if err := store.Replace(ctx, "../escape", SecretSessionKey, fakeSessionKey); err == nil {
		t.Fatal("accepted a path-like account id")
	}
	if err := store.Replace(ctx, "acct", SecretSessionKey, "  "); err == nil {
		t.Fatal("accepted an empty secret")
	}
	if err := store.Replace(ctx, "acct", "token", fakeSessionKey); err == nil {
		t.Fatal("accepted an unknown secret kind")
	}
}

func TestFileSecretStoreRefusesWorldReadableFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	store := FileSecretStore{Dir: t.TempDir()}
	ctx := context.Background()
	if err := store.Replace(ctx, "acct", SecretSessionKey, fakeSessionKey); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(store.Dir)
	if err := os.Chmod(filepath.Join(store.Dir, entries[0].Name()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "acct", SecretSessionKey); err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("load = %v", err)
	}
}

// A key file that is a symlink is refused: it could point anywhere,
// including at a file another user can swap.
func TestResolveSecretRefusesSymlinkedKeyFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlinks")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("sess-TARGET"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSecret(context.Background(), nil, config.APICreditsVendorOpenAI, "acct", SecretSessionKey, link); err == nil {
		t.Fatal("followed a symlinked key file")
	}
}

// Errors name the problem, never the file's path, because hold reasons
// reach the dashboard and paired phones.
func TestSecretFileErrorsDoNotNameThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	file := filepath.Join(t.TempDir(), "private-name-xyz")
	if err := os.WriteFile(file, []byte("sk-x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveSecret(context.Background(), nil, config.APICreditsVendorOpenAI, "acct", SecretAPIKey, file)
	if err == nil || strings.Contains(err.Error(), "private-name-xyz") {
		t.Fatalf("err = %v", err)
	}
}

// A configured key file wins over the store, is re-read on every call, and
// must be private.
func TestResolveSessionKeyPrefersConfiguredFile(t *testing.T) {
	ctx := context.Background()
	store := FileSecretStore{Dir: t.TempDir()}
	_ = store.Replace(ctx, "acct", SecretSessionKey, "sess-FROMSTORE")
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte("sess-FROMFILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveSecret(ctx, store, config.APICreditsVendorOpenAI, "acct", SecretSessionKey, file)
	if err != nil || got != "sess-FROMFILE" {
		t.Fatalf("file = %q, %v", got, err)
	}
	if err := os.WriteFile(file, []byte("sess-ROTATED"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := ResolveSecret(ctx, store, config.APICreditsVendorOpenAI, "acct", SecretSessionKey, file); got != "sess-ROTATED" {
		t.Fatalf("rotated = %q", got)
	}
	got, err = ResolveSecret(ctx, store, config.APICreditsVendorOpenAI, "acct", SecretSessionKey, "")
	if err != nil || got != "sess-FROMSTORE" {
		t.Fatalf("store = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(file, 0o644)
		if _, err := ResolveSecret(ctx, store, config.APICreditsVendorOpenAI, "acct", SecretSessionKey, file); err == nil {
			t.Fatal("read a world-readable key file")
		}
	}
}

// The environment variable serves a headless service with no store at all.
// It is taken out of the process environment as it is captured, so no job,
// hook or notifier Redline starts can ever inherit the session key.
func TestResolveSessionKeyFallsBackToEnvironment(t *testing.T) {
	t.Setenv(SessionKeyEnv, "sess-FROMENV")
	CaptureSessionKeyEnv()
	t.Cleanup(func() { capturedSessionKey.Store("") })
	if _, present := os.LookupEnv(SessionKeyEnv); present {
		t.Fatal("session key still in the process environment after capture")
	}
	for _, entry := range os.Environ() {
		if strings.Contains(entry, "sess-FROMENV") {
			t.Fatalf("session key visible to child processes: %s", entry[:10])
		}
	}
	got, err := ResolveSecret(context.Background(), FileSecretStore{Dir: t.TempDir()}, config.APICreditsVendorOpenAI, "acct", SecretSessionKey, "")
	if err != nil || got != "sess-FROMENV" {
		t.Fatalf("env = %q, %v", got, err)
	}
	// The environment never supplies the run API key: that would hand an
	// inherited key to jobs for an organization nobody confirmed.
	t.Setenv("OPENAI_API_KEY", "sk-inherited")
	if _, err := ResolveSecret(context.Background(), FileSecretStore{Dir: t.TempDir()}, config.APICreditsVendorOpenAI, "acct", SecretAPIKey, ""); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("api key from env = %v", err)
	}
}

// A configured key file that does not exist reads as a missing secret, so
// the account reports signed out, and the error never names the path.
func TestResolveSecretMissingKeyFileIsNotFound(t *testing.T) {
	file := filepath.Join(t.TempDir(), "private-name-xyz")
	_, err := ResolveSecret(context.Background(), nil, config.APICreditsVendorOpenAI, "acct", SecretSessionKey, file)
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("err = %v, want ErrSecretNotFound", err)
	}
	if strings.Contains(err.Error(), "private-name-xyz") {
		t.Fatalf("err names the path: %v", err)
	}
}
