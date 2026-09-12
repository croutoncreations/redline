package config_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/config"
)

func TestResolvedRelayEntitlementTokenStaysOutOfSerializationAndFormatting(t *testing.T) {
	const token = "host-entitlement-secret"
	resolved := config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted},
		EntitlementToken:  config.NewRelayEntitlementToken(token),
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for name, output := range map[string]string{
		"json":        string(raw),
		"format":      fmt.Sprint(resolved),
		"go format":   fmt.Sprintf("%#v", resolved),
		"token print": fmt.Sprint(resolved.EntitlementToken),
	} {
		if strings.Contains(output, token) {
			t.Fatalf("token crossed %s boundary: %s", name, output)
		}
	}
	if resolved.EntitlementToken.Value() != token {
		t.Fatal("dialer boundary cannot recover entitlement token")
	}
}

func TestRelayCoordinatorPublishesOneImmutableSnapshot(t *testing.T) {
	initial := config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted, URL: config.DefaultHostedRelayURL, SessionID: "session-abcdefghij0123"},
		Readiness:         config.RelayReadinessNeedsLicense,
	}
	runtime := config.NewRelayCoordinator(initial)
	updates, unsubscribe := runtime.Subscribe()
	defer unsubscribe()

	if got := <-updates; got.Mode != config.RelayModeHosted || got.Readiness != config.RelayReadinessNeedsLicense || got.Dial {
		t.Fatalf("initial snapshot=%#v", got)
	}
	next := initial
	next.Readiness = config.RelayReadinessHostedConfigured
	runtime.Update(next)
	next.Readiness = config.RelayReadinessUnavailable

	select {
	case observed := <-updates:
		if observed.Mode != config.RelayModeHosted || observed.Readiness != config.RelayReadinessHostedConfigured || observed.Dial {
			t.Fatalf("observed snapshot=%#v", observed)
		}
		if current := runtime.Current(); current != observed {
			t.Fatalf("current=%#v observed=%#v", current, observed)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime update was not published")
	}
}
