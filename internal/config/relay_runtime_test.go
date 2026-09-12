package config_test

import (
	"testing"
	"time"

	"github.com/jfox/redline/internal/config"
)

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
