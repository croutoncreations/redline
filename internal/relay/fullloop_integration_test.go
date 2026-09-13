//go:build integration

package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/jfox/redline/mobile/core"
)

// TestFullLoopPhoneToDesktop is the whole product in one test: the phone's
// relay client, the real Cloudflare Worker running under wrangler dev, the
// desktop's real dial loop, and a local API standing in for Redline's own
// HTTP server. Nothing between the phone and the desktop is a stub.
//
// Every layer has tests of its own and they all passed while the phone could
// not talk to the relay at all. This is the one that would have failed.
//
//	wrangler dev --port 8788 --local --var ALLOW_UNENTITLED:true
//	go test ./internal/relay/ -tags integration -run TestFullLoop -v
func TestFullLoopPhoneToDesktop(t *testing.T) {
	if _, err := http.Get("http://127.0.0.1:8788/health"); err != nil {
		t.Skip("no relay on :8788; start wrangler dev to run this")
	}

	// Stands in for Redline's local API, and checks the credential arrives.
	var servedPaths []string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer desktop-api-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		servedPaths = append(servedPaths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `","ok":true}`))
	}))
	defer local.Close()

	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	sessionID := "fullloop-session-0123456789ab"

	// The desktop dials out, exactly as the service does.
	dialer := NewDialer(DialerOptions{
		RelayURL:  "ws://127.0.0.1:8788",
		SessionID: sessionID,
		Keypair:   keypair,
		Forwarder: NewForwarder(local.URL, local.Client()),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	go dialer.Run(ctx)

	// Give the desktop a moment to register as the host peer.
	time.Sleep(2 * time.Second)

	// The phone connects using only what a QR would have given it.
	phone, err := core.DialRelay(
		"ws://127.0.0.1:8788",
		sessionID,
		core.DesktopPublicKey(keypair),
		"",
	)
	if err != nil {
		t.Fatalf("phone could not reach the desktop through the relay: %v", err)
	}
	defer phone.Close()
	phone.SetAuthToken("desktop-api-token")

	// A request the app actually makes.
	got, err := phone.Request("GET", "/v1/dashboard", "")
	if err != nil {
		t.Fatalf("dashboard request: %v", err)
	}
	if !strings.Contains(got, `"path":"/v1/dashboard"`) {
		t.Fatalf("dashboard response: %q", got)
	}

	// Several requests in sequence, because a Noise session advances a nonce
	// per message and a single successful request would not prove the second
	// one works.
	for _, path := range []string{"/v1/runs", "/v1/tasks", "/v1/dashboard?fields=providers"} {
		got, err := phone.Request("GET", path, "")
		if err != nil {
			t.Fatalf("request %s: %v", path, err)
		}
		if !strings.Contains(got, `"ok":true`) {
			t.Fatalf("request %s returned %q", path, got)
		}
	}

	// The hostile path is refused at the desktop, through the whole loop.
	if _, err := phone.Request("GET", "http://169.254.169.254/latest", ""); err == nil {
		t.Fatal("an SSRF attempt succeeded through the full loop")
	}

	// And the session survives that refusal.
	if _, err := phone.Request("GET", "/v1/dashboard", ""); err != nil {
		t.Fatalf("the session did not survive a refused request: %v", err)
	}

	// Nothing hostile reached the local API.
	for _, served := range servedPaths {
		if strings.Contains(served, "169.254") || strings.Contains(served, "..") {
			t.Fatalf("a hostile path reached the local API: %q", served)
		}
	}
	if len(servedPaths) < 5 {
		t.Fatalf("expected the local API to serve every good request, saw %v", servedPaths)
	}
}
