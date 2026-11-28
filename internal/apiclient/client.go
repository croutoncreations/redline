package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	// TokenProblem explains why Token is empty (for example an unreadable or
	// over-permissioned token file) so a 401 can name the real cause instead
	// of leaving the caller to guess.
	TokenProblem string
	HTTPClient   *http.Client
}

func (c Client) Do(ctx context.Context, method, path string, body, output any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s %s: encode request body: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(
		ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader,
	)
	if err != nil {
		// The parser's error repeats the URL, or part of its userinfo
		// (for example `invalid URL escape "%ss"`), which can leak a password
		// embedded in --api. Report only the redacted address.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("%s %s: build request: invalid URL for %s", method, path, c.redactedBaseURL())
		}
		return fmt.Errorf("%s %s: build request: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return c.transportError(method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&problem)
		if problem.Error == "" {
			problem.Error = resp.Status
		}
		message := fmt.Sprintf("%s %s: %d %s", method, path, resp.StatusCode, problem.Error)
		if resp.StatusCode == http.StatusUnauthorized {
			message += "; " + c.tokenHint()
		}
		return errors.New(message)
	}
	if output != nil {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			return fmt.Errorf("%s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// transportError reports a request that never produced a response. net/http
// wraps these in a *url.Error that repeats the method and full URL, which the
// message already carries, so unwrap it and name the service that was dialed.
func (c Client) transportError(method, path string, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	// A canceled context (for example Ctrl-C) is the caller's doing, not an
	// unreachable service.
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	hint := ""
	if errors.Is(err, syscall.ECONNREFUSED) {
		hint = "; start the service with `redline serve` or point --api at the address it is listening on"
	}
	return fmt.Errorf("%s %s: cannot reach Redline at %s: %w%s", method, path, c.redactedBaseURL(), err, hint)
}

// redactedBaseURL hides any password embedded in --api (for example
// http://user:password@host) so it does not reach stderr or copied error reports.
func (c Client) redactedBaseURL() string {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil {
		return "the configured API address"
	}
	return parsed.Redacted()
}

func (c Client) tokenHint() string {
	switch {
	case c.TokenProblem != "":
		return "no API token was sent: " + c.TokenProblem
	case c.Token == "":
		return "no API token was sent; set REDLINE_API_TOKEN or pass --config pointing beside the service's api-token file"
	default:
		return "the service rejected the API token; check REDLINE_API_TOKEN, or run with the --config the service was started with (it may have been rotated)"
	}
}
