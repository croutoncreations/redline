package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDialerReportsOnlyProvenLiveHostConnection(t *testing.T) {
	accepted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		close(accepted)
		<-release
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	}))
	defer server.Close()

	states := make(chan bool, 2)
	dialer := NewDialer(DialerOptions{
		RelayURL:        strings.Replace(server.URL, "http://", "ws://", 1),
		SessionID:       "connection-state-session-1234",
		ConnectionState: func(connected bool) { states <- connected },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dialer.Run(ctx)
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("relay handshake did not complete")
	}
	select {
	case connected := <-states:
		if !connected {
			t.Fatal("first proven state was disconnected")
		}
	case <-time.After(time.Second):
		t.Fatal("connected state was not reported")
	}
	close(release)
	select {
	case connected := <-states:
		if connected {
			t.Fatal("closed socket remained connected")
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect state was not reported")
	}
}
