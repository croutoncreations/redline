package apicredits

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

// lockedItem is a Keychain item whose every call fails, as on a locked
// Keychain or a headless session without one.
type lockedItem struct{}

func (lockedItem) Load(context.Context) (string, error) {
	return "", config.ErrLicenseStoreUnavailable
}
func (lockedItem) Replace(context.Context, string) error { return config.ErrLicenseStoreUnavailable }
func (lockedItem) Clear(context.Context) error           { return config.ErrLicenseStoreUnavailable }

// A Keychain that is present but unusable must not read as "no key saved":
// the keys are in it, so falling back to (empty) files would sign the
// account out. The failure names the Keychain and how to fix it, and a
// missing item still reads as not found.
func TestUnusableKeychainIsReportedNotTreatedAsSignedOut(t *testing.T) {
	store := keychainSecretStore{service: "svc", open: func(string, string) (config.LicenseStore, bool) { return lockedItem{}, true }}
	_, err := store.Load(context.Background(), "acct", SecretSessionKey)
	if err == nil || errors.Is(err, ErrSecretNotFound) || !strings.Contains(err.Error(), "Keychain") {
		t.Fatalf("Load error = %v", err)
	}
	if err := store.Replace(context.Background(), "acct", SecretSessionKey, "sess-x"); err == nil || !strings.Contains(err.Error(), "Keychain") {
		t.Fatalf("Replace error = %v", err)
	}
}

// Without a Keychain at all (another OS, or a build without one) the
// platform store is the private files store.
func TestNoKeychainUsesPrivateFiles(t *testing.T) {
	dir := t.TempDir()
	store := platformSecretStore(dir, "svc", func(string, string) (config.LicenseStore, bool) { return nil, false })
	if files, ok := store.(FileSecretStore); !ok || files.Dir != dir {
		t.Fatalf("store = %#v", store)
	}
}

// The Keychain probe reports a usable Keychain, and one that answers every
// call with "unavailable" as unusable.
func TestKeychainProbe(t *testing.T) {
	if keychainUsable(func(string, string) (config.LicenseStore, bool) { return lockedItem{}, true }, "svc") {
		t.Fatal("a locked Keychain probed as usable")
	}
	if !keychainUsable(func(string, string) (config.LicenseStore, bool) { return notFoundItem{}, true }, "svc") {
		t.Fatal("a working Keychain without the probe item probed as unusable")
	}
}

type notFoundItem struct{}

func (notFoundItem) Load(context.Context) (string, error)  { return "", config.ErrLicenseNotFound }
func (notFoundItem) Replace(context.Context, string) error { return nil }
func (notFoundItem) Clear(context.Context) error           { return nil }
