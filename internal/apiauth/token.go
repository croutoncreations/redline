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
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate API token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
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
