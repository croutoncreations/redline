// Package nativeusage reads subscription allowance windows directly from provider APIs.
package nativeusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
)

const (
	defaultClaudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	defaultCodexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
)

type Credential struct {
	AccessToken string
	AccountID   string
}

type Credentials interface {
	Access(context.Context, string) (Credential, error)
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
	if c.Credentials == nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("native credentials are unavailable")
	}
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	credential, err := c.Credentials.Access(ctx, name)
	if err != nil {
		return decision.UsageSnapshot{}, nil, err
	}
	if credential.AccessToken == "" {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("%s access token is unavailable", name)
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
	} else if name != "codex" {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("native provider %q is unsupported", name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("build native %s usage request: %w", name, err)
	}
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	req.Header.Set("Accept", "application/json")
	if name == "claude" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		req.Header.Set("User-Agent", "claude-code/2.1.69")
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
		return decision.UsageSnapshot{}, nil, fmt.Errorf("fetch native %s usage: %w", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("read native %s usage: %w", name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decision.UsageSnapshot{}, body, fmt.Errorf("native %s usage returned HTTP %d", name, resp.StatusCode)
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
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
	FiveHour *claudeWindow `json:"five_hour"`
	SevenDay *claudeWindow `json:"seven_day"`
	Limits   []claudeLimit `json:"limits"`
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
