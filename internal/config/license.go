package config

import (
	"context"
	"errors"
)

const (
	// RelayLicenseKeychainService and RelayLicenseKeychainAccount are stable
	// identifiers for the one hosted-relay generic-password item.
	RelayLicenseKeychainService = "ai.redline.mac.relay-license"
	RelayLicenseKeychainAccount = "hosted"
)

var (
	ErrLicenseNotFound         = errors.New("hosted relay license not found")
	ErrLicenseStoreUnavailable = errors.New("hosted relay license store unavailable")
)

// LicenseStore is the only boundary through which the service handles a hosted
// relay license. Replace and Clear are deliberately separate operations so a
// caller cannot accidentally interpret an empty value as either action.
type LicenseStore interface {
	Load(context.Context) (string, error)
	Replace(context.Context, string) error
	Clear(context.Context) error
}

func DefaultLicenseStore() LicenseStore { return newPlatformLicenseStore() }
