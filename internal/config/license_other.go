//go:build !darwin

package config

import "context"

type unavailableLicenseStore struct{}

func newPlatformLicenseStore() LicenseStore { return unavailableLicenseStore{} }

func (unavailableLicenseStore) Load(context.Context) (string, error) {
	return "", ErrLicenseNotFound
}

func (unavailableLicenseStore) Replace(context.Context, string) error {
	return ErrLicenseStoreUnavailable
}

func (unavailableLicenseStore) Clear(context.Context) error {
	return ErrLicenseStoreUnavailable
}
