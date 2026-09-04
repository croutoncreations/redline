package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
//
// Status is part of the contract, not an extra. The desktop's own code has to
// survive the tunnel or every relayed answer reads as a flat success: a
// dispatch refused with 409 would come back as neither started nor refused,
// and a 401 could never prompt a re-pair.
// The status travels with the body, encoded into one string, because gomobile
// binds neither multiple return values nor a proxied method returning a
// struct -- and when it cannot bind something it skips the whole file,
// silently, leaving the AAR without every type in it.
//
// An earlier version read the status back through a second call. That is two
// operations on shared state, and it raced: one shared client serves three
// view models on the phone, each refreshing on its own thread, so a dispatch
// could report the status of a usage refresh that finished between the calls.
// A probe crossed 4 of 200 concurrent dispatches and -race confirmed it.
// Returning both together leaves nothing to interleave.
type RelayFallback interface {
	// Do performs the request and returns "<status> <body>": the decimal
	// status, one space, then the raw body.
	//
	// A non-2xx status is NOT an error here -- it is the desktop's own answer.
	// An error means the relay itself failed.
	Do(method, path, body string) (string, error)
}

// splitRelayAnswer separates the status from the body.
//
// A malformed prefix is an error rather than an assumed 200. FormatRelayAnswer
// is the only producer and always emits a valid decimal, so a prefix that will
// not parse means the encoding itself broke -- and calling that success would
// hand the UI a body it would try to render as data. A failure that looks like
// success is the exact shape of bug this code has been bitten by repeatedly,
// and it costs nothing to make this one loud.
//
// Only the first space is consumed, so a body that itself begins with
// something status-shaped is returned intact.
func splitRelayAnswer(answer string) (int, string, error) {
	space := strings.IndexByte(answer, ' ')
	if space <= 0 {
		return 0, "", fmt.Errorf("relay answer has no status prefix")
	}
	status, err := strconv.Atoi(answer[:space])
	if err != nil {
		return 0, "", fmt.Errorf("relay answer has an unreadable status")
	}
	if status < 100 || status > 599 {
		return 0, "", fmt.Errorf("relay answer has an out-of-range status %d", status)
	}
	return status, answer[space+1:], nil
}

// FormatRelayAnswer builds the "<status> <body>" string a RelayFallback returns.
//
// Exported so the phone's implementation cannot get the encoding subtly wrong
// in a way that silently reads every answer as 200.
func FormatRelayAnswer(status int, body string) string {
	return strconv.Itoa(status) + " " + body
}

// RelayFallbackWithStatus adapts a function to RelayFallback, for tests and
// for Go callers with no object to hang the method on.
func RelayFallbackWithStatus(f func(method, path, body string) (int, string, error)) RelayFallback {
	return relayFallbackFunc(func(method, path, body string) (string, error) {
		status, answer, err := f(method, path, body)
		if err != nil {
			return "", err
		}
		return FormatRelayAnswer(status, answer), nil
	})
}

type relayFallbackFunc func(method, path, body string) (string, error)

func (f relayFallbackFunc) Do(method, path, body string) (string, error) {
	return f(method, path, body)
}

// RelayFallbackRaw adapts a function that returns the encoded answer itself.
//
// For tests that need to produce a malformed answer on purpose; real callers
// should use RelayFallbackWithStatus, which cannot get the encoding wrong.
func RelayFallbackRaw(f func(method, path, body string) (string, error)) RelayFallback {
	return relayFallbackFunc(f)
}

// routeKey marks the context value carrying how one request was served.
//
// Per-request rather than per-client: three view models share one client, so a
// field on the client is last-write-wins, and a Runs refresh going direct
// would clear what a Usage refresh had just set. The request that took the
// route is the only thing that can truthfully report it.
type routeKey struct{}

// routeRecorder collects the route for one request.
type routeRecorder struct{ relayed bool }

// withRoute returns a context that records how its request was served.
func withRoute(ctx context.Context) (context.Context, *routeRecorder) {
	recorder := &routeRecorder{}
	return context.WithValue(ctx, routeKey{}, recorder), recorder
}

// markRelayed records that this request crossed the relay. A request with no
// recorder -- every caller that does not care -- is unaffected.
func markRelayed(ctx context.Context) {
	if recorder, ok := ctx.Value(routeKey{}).(*routeRecorder); ok {
		recorder.relayed = true
	}
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
				markRelayed(ctx)
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
	status, decoded, err := splitRelayAnswer(answer)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(decoded)),
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
