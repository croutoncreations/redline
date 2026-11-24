package apicredits

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/croutoncreations/redline/internal/config"
)

// KeychainService names Redline's Keychain items for API credits accounts.
// Each item's account attribute is "<account id>/<kind>".
const KeychainService = "ai.redline.api-credits"

// SecretsDirEnv names a directory of owner-only secret files to use instead
// of the Keychain, for headless services and tests. Both the service and
// the CLI honour it, so they always agree on where keys live.
const SecretsDirEnv = "REDLINE_SECRETS_DIR"

// DefaultSecretStore is where an API credits account's keys live: the login
// Keychain on macOS, otherwise owner-only files in a "secrets" directory
// beside the service's config file.
func DefaultSecretStore(configPath string) SecretStore {
	if dir := strings.TrimSpace(os.Getenv(SecretsDirEnv)); dir != "" {
		return FileSecretStore{Dir: dir}
	}
	dir := ""
	if configPath != "" {
		if absolute, err := filepath.Abs(configPath); err == nil {
			configPath = absolute
		}
		dir = filepath.Join(filepath.Dir(configPath), "secrets")
	}
	return newPlatformSecretStore(dir, KeychainService)
}

func newPlatformSecretStore(dir, service string) SecretStore {
	if _, available := config.KeychainItem(service, "probe"); available {
		return keychainSecretStore{service: service}
	}
	return FileSecretStore{Dir: dir}
}

type keychainSecretStore struct{ service string }

func (s keychainSecretStore) item(account, kind string) (config.LicenseStore, error) {
	if err := validSecretItem(account, kind); err != nil {
		return nil, err
	}
	item, _ := config.KeychainItem(s.service, account+"/"+kind)
	return item, nil
}

func (s keychainSecretStore) Load(ctx context.Context, account, kind string) (string, error) {
	item, err := s.item(account, kind)
	if err != nil {
		return "", err
	}
	value, err := item.Load(ctx)
	if errors.Is(err, config.ErrLicenseNotFound) {
		return "", ErrSecretNotFound
	}
	if err != nil {
		return "", errors.New("the Keychain is unavailable; unlock it or sign in to this Mac")
	}
	return value, nil
}

func (s keychainSecretStore) Replace(ctx context.Context, account, kind, value string) error {
	item, err := s.item(account, kind)
	if err != nil {
		return err
	}
	if err := item.Replace(ctx, value); err != nil {
		return errors.New("could not save to the Keychain")
	}
	return nil
}

func (s keychainSecretStore) Clear(ctx context.Context, account, kind string) error {
	item, err := s.item(account, kind)
	if err != nil {
		return err
	}
	if err := item.Clear(ctx); err != nil {
		return errors.New("could not remove the Keychain item")
	}
	return nil
}
