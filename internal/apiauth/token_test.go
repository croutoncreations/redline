package apiauth_test

import (
	"os"
	"path/filepath"
	"strings"
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

func TestRotateTokenReplacesCredentialAndPreservesPermissions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "redline.yaml")
	original, err := apiauth.EnsureToken(configPath)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := apiauth.RotateToken(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if rotated == "" || rotated == original {
		t.Fatalf("rotated token %q must differ from %q", rotated, original)
	}
	stored, err := apiauth.ReadToken(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored != rotated {
		t.Fatalf("stored token = %q, want %q", stored, rotated)
	}
	info, err := os.Stat(apiauth.TokenPath(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
	// Rotation must not leave temporary files beside the credential.
	entries, err := os.ReadDir(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".api-token-") {
			t.Fatalf("rotation left a temporary file: %s", entry.Name())
		}
	}
}

func TestRotateTokenRequiresAnExistingCredential(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "redline.yaml")
	if _, err := apiauth.RotateToken(configPath); err == nil {
		t.Fatal("expected rotation to fail without an existing token")
	}
}

func TestRotateTokenRejectsInsecurePermissions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "redline.yaml")
	path := apiauth.TokenPath(configPath)
	if err := os.WriteFile(path, []byte("abcdefghijklmnopqrstuvwxyz1234567890\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := apiauth.RotateToken(configPath); err == nil {
		t.Fatal("expected rotation to refuse a world-readable token")
	}
	// The original credential must survive a refused rotation.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "abcdefghijklmnopqrstuvwxyz1234567890" {
		t.Fatalf("refused rotation modified the token file: %q", string(data))
	}
}
