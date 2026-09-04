package core_test

import (
	"strings"
	"testing"

	core "github.com/jfox/redline/mobile/core"
)

// Fallback belongs at the one place every request passes through.
//
// The first attempt put it in the Kotlin sources, which meant wiring it into
// each of them separately and reproducing each method's request shape. That is
// wrong twice over: FetchRuns makes two calls, so one relayed path cannot
// stand in for it, and anything not wired up silently keeps failing. Here it
// is one change for all ~65 endpoints.
func TestClientFallsBackToTheRelayWhenDirectIsUnreachable(t *testing.T) {
	relayed := 0
	// A relay stand-in that answers with the payload the desktop would.
	fallback := func(method, path, body string) (string, error) {
		relayed++
		return `{"ok":true}`, nil
	}

	// Port 1 refuses immediately, so the direct leg fails as it would with the
	// tailnet down.
	client := core.NewClient("http://127.0.0.1:1", "token")
	client.SetRelayFallback(core.RelayFallbackFunc(fallback))

	got, err := client.FetchHealth()
	if err != nil {
		t.Fatalf("with a relay available the request must succeed: %v", err)
	}
	if relayed != 1 {
		t.Errorf("relay attempts = %d, want 1", relayed)
	}
	if got == "" {
		t.Error("expected the relayed body to be returned")
	}
}

func TestClientWithoutARelayReportsTheDirectFailure(t *testing.T) {
	client := core.NewClient("http://127.0.0.1:1", "token")

	_, err := client.FetchHealth()
	if err == nil {
		t.Fatal("expected the direct failure")
	}
	if !strings.Contains(err.Error(), "reach redline") {
		t.Errorf("error = %v, want the direct reachability failure", err)
	}
}
