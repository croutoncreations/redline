//go:build integration

package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	core "github.com/croutoncreations/redline/mobile/core"
	"github.com/flynn/noise"
)

// TestDesktopRecoversFromAStaleHostSocket replays the production outage
// against the real Worker: a previous host socket is still registered but
// will never answer (its network path vanished). Before the fix the real
// desktop got 409 for as long as that socket lived, and a phone was admitted
// and handshook into silence. Now the desktop's dial must replace the stale
// host and a phone must complete a handshake promptly.
//
//	wrangler dev --port 8788 --local --var ALLOW_UNENTITLED:true
//	go test ./internal/relay/ -tags integration -run TestDesktopRecoversFromAStaleHostSocket
func TestDesktopRecoversFromAStaleHostSocket(t *testing.T) {
	if _, err := http.Get("http://127.0.0.1:8788/health"); err != nil {
		t.Skip("no relay on :8788; start wrangler dev to run this")
	}
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer local.Close()

	sessionID := fmt.Sprintf("stale-host-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// The stale host: registered, never reads, never answers.
	stale, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:8788/v1/session/%s?role=host", sessionID), nil)
	if err != nil {
		t.Fatalf("stale host dial: %v", err)
	}
	defer stale.CloseNow()

	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	rec := &recordingLog{}
	dialer := NewDialer(DialerOptions{
		RelayURL:  "ws://127.0.0.1:8788",
		SessionID: sessionID,
		Keypair:   keypair,
		Forwarder: NewForwarder(local.URL, local.Client()),
		Logf:      rec.log,
	})
	go dialer.Run(ctx)

	// The phone must reach the real desktop, not the stale socket, well inside
	// the window that used to last half an hour.
	deadline := time.Now().Add(8 * time.Second)
	for {
		if err := tryPhoneHandshake(ctx, sessionID, keypair); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("phone never reached the desktop past a stale host: %v\nlog:\n%s", err, rec.all())
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func tryPhoneHandshake(ctx context.Context, sessionID string, keypair noise.DHKey) error {
	conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:8788/v1/session/%s?role=client", sessionID), nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	phone, err := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	if err != nil {
		return err
	}
	first, _ := phone.StartHandshake()
	if err := conn.Write(ctx, websocket.MessageBinary, first); err != nil {
		return err
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, reply, err := conn.Read(readCtx)
	if err != nil {
		return err
	}
	return phone.FinishHandshake(reply)
}
