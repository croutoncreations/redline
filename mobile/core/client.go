package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultTimeout = 20 * time.Second

// Client talks to a Redline service.
//
// It is the gomobile entry point, so it is constructed by function rather than
// struct literal and holds no exported fields: gomobile binds methods, not
// struct internals.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	clock      func() time.Time
	// relay carries requests when the direct route is unreachable. Nil means
	// direct only, which is every desktop paired before relays existed.
	relay RelayFallback
}

// RelayFallback carries one request over a relayed tunnel.
//
// An interface rather than a func type because gomobile binds interfaces but
// not function values, and the Android implementation lives in Kotlin.
type RelayFallback interface {
	// Do performs the request and returns the raw response body.
	Do(method, path, body string) (string, error)
}

// RelayFallbackFunc adapts a function to RelayFallback, for tests and for Go
// callers that have no object to hang the method on.
type RelayFallbackFunc func(method, path, body string) (string, error)

// Do implements RelayFallback.
func (f RelayFallbackFunc) Do(method, path, body string) (string, error) {
	return f(method, path, body)
}

// SetRelayFallback installs the route used when the direct one fails.
//
// Set at the client rather than at each call site: every request already
// funnels through send, so one place covers all ~65 endpoints. The earlier
// attempt wired fallback into individual screens, which meant each had to
// reproduce its own request shape and anything missed kept failing silently.
func (c *Client) SetRelayFallback(fallback RelayFallback) {
	c.relay = fallback
}

// NewClient returns a client for the given Redline base URL and bearer token.
func NewClient(baseURL, token string) *Client {
	return NewClientWithClock(baseURL, token, time.Now)
}

// NewClientWithClock is NewClient with an injectable clock, so countdowns can
// be asserted deterministically in tests.
func NewClientWithClock(baseURL, token string, clock func() time.Time) *Client {
	if clock == nil {
		clock = time.Now
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		// Tokens are pasted, read from files, and scanned from QR codes, so
		// they arrive with stray whitespace. A trailing newline makes an
		// invalid header value, which fails as a confusing transport error
		// rather than as an auth problem.
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: defaultTimeout},
		clock:      clock,
	}
}

// BaseURL reports the service this client targets, for display.
func (c *Client) BaseURL() string { return c.baseURL }

// now reads the injected clock. Every constructor path substitutes time.Now
// for a nil clock, so this does not need its own nil guard.
func (c *Client) now() time.Time { return c.clock() }

// get issues an authenticated GET and decodes a JSON response.
func (c *Client) get(ctx context.Context, path string, output any) error {
	return c.do(ctx, http.MethodGet, path, nil, output)
}

// postNoBody issues an authenticated POST with no request body and reports the
// response status alongside the decoded body.
//
// The status is returned because some endpoints distinguish outcomes by code
// rather than by payload: task dispatch uses 202 for "a run started" and 200
// for "considered and held back", which are opposite answers with similar
// bodies.
func (c *Client) postNoBody(ctx context.Context, path string, output any) (int, error) {
	return c.doWithStatus(ctx, http.MethodPost, path, nil, output)
}

// doCapturingResponse issues a request and hands back the response itself
// rather than a decoded body.
//
// Pairing needs this because its result arrives as a Set-Cookie header, not as
// JSON. The response body is drained and closed before returning, so the caller
// only inspects headers.
func (c *Client) doCapturingResponse(
	ctx context.Context, method, path string, body any,
) (*http.Response, error) {
	response, err := c.send(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, c.errorFromResponse(response)
	}
	return response, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, output any) error {
	_, err := c.doWithStatus(ctx, method, path, body, output)
	return err
}

// send builds and issues one authenticated request. The caller closes the body.
func (c *Client) send(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = strings.NewReader(string(encoded))
	}

	var request *http.Request
	var err error
	if reader != nil {
		request, err = http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	} else {
		request, err = http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		// The direct route is unreachable. If a relay is configured, try it
		// before giving up: this is the whole point of having one, and it is
		// exactly the case where the phone has left the tailnet.
		//
		// Only transport failures fall back. An HTTP error response means the
		// desktop answered, so the relay would only reach the same desktop and
		// get the same answer more slowly.
		if c.relay != nil {
			if relayed, relayErr := c.relayRequest(method, path, body); relayErr == nil {
				return relayed, nil
			}
		}
		// Transport failures are reachability problems, never auth problems;
		// keeping them distinct is what lets the UI say "desktop unreachable"
		// rather than "please pair again".
		return nil, fmt.Errorf("reach redline at %s: %w", c.baseURL, err)
	}
	return response, nil
}

// relayRequest replays one request through the relay and shapes the answer
// like an http.Response so callers cannot tell the routes apart.
func (c *Client) relayRequest(method, path string, body any) (*http.Response, error) {
	encoded := ""
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		encoded = string(raw)
	}

	answer, err := c.relay.Do(method, path, encoded)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(answer)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// errorFromResponse turns a non-2xx into an apiError carrying the service's own
// explanation, so the UI can show why rather than a bare status code.
func (c *Client) errorFromResponse(response *http.Response) error {
	var problem struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(response.Body).Decode(&problem)
	if problem.Error == "" {
		problem.Error = response.Status
	}
	return &apiError{StatusCode: response.StatusCode, Message: problem.Error}
}

func (c *Client) doWithStatus(ctx context.Context, method, path string, body, output any) (int, error) {
	response, err := c.send(ctx, method, path, body)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, c.errorFromResponse(response)
	}

	if output != nil {
		err := json.NewDecoder(response.Body).Decode(output)
		// An empty body is a legitimate answer for a status that carries its
		// own meaning, such as 202 for "accepted". Treating the absent body as
		// a decode failure would report a successful request as an error.
		if err != nil && !errors.Is(err, io.EOF) {
			return response.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return response.StatusCode, nil
}
