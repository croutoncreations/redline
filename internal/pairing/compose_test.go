package pairing

import (
	"errors"
	"strings"
	"testing"

	"github.com/jfox/redline/internal/config"
)

// Detection failing is not a failure when there is a configured host to use
// instead -- but it must not vanish either. The usual cause is Tailscale not
// running, and a person about to scan a tailnet code would want to know.
func composeForTest(cfg config.Config, token string, options Options) (Code, error) {
	plan, err := PlanRoutes(cfg.API.TrustedHosts, config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeOff},
		Readiness:         config.RelayReadinessOff,
	}, options)
	if err != nil {
		return Code{}, err
	}
	prepared, err := PrepareIdentity(plan, "")
	if err != nil {
		return Code{}, err
	}
	return prepared.Render(token), nil
}

func TestPlanPortErrorDocumentsZeroAsTheDefault(t *testing.T) {
	_, err := PlanRoutes([]string{"mac.example.ts.net"}, config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeOff},
		Readiness:         config.RelayReadinessOff,
	}, Options{Port: -1})
	if err == nil || !strings.Contains(err.Error(), "zero or between 1 and 65535") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlanRefusesRelayOnlyWhenRuntimeIsUnavailable(t *testing.T) {
	runtime := config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted, URL: config.DefaultHostedRelayURL, SessionID: "managed-session-abcdefghij"},
		Readiness:         config.RelayReadinessUnavailable,
		// A malformed external update must not make pairing advertise a route
		// the supervisor refuses to dial.
		Dial: true,
	}
	if _, err := PlanRoutes(nil, runtime, Options{RelayOnly: true}); err == nil || !strings.Contains(err.Error(), "ready relay") {
		t.Fatalf("error = %v", err)
	}
}

func TestComposeReportsAFailedDetectionItRecoveredFrom(t *testing.T) {
	cfg := config.Config{}
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net:8443"}

	code, err := composeForTest(cfg, "tok", Options{
		DetectHost: func() (string, error) { return "", errors.New("tailscale is not running") },
	})
	if err != nil {
		t.Fatalf("a configured host should have carried the day: %v", err)
	}
	if code.Endpoint != "macbook.example.ts.net:8443" {
		t.Errorf("endpoint = %q", code.Endpoint)
	}
	if !strings.Contains(code.Notice, "tailscale is not running") {
		t.Errorf("the detection failure was swallowed; notice = %q", code.Notice)
	}
}

// When detection succeeds there is nothing to say.
func TestComposeIsQuietWhenDetectionWorks(t *testing.T) {
	cfg := config.Config{}
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net"}

	code, err := composeForTest(cfg, "tok", Options{
		DetectHost: func() (string, error) { return "macbook.example.ts.net", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if code.Notice != "" {
		t.Errorf("unexpected notice %q", code.Notice)
	}
}

// With nothing to fall back to, the detection error is the answer.
func TestComposeReturnsTheDetectionErrorWhenItIsAllThereIs(t *testing.T) {
	cfg := config.Config{}
	_, err := composeForTest(cfg, "tok", Options{
		DetectHost: func() (string, error) { return "", errors.New("tailscale is not running") },
	})
	if err == nil || !strings.Contains(err.Error(), "tailscale is not running") {
		t.Errorf("err = %v", err)
	}
}

// Without a detector there is nothing to fail, so nothing to notice. The
// service composes codes without one -- it has no tailnet CLI to ask and no
// terminal to print a note on -- and so it never has a Notice to surface. The
// menu-bar sheet is correct not to look for one. If the service ever gains a
// detector, this test is the reminder that the response and the sheet need a
// place for what it says.
func TestComposeWithoutADetectorNeverHasANotice(t *testing.T) {
	cfg := config.Config{}
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net:8443"}

	code, err := composeForTest(cfg, "tok", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if code.Notice != "" {
		t.Errorf("a notice with no detector to fail: %q", code.Notice)
	}
}
