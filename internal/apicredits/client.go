package apicredits

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The only endpoint a session key is ever sent to.
const (
	platformOrigin   = "https://api.openai.com"
	creditGrantsPath = "/v1/dashboard/billing/credit_grants"
)

// Fixed, credential-free errors.
var (
	ErrNoSessionKey   = errors.New("no OpenAI platform session key is saved")
	ErrNotSessionKey  = errors.New("the saved key is not an OpenAI platform session key (sess-…); API keys cannot read credit grants")
	ErrSessionExpired = errors.New("OpenAI rejected the saved session key; paste a fresh one from the platform billing page")
	ErrRateLimited    = errors.New("OpenAI rate-limited the credit grants request (HTTP 429)")
)

// Credential is what a credit grants request needs. SessionKey is a
// platform browser session key and must never be logged or sent anywhere
// but the credit grants endpoint.
type Credential struct {
	SessionKey     string
	OrganizationID string
}

// FetchedReport is a Report with the time it was observed.
type FetchedReport struct {
	Report
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
}

// Client reads credit grants from the OpenAI platform.
type Client struct {
	HTTPClient *http.Client
	// BaseURL overrides the platform origin (tests only).
	BaseURL string
	Now     func() time.Time
}

func (c Client) endpoint() string {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = platformOrigin
	}
	return base + creditGrantsPath
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c Client) httpClient() *http.Client {
	base := c.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: 15 * time.Second}
	}
	// Never follow a redirect: it would carry the session key elsewhere.
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

// Fetch reads one observation of the organization's credit grants.
func (c Client) Fetch(ctx context.Context, credential Credential) (FetchedReport, error) {
	key := strings.TrimSpace(credential.SessionKey)
	if key == "" {
		return FetchedReport{}, ErrNoSessionKey
	}
	if !strings.HasPrefix(key, "sess-") {
		return FetchedReport{}, ErrNotSessionKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(), nil)
	if err != nil {
		return FetchedReport{}, fmt.Errorf("build credit grants request")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if org := strings.TrimSpace(credential.OrganizationID); org != "" {
		req.Header.Set("OpenAI-Organization", org)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Redline")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return FetchedReport{}, ctx.Err()
		}
		return FetchedReport{}, fmt.Errorf("credit grants request failed: connection failed")
	}
	defer resp.Body.Close()
	// Classify by status first: a truncated error body must not hide a
	// rejected session or a rate limit.
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return FetchedReport{}, ErrSessionExpired
	case resp.StatusCode == http.StatusTooManyRequests:
		return FetchedReport{}, ErrRateLimited
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return FetchedReport{}, fmt.Errorf("credit grants request failed: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return FetchedReport{}, fmt.Errorf("credit grants request failed: read failed")
	}
	now := c.now()
	report, err := Parse(body, now)
	if err != nil {
		return FetchedReport{}, err
	}
	return FetchedReport{Report: report, FetchedAt: &now}, nil
}
