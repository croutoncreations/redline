package apiauth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const tokenFilename = "api-token"

func TokenPath(configPath string) string {
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		absolute = configPath
	}
	return filepath.Join(filepath.Dir(absolute), tokenFilename)
}

func EnsureToken(configPath string) (string, error) {
	path := TokenPath(configPath)
	if token, err := ReadToken(configPath); err == nil {
		return token, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return ReadToken(configPath)
	}
	if err != nil {
		return "", fmt.Errorf("create API token %q: %w", path, err)
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write API token %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close API token %q: %w", path, err)
	}
	return token, nil
}

// RotateToken replaces the stored API token with a freshly generated one.
// Every paired browser session and saved bearer credential stops working, so
// this is the escape valve for revoking access to a device you no longer
// control. The replacement is written atomically: a failed rotation leaves the
// previous token intact rather than locking the operator out of their service.
func RotateToken(configPath string) (string, error) {
	path := TokenPath(configPath)
	if _, err := ReadToken(configPath); err != nil {
		return "", err
	}
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".api-token-*")
	if err != nil {
		return "", fmt.Errorf("create replacement API token: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("secure replacement API token: %w", err)
	}
	if _, err := temporary.WriteString(token + "\n"); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("write replacement API token: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("flush replacement API token: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close replacement API token: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", fmt.Errorf("replace API token %q: %w", path, err)
	}
	return token, nil
}

func generateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate API token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func ReadToken(configPath string) (string, error) {
	path := TokenPath(configPath)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("API token %q must not be accessible by group or other users", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read API token %q: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 || len(token) > 256 {
		return "", fmt.Errorf("API token %q has an invalid length", path)
	}
	return token, nil
}
