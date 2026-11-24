package apicredits

import (
	"context"
	"errors"
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
	got, err := ResolveSecret(ctx, store, "acct", SecretSessionKey, file)
	if err != nil || got != "sess-FROMFILE" {
		t.Fatalf("file = %q, %v", got, err)
	}
	if err := os.WriteFile(file, []byte("sess-ROTATED"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := ResolveSecret(ctx, store, "acct", SecretSessionKey, file); got != "sess-ROTATED" {
		t.Fatalf("rotated = %q", got)
	}
	got, err = ResolveSecret(ctx, store, "acct", SecretSessionKey, "")
	if err != nil || got != "sess-FROMSTORE" {
		t.Fatalf("store = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(file, 0o644)
		if _, err := ResolveSecret(ctx, store, "acct", SecretSessionKey, file); err == nil {
			t.Fatal("read a world-readable key file")
		}
	}
}

// The environment variable serves a headless service with no store at all.
func TestResolveSessionKeyFallsBackToEnvironment(t *testing.T) {
	t.Setenv(SessionKeyEnv, "sess-FROMENV")
	got, err := ResolveSecret(context.Background(), FileSecretStore{Dir: t.TempDir()}, "acct", SecretSessionKey, "")
	if err != nil || got != "sess-FROMENV" {
		t.Fatalf("env = %q, %v", got, err)
	}
	// The environment never supplies the run API key: that would hand an
	// inherited key to jobs for an organization nobody confirmed.
	t.Setenv("OPENAI_API_KEY", "sk-inherited")
	if _, err := ResolveSecret(context.Background(), FileSecretStore{Dir: t.TempDir()}, "acct", SecretAPIKey, ""); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("api key from env = %v", err)
	}
}
