package apiauth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jfox/redline/internal/apiauth"
)

func TestEnsureTokenCreatesAndReusesProtectedCredential(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "redline.yaml")
	first, err := apiauth.EnsureToken(configPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := apiauth.EnsureToken(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("tokens first=%q second=%q", first, second)
	}
	info, err := os.Stat(apiauth.TokenPath(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}

func TestReadTokenRejectsBroadPermissions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "redline.yaml")
	path := apiauth.TokenPath(configPath)
	if err := os.WriteFile(path, []byte("abcdefghijklmnopqrstuvwxyz1234567890\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := apiauth.ReadToken(configPath); err == nil {
		t.Fatal("expected insecure permission error")
	}
}
