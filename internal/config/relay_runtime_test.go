package config_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
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

func TestResolvedRelayEntitlementTokenAtRejectsRawExpiration(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	resolved := config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted},
		Readiness:         config.RelayReadinessActive, Dial: true,
		EntitlementToken: config.NewRelayEntitlementToken("host-entitlement-secret"),
		ExpiresAt:        now,
	}
	if got := resolved.EntitlementTokenAt(now); got != "" {
		t.Fatalf("expired token supplier returned %q", got)
	}
	resolved.ExpiresAt = now.Add(time.Nanosecond)
	if got := resolved.EntitlementTokenAt(now); got == "" {
		t.Fatal("unexpired token supplier rejected authority")
	}
}

func TestResolvedRelayDialabilityCannotContradictReadiness(t *testing.T) {
	for _, snapshot := range []config.ResolvedRelay{
		{RelayManagedState: config.RelayManagedState{Mode: config.RelayModeOff}, Readiness: config.RelayReadinessOff, Dial: true},
		{RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted}, Readiness: config.RelayReadinessNeedsLicense, Dial: true},
		{RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted}, Readiness: config.RelayReadinessUnavailable, Dial: true},
	} {
		if snapshot.CanDial() {
			t.Fatalf("inconsistent snapshot reported dialable: %#v", snapshot)
		}
	}
	ready := config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeSelfHosted, URL: "https://relay.example", SessionID: "session-abcdefghij0123"},
		Readiness:         config.RelayReadinessSelfHosted,
		Dial:              true,
	}
	if !ready.CanDial() {
		t.Fatal("ready self-hosted snapshot should be dialable")
	}
}

func TestRelayCoordinatorDeepCopiesDeactivationIntent(t *testing.T) {
	intent := &config.RelayDeactivationIntent{ActivationID: "current-device", StateGeneration: 4}
	initial := config.ResolvedRelay{RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted, Generation: 4, Deactivation: intent}}
	coordinator := config.NewRelayCoordinator(initial)
	intent.ActivationID = "mutated-input"
	first := coordinator.Current()
	if first.Deactivation.ActivationID != "current-device" {
		t.Fatalf("constructor retained mutable intent: %#v", first.Deactivation)
	}
	first.Deactivation.ActivationID = "mutated-reader"
	if current := coordinator.Current(); current.Deactivation.ActivationID != "current-device" {
		t.Fatalf("reader mutated coordinator intent: %#v", current.Deactivation)
	}
	updates, cancel := coordinator.Subscribe()
	defer cancel()
	published := <-updates
	published.Deactivation.ActivationID = "mutated-subscriber"
	if current := coordinator.Current(); current.Deactivation.ActivationID != "current-device" {
		t.Fatalf("subscriber mutated coordinator intent: %#v", current.Deactivation)
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
