// Package nativeusage reads subscription allowance windows directly from provider APIs.
package nativeusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
)

const (
	defaultClaudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	defaultCodexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"

	// Anthropic only includes banked limit resets (the "cedar_ember" program,
	// Claude Code's /limit-reset) when asked with these query parameters and a
	// Claude Code CLI user agent; any other agent is told it is ineligible
	// with reason "surface". skip_spend drops the spend block we do not read.
	claudeBankedResetsQuery = "cedar_ember=1&skip_spend=1"
	claudeUserAgent         = "claude-cli/2.1.280 (external, cli)"
)

type Credential struct {
	AccessToken string
	AccountID   string
}

type Credentials interface {
	Access(context.Context, string) (Credential, error)
}

// ReadOnlyCredentials can hand out a credential without refreshing it.
type ReadOnlyCredentials interface {
	AccessWithoutRefresh(context.Context, string) (Credential, error)
}

type Client struct {
	HTTPClient     *http.Client
	Credentials    Credentials
	ClaudeUsageURL string
	CodexUsageURL  string
	Now            func() time.Time
}

func (c Client) Name() string { return "native" }

func (c Client) Fetch(ctx context.Context, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	body, err := c.fetchBody(ctx, name, false)
	if err != nil {
		return decision.UsageSnapshot{}, body, err
	}
	now := c.now()
	var snapshot decision.UsageSnapshot
	if name == "claude" {
		snapshot, err = parseClaude(body, now)
	} else {
		snapshot, err = parseCodex(body, now)
	}
	if err != nil {
		return decision.UsageSnapshot{}, body, err
	}
	return snapshot, body, nil
}

// BankedResets reads only the banked reset report, for supplementing a
// snapshot that came from a source which does not carry resets. A nil report
// with a nil error means the provider answered but reported no resets data
// (for example, the account is not in Anthropic's program).
func (c Client) BankedResets(ctx context.Context, provider config.Provider) (*decision.BankedResets, error) {
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	if name != "claude" {
		return nil, fmt.Errorf("native banked resets for %q are unsupported", name)
	}
	// Supplementary: never refresh Claude Code's shared token for this.
	body, err := c.fetchBody(ctx, name, true)
	if err != nil {
		return nil, err
	}
	var payload struct {
		CedarEmber *claudeResetStatus `json:"cedar_ember"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode native claude banked resets: %w", err)
	}
	return claudeBankedResets(payload.CedarEmber, c.now()), nil
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c Client) fetchBody(ctx context.Context, name string, readOnly bool) ([]byte, error) {
	if c.Credentials == nil {
		return nil, fmt.Errorf("native credentials are unavailable")
	}
	var credential Credential
	var err error
	if readOnly {
		reader, ok := c.Credentials.(ReadOnlyCredentials)
		if !ok {
			return nil, fmt.Errorf("native %s credentials cannot be read without refreshing", name)
		}
		credential, err = reader.AccessWithoutRefresh(ctx, name)
	} else {
		credential, err = c.Credentials.Access(ctx, name)
	}
	if err != nil {
		return nil, err
	}
	if credential.AccessToken == "" {
		return nil, fmt.Errorf("%s access token is unavailable", name)
	}
	url := c.CodexUsageURL
	if url == "" {
		url = defaultCodexUsageURL
	}
	if name == "claude" {
		url = c.ClaudeUsageURL
		if url == "" {
			url = defaultClaudeUsageURL
		}
		url = withQuery(url, claudeBankedResetsQuery)
	} else if name != "codex" {
		return nil, fmt.Errorf("native provider %q is unsupported", name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build native %s usage request: %w", name, err)
	}
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	req.Header.Set("Accept", "application/json")
	if name == "claude" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		req.Header.Set("User-Agent", claudeUserAgent)
	} else {
		req.Header.Set("User-Agent", "Redline")
		if credential.AccountID != "" {
			req.Header.Set("ChatGPT-Account-Id", credential.AccountID)
		}
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch native %s usage: %w", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read native %s usage: %w", name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, &HTTPError{Provider: name, StatusCode: resp.StatusCode,
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), c.now())}
	}
	return body, nil
}

// HTTPError is a non-2xx answer from a provider usage endpoint.
type HTTPError struct {
	Provider   string
	StatusCode int
	retryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("native %s usage returned HTTP %d", e.Provider, e.StatusCode)
}

// RetryAfter is how long the provider asked us to wait, or zero if it did not
// say. Asking again sooner only extends a rate limit.
func (e *HTTPError) RetryAfter() time.Duration { return e.retryAfter }

// RateLimited reports whether the provider refused the request for rate.
func (e *HTTPError) RateLimited() bool { return e.StatusCode == http.StatusTooManyRequests }

// parseRetryAfter accepts both forms RFC 9110 allows: delay-seconds and an
// HTTP-date. Anthropic's usage endpoint is known to answer "retry-after: 0"
// while still limiting; zero is returned as-is and callers apply their own
// backoff floor.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

type claudeWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}
type claudeLimit struct {
	Kind     string  `json:"kind"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resets_at"`
	Scope    struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}
type claudePayload struct {
	FiveHour   *claudeWindow      `json:"five_hour"`
	SevenDay   *claudeWindow      `json:"seven_day"`
	Limits     []claudeLimit      `json:"limits"`
	CedarEmber *claudeResetStatus `json:"cedar_ember"`
}

// claudeResetStatus is the banked limit reset block. Field names follow
// Claude Code 2.1.280's own schema for this undocumented response.
type claudeResetStatus struct {
	Eligible bool               `json:"eligible"`
	Grants   []claudeResetGrant `json:"grants"`
}

type claudeResetGrant struct {
	ID         string `json:"id"`
	ResetsLeft *int   `json:"resets_left"`
	EndsAt     string `json:"ends_at"`
}

// claudeBankedResets totals the resets left across grants the way Claude
// Code does, and reports the soonest expiry among grants that still hold one.
//
// Absent block, ineligible account, or a malformed grant set all report
// nothing rather than a guess: the account may not be in the program, or the
// shape may have changed, and neither means "zero".
func claudeBankedResets(status *claudeResetStatus, now time.Time) *decision.BankedResets {
	if status == nil || !status.Eligible {
		return nil
	}
	resets := decision.BankedResets{}
	for _, grant := range status.Grants {
		if grant.ResetsLeft == nil || *grant.ResetsLeft < 0 {
			return nil
		}
		if *grant.ResetsLeft == 0 {
			continue
		}
		var expires time.Time
		if text := strings.TrimSpace(grant.EndsAt); text != "" {
			parsed, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return nil
			}
			if !parsed.After(now) {
				// Expired grants can linger briefly; they are not spendable.
				continue
			}
			expires = parsed
		}
		resets.Available += *grant.ResetsLeft
		if !expires.IsZero() && (resets.NextExpiresAt == nil || expires.Before(*resets.NextExpiresAt)) {
			resets.NextExpiresAt = &expires
		}
	}
	return &resets
}

