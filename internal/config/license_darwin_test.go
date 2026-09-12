//go:build darwin && cgo

package config

import (
	"context"
	"strings"
	"testing"
)

func TestDarwinLicenseStoreUsesSecurityFrameworkBoundary(t *testing.T) {
	var _ LicenseStore = keychainLicenseStore{}
	if RelayLicenseKeychainService != "ai.redline.mac.relay-license" || RelayLicenseKeychainAccount != "hosted" {
		t.Fatalf("keychain identifiers changed: service=%q account=%q", RelayLicenseKeychainService, RelayLicenseKeychainAccount)
	}
}

func TestDarwinLicenseReplacementRequiresExplicitNonEmptyValue(t *testing.T) {
	store := keychainLicenseStore{}
	err := store.Replace(context.Background(), "  ")
	if err == nil || !strings.Contains(err.Error(), "use Clear") {
		t.Fatalf("error = %v", err)
	}
}
