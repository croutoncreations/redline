//go:build integration

package relay

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/coder/websocket"
	core "github.com/jfox/redline/mobile/core"
	"testing"
)

// TestFullChainAgainstRealRelay exercises the desktop's real dial loop, the
// real Cloudflare Worker under `wrangler dev`, and a real phone-side Noise
// session. Nothing is stubbed except the local API, which stands in for
// Redline's own HTTP server.
//
// Unit tests can prove each piece in isolation and still miss the thing that
// matters: that a request survives the whole path and that the relay in the
// middle genuinely cannot read it. Behind a build tag because it needs a relay
// running:
//
//	wrangler dev --port 8788 --local --var ALLOW_UNENTITLED:true
//	go test ./internal/relay/ -tags integration -run TestFullChain
func TestFullChainAgainstRealRelay(t *testing.T) {
	if _, err := http.Get("http://127.0.0.1:8788/health"); err != nil {
		t.Skip("no relay on :8788; start wrangler dev to run this")
	}
	// Stand-in for Redline's local API.
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer real-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"path":%q,"ok":true}`, r.URL.Path)
	}))
	defer local.Close()

	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		fmt.Println("keypair:", err)
		t.FailNow()
	}
	sessionID := "fullchain-session-0123456789abc"

	// The desktop's real dial loop.
	dialer := NewDialer(DialerOptions{
		RelayURL:  "ws://127.0.0.1:8788",
		SessionID: sessionID,
		Keypair:   keypair,
		Forwarder: NewForwarder(local.URL, local.Client()),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	go dialer.Run(ctx)
	time.Sleep(2 * time.Second) // let the desktop register as host

	// The phone.
	url := fmt.Sprintf("ws://127.0.0.1:8788/v1/session/%s?role=client", sessionID)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		fmt.Println("phone dial:", err)
		t.FailNow()
	}
	defer conn.CloseNow()

	phone, err := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	if err != nil {
		fmt.Println("initiator:", err)
		t.FailNow()
	}
	first, _ := phone.StartHandshake()
	if err := conn.Write(ctx, websocket.MessageBinary, first); err != nil {
		fmt.Println("write handshake:", err)
		t.FailNow()
	}
	_, reply, err := conn.Read(ctx)
	if err != nil {
		fmt.Println("read handshake:", err)
		t.FailNow()
	}
	if err := phone.FinishHandshake(reply); err != nil {
		fmt.Println("finish handshake:", err)
		t.FailNow()
	}
	if !phone.Established() {
		t.Fatal("handshake did not complete through the relay")
	}

	// A real request with a real-looking credential.
	header := http.Header{"Authorization": []string{"Bearer real-token"}}
	request, _ := EncodeRequestParts(http.MethodGet, "/v1/dashboard", header, nil)
	sealed, _ := phone.Seal(request)

	// The bytes the relay actually forwards must contain none of this.
	if bytes.Contains(sealed, []byte("Bearer")) || bytes.Contains(sealed, []byte("real-token")) {
		t.Fatal("the credential is readable in the frame the relay forwards")
	}

	if err := conn.Write(ctx, websocket.MessageBinary, sealed); err != nil {
		fmt.Println("write request:", err)
		t.FailNow()
	}
	_, frame, err := conn.Read(ctx)
	if err != nil {
		fmt.Println("read response:", err)
		t.FailNow()
	}
	opened, err := phone.Open(frame)
	if err != nil {
		fmt.Println("open:", err)
		t.FailNow()
	}
	resp, err := DecodeResponse(opened)
	if err != nil {
		fmt.Println("decode:", err)
		t.FailNow()
	}
	if resp.Status != http.StatusOK || !strings.Contains(string(resp.Body), "/v1/dashboard") {
		t.Fatalf("desktop did not serve the request: status=%d body=%s", resp.Status, resp.Body)
	}

	// An SSRF attempt through the whole chain.
	hostile, _ := EncodeRequestParts(http.MethodGet, "http://169.254.169.254/latest", header, nil)
	hSealed, _ := phone.Seal(hostile)
	conn.Write(ctx, websocket.MessageBinary, hSealed)
	_, hFrame, err := conn.Read(ctx)
	if err == nil {
		hOpened, _ := phone.Open(hFrame)
		hResp, _ := DecodeResponse(hOpened)
		if hResp.Status < 400 {
			t.Fatalf("an SSRF attempt succeeded through the full chain: status=%d", hResp.Status)
		}
	}

	// The session survives that refusal.
	ok2, _ := EncodeRequestParts(http.MethodGet, "/v1/usage", header, nil)
	s2, _ := phone.Seal(ok2)
	conn.Write(ctx, websocket.MessageBinary, s2)
	_, f2, err := conn.Read(ctx)
	if err == nil {
		o2, _ := phone.Open(f2)
		r2, _ := DecodeResponse(o2)
		if r2.Status != http.StatusOK {
			t.Fatalf("the session did not survive a refused request: status=%d", r2.Status)
		}
	}
}
