package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Connection states the UI can show. Strings rather than an enum because
// gomobile binds only simple types across the FFI boundary.
const (
	// StreamStateConnecting means an attempt is in progress.
	StreamStateConnecting = "connecting"
	// StreamStateLive means frames are arriving.
	StreamStateLive = "live"
	// StreamStateReconnecting means the connection dropped and will be retried.
	StreamStateReconnecting = "reconnecting"
	// StreamStateUnauthorized means the credential was rejected. Terminal:
	// retrying would hammer the desktop and never succeed.
	StreamStateUnauthorized = "unauthorized"
	// StreamStateStopped means the caller stopped the stream.
	StreamStateStopped = "stopped"
)

// defaultReconnectDelayMillis is how long to wait before retrying a dropped
// connection. A desktop that sleeps and wakes is the normal case, so this is
// short enough to feel immediate without spinning while it is away.
const defaultReconnectDelayMillis = 2000

// UsageStreamSink receives rendered views and connection state.
//
// Implemented on the platform side; gomobile turns this into a Java interface.
type UsageStreamSink interface {
	// OnUsage receives a rendered UsageView as JSON, the same shape FetchUsage
	// returns, so the UI has one thing to render rather than two.
	OnUsage(payload string)
	// OnState receives one of the StreamState constants.
	OnState(state string)
}

// UsageStream is a running subscription.
type UsageStream struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// Stop ends the stream and any pending reconnection.
//
// Safe to call more than once, because a screen can be torn down along more
// than one path.
func (s *UsageStream) Stop() {
	s.once.Do(func() {
		s.cancel()
		<-s.done
	})
}

// StreamUsage subscribes to live capacity updates.
func (c *Client) StreamUsage(sink UsageStreamSink) *UsageStream {
	return c.StreamUsageWithBackoff(sink, defaultReconnectDelayMillis)
}

// StreamUsageWithBackoff is StreamUsage with a configurable retry delay, so
// tests do not have to wait seconds.
func (c *Client) StreamUsageWithBackoff(sink UsageStreamSink, reconnectDelayMillis int) *UsageStream {
	if reconnectDelayMillis <= 0 {
		reconnectDelayMillis = defaultReconnectDelayMillis
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream := &UsageStream{cancel: cancel, done: make(chan struct{})}

	go func() {
		defer close(stream.done)
		delay := time.Duration(reconnectDelayMillis) * time.Millisecond

		for {
			if ctx.Err() != nil {
				sink.OnState(StreamStateStopped)
				return
			}

			sink.OnState(StreamStateConnecting)
			err := c.consumeUsageStream(ctx, sink)

			switch {
			case ctx.Err() != nil:
				sink.OnState(StreamStateStopped)
				return
			case IsUnauthorized(err):
				// The credential will not become valid by trying again, and
				// retrying would hammer the desktop forever.
				sink.OnState(StreamStateUnauthorized)
				return
			}

			// Any other end is a dropped connection: a sleeping desktop, a
			// dropped network, a restart. All are recoverable and all are
			// normal, so the stream waits and tries again.
			sink.OnState(StreamStateReconnecting)
			select {
			case <-ctx.Done():
				sink.OnState(StreamStateStopped)
				return
			case <-time.After(delay):
			}
		}
	}()

	return stream
}

// consumeUsageStream reads one connection to exhaustion, returning why it
// ended.
func (c *Client) consumeUsageStream(ctx context.Context, sink UsageStreamSink) error {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet,
		// The stream re-sends the whole read model every few seconds, so
		// trimming it to what the screen renders is where the saving compounds.
		c.baseURL+"/v1/dashboard/events?fields=providers,health", nil,
	)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	// A streaming response never completes, so the client's overall timeout
	// must not apply. Liveness comes from the server's own cadence instead:
	// if frames stop arriving the read blocks until the connection drops, and
	// the caller reconnects.
	streamClient := &http.Client{Transport: c.httpClient.Transport}
	response, err := streamClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return c.errorFromResponse(response)
	}

	sink.OnState(StreamStateLive)

	scanner := bufio.NewScanner(response.Body)
	// Frames carry a whole dashboard payload, which is far larger than the
	// default 64KB line limit even after trimming.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			// A blank line ends an event.
			if data.Len() > 0 {
				c.deliverUsageFrame(data.String(), sink)
				data.Reset()
			}
		}
		// Other fields (event:, id:, retry:) are ignored: there is one event
		// type on this endpoint.
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("stream ended")
}

// deliverUsageFrame renders one frame and hands it to the sink.
//
// A malformed frame is skipped rather than fatal: one bad payload is not a
// reason to stop showing every frame after it.
func (c *Client) deliverUsageFrame(raw string, sink UsageStreamSink) {
	var payload dashboardPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return
	}
	rendered, err := json.Marshal(renderUsage(payload, c.now()))
	if err != nil {
		return
	}
	sink.OnUsage(string(rendered))
}
