//go:build darwin && cgo

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
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

func TestDarwinKeychainStatusMappingDistinguishesMissingAndUnavailable(t *testing.T) {
	if err := keychainStatusError("read", -25300); !errors.Is(err, ErrLicenseNotFound) {
		t.Fatalf("item-not-found mapping = %v", err)
	}
	if err := keychainStatusError("read", -25308); !errors.Is(err, ErrLicenseStoreUnavailable) || errors.Is(err, ErrLicenseNotFound) {
		t.Fatalf("interaction-not-allowed mapping = %v", err)
	}
}

func TestSyntheticKeychainLicenseLifecycle(t *testing.T) {
	service := fmt.Sprintf("ai.redline.test.%d.%d", os.Getpid(), time.Now().UnixNano())
	account := "isolated-synthetic-item"
	store := newKeychainLicenseStore(service, account)
	ctx := context.Background()
	// Cleanup is registered before the first mutation and uses an isolated item.
	t.Cleanup(func() {
		if err := store.Clear(ctx); err != nil {
			t.Errorf("cleanup synthetic Keychain item: %v", err)
		}
	})
	if err := store.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); !errors.Is(err, ErrLicenseNotFound) {
		t.Fatalf("initial Load error = %v", err)
	}
	if err := store.Replace(ctx, "synthetic-not-a-license"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(ctx); err != nil || got != "synthetic-not-a-license" {
		t.Fatalf("Load = %q, %v", got, err)
	}
	if err := store.Replace(ctx, "replacement-synthetic-value"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(ctx); err != nil || got != "replacement-synthetic-value" {
		t.Fatalf("replacement Load = %q, %v", got, err)
	}
	if err := store.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); !errors.Is(err, ErrLicenseNotFound) {
		t.Fatalf("Load after Clear error = %v", err)
	}
}
