package openusage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
)

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// Source adapts the OpenUsage loopback API to the shared usage-source contract.
// An empty URL in auto mode probes the conventional local endpoint.
type Source struct{ HTTPClient *http.Client }

func (Source) Name() string { return "openusage" }

func (s Source) Fetch(ctx context.Context, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	baseURL := strings.TrimSpace(provider.OpenUsageURL)
	if baseURL == "" {
		baseURL = "http://127.0.0.1:6736"
	}
	return (Client{BaseURL: baseURL, HTTPClient: s.HTTPClient}).Fetch(ctx, provider.Provider)
}

func (c Client) Fetch(ctx context.Context, provider string) (decision.UsageSnapshot, []byte, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("OpenUsage base URL is required")
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		base+"/v1/usage/"+url.PathEscape(provider),
		nil,
	)
	if err != nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("build OpenUsage request: %w", err)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("fetch OpenUsage: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("read OpenUsage response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decision.UsageSnapshot{}, body, fmt.Errorf("OpenUsage returned HTTP %d", resp.StatusCode)
	}
	snapshot, err := Parse(body, provider)
	if err != nil {
		return decision.UsageSnapshot{}, body, err
	}
	return snapshot, body, nil
}

type providerPayload struct {
	ProviderID string      `json:"providerId"`
	FetchedAt  string      `json:"fetchedAt"`
	Lines      []usageLine `json:"lines"`
}

type usageLine struct {
	Type             string  `json:"type"`
	Label            string  `json:"label"`
	Used             float64 `json:"used"`
	Limit            float64 `json:"limit"`
	ResetsAt         string  `json:"resetsAt"`
	PeriodDurationMS int64   `json:"periodDurationMs"`
}

func Parse(data []byte, provider string) (decision.UsageSnapshot, error) {
	var payloads []providerPayload
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return decision.UsageSnapshot{}, fmt.Errorf("decode OpenUsage response: empty body")
	}
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &payloads); err != nil {
			return decision.UsageSnapshot{}, fmt.Errorf("decode OpenUsage response: %w", err)
		}
	case '{':
		var payload providerPayload
		if err := json.Unmarshal(trimmed, &payload); err != nil {
			return decision.UsageSnapshot{}, fmt.Errorf("decode OpenUsage response: %w", err)
		}
		payloads = []providerPayload{payload}
	default:
		return decision.UsageSnapshot{}, fmt.Errorf("decode OpenUsage response: expected object or array")
	}
	var selected *providerPayload
	for i := range payloads {
		if strings.EqualFold(payloads[i].ProviderID, provider) {
			selected = &payloads[i]
			break
		}
	}
	if selected == nil {
		return decision.UsageSnapshot{}, fmt.Errorf("provider %q not found in OpenUsage response", provider)
	}
	observedAt, err := parseTime("fetchedAt", selected.FetchedAt)
	if err != nil {
		return decision.UsageSnapshot{}, err
	}
	snapshot := decision.UsageSnapshot{
		Provider:   strings.ToLower(selected.ProviderID),
		ObservedAt: observedAt,
		Source:     "openusage",
		Confidence: "high",
	}
	var accountWeeklyReset time.Time
	for _, line := range selected.Lines {
		key, _, _ := normalizeLabel(line.Label)
		if key != "weekly" || strings.TrimSpace(line.ResetsAt) == "" {
			continue
		}
		if reset, parseErr := parseTime("resetsAt", line.ResetsAt); parseErr == nil {
			accountWeeklyReset = reset
		}
		break
	}
	weeklyFound := false
	for _, line := range selected.Lines {
		if !strings.EqualFold(line.Type, "progress") {
			continue
		}
		key, scope, role := normalizeLabel(line.Label)
		if key == "" {
			continue
		}
		remaining, err := remainingFraction(line)
		if err != nil {
			return decision.UsageSnapshot{}, fmt.Errorf("line %q: %w", line.Label, err)
		}
		resetInferred := false
		var reset time.Time
		if strings.TrimSpace(line.ResetsAt) == "" && scope == "model" && role == "weekly" && !accountWeeklyReset.IsZero() {
			reset, resetInferred = accountWeeklyReset, true
			snapshot.Confidence = "medium"
		} else {
			reset, err = parseTime("resetsAt", line.ResetsAt)
			if err != nil {
				return decision.UsageSnapshot{}, fmt.Errorf("line %q: %w", line.Label, err)
			}
		}
		periodSeconds := line.PeriodDurationMS / 1000
		if periodSeconds == 0 {
			if role == "short" {
				periodSeconds = int64(decision.ShortWindowDuration / time.Second)
			} else {
				periodSeconds = int64((7 * 24 * time.Hour) / time.Second)
			}
		}
		allowance := decision.AllowanceWindow{Key: key, SourceLabel: line.Label, Scope: scope, Role: role,
			Remaining: remaining, ResetsAt: reset, PeriodDurationSeconds: periodSeconds, ResetInferred: resetInferred}
		snapshot.Allowances = append(snapshot.Allowances, allowance)
		switch key {
		case "session":
			snapshot.Short = &decision.UsageWindow{Remaining: remaining, ResetsAt: reset}
		case "weekly":
			snapshot.Weekly = decision.UsageWindow{Remaining: remaining, ResetsAt: reset}
			weeklyFound = true
		}
	}
	if !weeklyFound {
		return decision.UsageSnapshot{}, fmt.Errorf(
			"provider %q is missing required weekly usage window",
			provider,
		)
	}
	if err := snapshot.Validate(); err != nil {
		return decision.UsageSnapshot{}, fmt.Errorf("normalize OpenUsage snapshot: %w", err)
	}
	return snapshot, nil
}

func normalizeLabel(label string) (key, scope, role string) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "session", "5-hour", "5 hour", "five-hour", "five hour":
		return "session", "account", "short"
	case "weekly", "7-day", "7 day", "seven-day", "seven day":
		return "weekly", "account", "weekly"
	case "fable":
		return "model:fable:weekly", "model", "weekly"
	default:
		return "", "", ""
	}
}

func remainingFraction(line usageLine) (float64, error) {
	if line.Limit <= 0 {
		return 0, fmt.Errorf("limit must be greater than zero")
	}
	if line.Used < 0 || line.Used > line.Limit {
		return 0, fmt.Errorf("used must be between zero and limit")
	}
	return 1 - line.Used/line.Limit, nil
}

func parseTime(name, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}
