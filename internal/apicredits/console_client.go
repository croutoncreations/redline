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

// The only origin a Console session key is ever sent to.
const consoleOrigin = "https://platform.claude.com"

// ConsoleSessionKeyPrefix starts every Claude Console session key (the
// sessionKey cookie).
const ConsoleSessionKeyPrefix = "sk-ant-sid"

// consoleUserAgent is a browser-like User-Agent: the Console serves its
// own web app and may refuse obviously non-browser clients.
const consoleUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Fixed, credential-free errors.
var (
	ErrNoConsoleSessionKey         = errors.New("no Claude Console session key is saved")
	ErrNotConsoleSessionKey        = errors.New("the saved key is not a Claude Console session key (sk-ant-sid…); API keys cannot read the credit balance")
	ErrConsoleSessionExpired       = errors.New("Claude Console rejected the saved session key; paste a fresh sessionKey cookie from platform.claude.com")
	ErrConsoleOrganizationNotFound = errors.New("the Claude Console organization was not found or has no API access (HTTP 404); check organization_id")
	ErrConsoleRateLimited          = errors.New("Claude Console rate-limited the credits request (HTTP 429)")
)

// ConsoleClient reads prepaid credits from the Claude Console.
type ConsoleClient struct {
	HTTPClient *http.Client
	// BaseURL overrides the Console origin (tests only).
	BaseURL string
	// UserAgent overrides the browser-like default (tests only).
	UserAgent string
	Now       func() time.Time
}

func (c ConsoleClient) origin() string {
	if base := strings.TrimRight(c.BaseURL, "/"); base != "" {
		return base
	}
	return consoleOrigin
}

func (c ConsoleClient) organizationsURL() string { return c.origin() + "/api/organizations" }

// creditsURL is only called with an organization ValidConsoleOrganizationID
// accepted.
func (c ConsoleClient) creditsURL(organization string) string {
	return c.origin() + "/api/organizations/" + organization + "/prepaid/credits"
}

func (c ConsoleClient) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return consoleUserAgent
}

func (c ConsoleClient) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c ConsoleClient) httpClient() *http.Client {
	return Client{HTTPClient: c.HTTPClient}.httpClient()
}

// Fetch reads one observation of the organization's prepaid credits. With
// no OrganizationID the one organization with API access is used.
func (c ConsoleClient) Fetch(ctx context.Context, credential Credential) (FetchedReport, error) {
	key := strings.TrimSpace(credential.SessionKey)
	if key == "" {
		return FetchedReport{}, ErrNoConsoleSessionKey
	}
	if !strings.HasPrefix(key, ConsoleSessionKeyPrefix) || strings.ContainsAny(key, " \t\r\n;") {
		return FetchedReport{}, ErrNotConsoleSessionKey
	}
	organization := strings.ToLower(strings.TrimSpace(credential.OrganizationID))
	if organization != "" && !ValidConsoleOrganizationID(organization) {
		return FetchedReport{}, fmt.Errorf("the Claude Console organization_id must be an organization UUID")
	}
	if organization == "" {
		body, err := c.get(ctx, c.organizationsURL(), key, "organizations")
		if err != nil {
			return FetchedReport{}, err
		}
		orgs, err := ParseConsoleOrganizations(body)
		if err != nil {
			return FetchedReport{}, err
		}
		if organization, err = SelectConsoleOrganization(orgs); err != nil {
			return FetchedReport{}, err
		}
	}
	body, err := c.get(ctx, c.creditsURL(organization), key, "credits")
	if err != nil {
		return FetchedReport{}, err
	}
	now := c.now()
	report, err := ParseConsoleCredits(body, now)
	if err != nil {
		return FetchedReport{}, err
	}
	return FetchedReport{Report: report, FetchedAt: &now}, nil
}

// get sends one Console GET with only the session cookie. The status is
// classified before the body is read, so a truncated error body cannot
// hide a rejected session or a rate limit; no body is ever echoed.
func (c ConsoleClient) get(ctx context.Context, url, key, what string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build Claude Console %s request", what)
	}
	req.Header.Set("Cookie", "sessionKey="+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Claude Console %s request failed: connection failed", what)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrConsoleSessionExpired
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrConsoleOrganizationNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrConsoleRateLimited
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("Claude Console %s request failed: HTTP %d", what, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("Claude Console %s request failed: read failed", what)
	}
	return body, nil
}
