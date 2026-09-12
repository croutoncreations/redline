//go:build dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jfox/redline/internal/config"
)

func TestDefaultLicenseStoreReportsUnavailableOffDarwin(t *testing.T) {
	if _, err := config.DefaultLicenseStore().Load(context.Background()); !errors.Is(err, config.ErrLicenseStoreUnavailable) {
		t.Fatalf("Load error=%v, want ErrLicenseStoreUnavailable", err)
	}
	resolved, err := config.NewRelayResolver(
		config.NewRelayStateStore(filepath.Join(t.TempDir(), "relay-state.json")),
		config.DefaultLicenseStore(),
	).Resolve(context.Background(), config.RelayBootstrap{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Readiness != config.RelayReadinessUnavailable || resolved.Dial {
		t.Fatalf("resolved=%#v", resolved)
	}
}
