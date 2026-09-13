package core

import (
	"context"
	"net/http"
	"testing"

	"github.com/coder/websocket"
)

// upgradeAndPump accepts a WebSocket connection and pumps frames bidirectionally.
//
// Frames arriving from the socket are forwarded to toDesktop. Frames arriving
// on toPhone are written to the socket. This mirrors what the Durable Object
// does: it drops frames into whichever half of the pair is listening, without
// inspecting them.
//
// The pump runs until the connection closes or one of the channels is drained.
// Panics and goroutine leaks on close are avoided by checking errors and
// returning quietly when the connection ends.
func upgradeAndPump(t *testing.T, w http.ResponseWriter, r *http.Request, toDesktop chan []byte, toPhone chan []byte) {
	t.Helper()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// The test server may have closed already; don't fail for that.
		return
	}
	defer conn.CloseNow()

	// Generous read limit so large frames aren't dropped by the stand-in relay.
	conn.SetReadLimit(relayClientFrameLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Read socket → toDesktop in a goroutine.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, frame, err := conn.Read(ctx)
			if err != nil {
				return
			}
			select {
			case toDesktop <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Write toPhone → socket in this goroutine.
	for {
		select {
		case frame, ok := <-toPhone:
			if !ok {
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
				cancel()
				<-readDone
				return
			}
		case <-ctx.Done():
			<-readDone
			return
		case <-readDone:
			return
		}
	}
}
