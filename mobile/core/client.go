package core

import (
	"context"
	"encoding/json"
	"fmt"
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

func (c *Client) do(ctx context.Context, method, path string, body, output any) error {
	_, err := c.doWithStatus(ctx, method, path, body, output)
	return err
}

func (c *Client) doWithStatus(ctx context.Context, method, path string, body, output any) (int, error) {
	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode request: %w", err)
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
		return 0, fmt.Errorf("build request: %w", err)
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
		// Transport failures are reachability problems, never auth problems;
		// keeping them distinct is what lets the UI say "desktop unreachable"
		// rather than "please pair again".
		return 0, fmt.Errorf("reach redline at %s: %w", c.baseURL, err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&problem)
		if problem.Error == "" {
			problem.Error = response.Status
		}
		return response.StatusCode, &apiError{StatusCode: response.StatusCode, Message: problem.Error}
	}

	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			return response.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return response.StatusCode, nil
}