func withQuery(rawURL, query string) string {
	if strings.Contains(rawURL, "?") {
		return rawURL + "&" + query
	}
	return rawURL + "?" + query
}

func parseClaude(body []byte, now time.Time) (decision.UsageSnapshot, error) {
	var payload claudePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return decision.UsageSnapshot{}, fmt.Errorf("decode native claude usage: %w", err)
	}
	snapshot := decision.UsageSnapshot{Provider: "claude", ObservedAt: now, Source: "native", Confidence: "high"}
	if payload.FiveHour != nil {
		window, allowance, err := normalizedWindow("session", "Session", "short", payload.FiveHour.Utilization, payload.FiveHour.ResetsAt, 5*time.Hour)
		if err != nil {
			return decision.UsageSnapshot{}, err
		}
		snapshot.Short = &window
		snapshot.Allowances = append(snapshot.Allowances, allowance)
	}
	if payload.SevenDay == nil {
		return decision.UsageSnapshot{}, fmt.Errorf("native claude usage is missing weekly window")
	}
	weekly, allowance, err := normalizedWindow("weekly", "Weekly", "weekly", payload.SevenDay.Utilization, payload.SevenDay.ResetsAt, 7*24*time.Hour)
	if err != nil {
		return decision.UsageSnapshot{}, err
	}
	snapshot.Weekly = weekly
	snapshot.Allowances = append(snapshot.Allowances, allowance)
	for _, limit := range payload.Limits {
		if limit.Kind != "weekly_scoped" || !strings.EqualFold(limit.Scope.Model.DisplayName, "Fable") {
			continue
		}
		resetText := limit.ResetsAt
		resetInferred := false
		if strings.TrimSpace(resetText) == "" {
			resetText = weekly.ResetsAt.Format(time.RFC3339Nano)
			resetInferred = true
			snapshot.Confidence = "medium"
		}
		_, fable, err := normalizedWindow("model:fable:weekly", "Fable", "weekly", limit.Percent, resetText, 7*24*time.Hour)
		if err != nil {
			return decision.UsageSnapshot{}, err
		}
		fable.Scope = "model"
		fable.ResetInferred = resetInferred
		snapshot.Allowances = append(snapshot.Allowances, fable)
		break
	}
	snapshot.ApplyBankedResets(claudeBankedResets(payload.CedarEmber, now))
	if err := snapshot.Validate(); err != nil {
		return decision.UsageSnapshot{}, fmt.Errorf("normalize native claude snapshot: %w", err)
	}
	return snapshot, nil
}

type codexWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAt            float64 `json:"reset_at"`
	ResetAfterSeconds  float64 `json:"reset_after_seconds"`
}
type codexPayload struct {
	RateLimit struct {
		Primary   *codexWindow `json:"primary_window"`
		Secondary *codexWindow `json:"secondary_window"`
	} `json:"rate_limit"`
	ResetCredits *struct {
		AvailableCount *int `json:"available_count"`
	} `json:"rate_limit_reset_credits"`
}

func parseCodex(body []byte, now time.Time) (decision.UsageSnapshot, error) {
	var payload codexPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return decision.UsageSnapshot{}, fmt.Errorf("decode native codex usage: %w", err)
	}
	snapshot := decision.UsageSnapshot{Provider: "codex", ObservedAt: now, Source: "native", Confidence: "high"}
	for _, window := range []*codexWindow{payload.RateLimit.Primary, payload.RateLimit.Secondary} {
		if window == nil {
			continue
		}
		reset := time.Unix(int64(window.ResetAt), 0).UTC()
		if window.ResetAt == 0 && window.ResetAfterSeconds > 0 {
			reset = now.Add(time.Duration(window.ResetAfterSeconds * float64(time.Second)))
		}
		if window.LimitWindowSeconds == int64((5*time.Hour)/time.Second) {
			remaining, err := remaining(window.UsedPercent)
			if err != nil {
				return decision.UsageSnapshot{}, err
			}
			snapshot.Short = &decision.UsageWindow{Remaining: remaining, ResetsAt: reset}
			snapshot.Allowances = append(snapshot.Allowances, decision.AllowanceWindow{Key: "session", SourceLabel: "Session", Scope: "account", Role: "short", Remaining: remaining, ResetsAt: reset, PeriodDurationSeconds: window.LimitWindowSeconds})
		} else if window.LimitWindowSeconds == int64((7*24*time.Hour)/time.Second) {
			remaining, err := remaining(window.UsedPercent)
			if err != nil {
				return decision.UsageSnapshot{}, err
			}
			snapshot.Weekly = decision.UsageWindow{Remaining: remaining, ResetsAt: reset}
			snapshot.Allowances = append(snapshot.Allowances, decision.AllowanceWindow{Key: "weekly", SourceLabel: "Weekly", Scope: "account", Role: "weekly", Remaining: remaining, ResetsAt: reset, PeriodDurationSeconds: window.LimitWindowSeconds})
		}
	}
	if snapshot.Weekly.ResetsAt.IsZero() {
		return decision.UsageSnapshot{}, fmt.Errorf("native codex usage is missing weekly window")
	}
	// The usage endpoint carries the count but not the expiry; that lives on a
	// separate credits endpoint we do not call on every refresh.
	if credits := payload.ResetCredits; credits != nil && credits.AvailableCount != nil && *credits.AvailableCount >= 0 {
		snapshot.ApplyBankedResets(&decision.BankedResets{Available: *credits.AvailableCount})
	}
	if err := snapshot.Validate(); err != nil {
		return decision.UsageSnapshot{}, fmt.Errorf("normalize native codex snapshot: %w", err)
	}
	return snapshot, nil
}

func normalizedWindow(key, label, role string, used float64, resetText string, period time.Duration) (decision.UsageWindow, decision.AllowanceWindow, error) {
	remaining, err := remaining(used)
	if err != nil {
		return decision.UsageWindow{}, decision.AllowanceWindow{}, fmt.Errorf("%s utilization: %w", label, err)
	}
	reset, err := time.Parse(time.RFC3339Nano, resetText)
	if err != nil {
		return decision.UsageWindow{}, decision.AllowanceWindow{}, fmt.Errorf("parse %s reset: %w", label, err)
	}
	window := decision.UsageWindow{Remaining: remaining, ResetsAt: reset}
	allowance := decision.AllowanceWindow{Key: key, SourceLabel: label, Scope: "account", Role: role, Remaining: remaining, ResetsAt: reset, PeriodDurationSeconds: int64(period / time.Second)}
	return window, allowance, nil
}

func remaining(used float64) (float64, error) {
	if used < 0 || used > 100 {
		return 0, fmt.Errorf("used percent must be between zero and 100")
	}
	return 1 - used/100, nil
}
