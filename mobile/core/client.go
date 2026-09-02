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
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: defaultTimeout},
		clock:      clock,
	}
}

// BaseURL reports the service this client targets, for display.
func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) now() time.Time {
	if c.clock == nil {
		return time.Now()
	}
	return c.clock()
}

func (c *Client) timeout() time.Duration { return defaultTimeout }

// get issues an authenticated GET and decodes a JSON response.
func (c *Client) get(ctx context.Context, path string, output any) error {
	return c.do(ctx, http.MethodGet, path, nil, output)
}

func (c *Client) do(ctx context.Context, method, path string, body, output any) error {
	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
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
		return fmt.Errorf("build request: %w", err)
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
		return fmt.Errorf("reach redline at %s: %w", c.baseURL, err)
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
		return &apiError{StatusCode: response.StatusCode, Message: problem.Error}
	}

	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
