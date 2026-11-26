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

// keychainOpener returns a Keychain item, and false when this build or
// platform has no Keychain.
type keychainOpener func(service, account string) (config.LicenseStore, bool)

func newPlatformSecretStore(dir, service string) SecretStore {
	return platformSecretStore(dir, service, config.KeychainItem)
}

// platformSecretStore uses the Keychain wherever this build has one, and
// private files otherwise. A Keychain that exists but is locked or
// unreachable is not a reason to switch to files: the keys are in the
// Keychain, so files would read as "no key saved" and sign the account out.
// Such a Keychain is reported by each call instead (see keychainUsable).
func platformSecretStore(dir, service string, open keychainOpener) SecretStore {
	if _, available := open(service, "probe"); available {
		return keychainSecretStore{service: service, open: open}
	}
	return FileSecretStore{Dir: dir}
}

// keychainUsable reports whether the Keychain answers: a missing item is
// fine, "unavailable" (locked, no login session) is not.
func keychainUsable(open keychainOpener, service string) bool {
	item, available := open(service, "probe")
	if !available {
		return false
	}
	_, err := item.Load(context.Background())
	return err == nil || errors.Is(err, config.ErrLicenseNotFound)
}

// KeychainProblem explains why the platform's Keychain can't be used right
// now, or "" when it can (or this platform keeps keys in files). The service
// logs it at start so a locked Keychain is visible before an account reads
// as signed out.
func KeychainProblem() string {
	if os.Getenv(SecretsDirEnv) != "" {
		return ""
	}
	if _, available := config.KeychainItem(KeychainService, "probe"); !available {
		return ""
	}
	if keychainUsable(config.KeychainItem, KeychainService) {
		return ""
	}
	return keychainUnavailable
}

const keychainUnavailable = "the login Keychain is unavailable (locked, or no user session); unlock it or sign in to this Mac, or set " + SecretsDirEnv + " for a headless service"

type keychainSecretStore struct {
	service string
	open    keychainOpener
}

func (s keychainSecretStore) item(account, kind string) (config.LicenseStore, error) {
	if err := validSecretItem(account, kind); err != nil {
		return nil, err
	}
	open := s.open
	if open == nil {
		open = config.KeychainItem
	}
	item, _ := open(s.service, account+"/"+kind)
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
		return "", errors.New(keychainUnavailable)
	}
	return value, nil
}

func (s keychainSecretStore) Replace(ctx context.Context, account, kind, value string) error {
	item, err := s.item(account, kind)
	if err != nil {
		return err
	}
	if err := item.Replace(ctx, value); err != nil {
		return errors.New("could not save to the Keychain: " + keychainUnavailable)
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
