package openusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LimitsSchema is the machine-facing envelope served by OpenUsage's
// GET /v1/limits and GET /v1/limits/{id}.
const LimitsSchema = "openusage.limits.v1"

// LimitsEnvelope is the openusage.limits.v1 response. Optional fields are
// pointers so an absent value stays distinguishable from zero.
type LimitsEnvelope struct {
	Schema      string                    `json:"schema"`
	GeneratedAt string                    `json:"generatedAt"`
	Providers   map[string]LimitsProvider `json:"providers"`
	Errors      []LimitsError             `json:"errors"`
}

type LimitsProvider struct {
	DisplayName string                    `json:"displayName"`
	Plan        string                    `json:"plan"`
	FetchedAt   string                    `json:"fetchedAt"`
	ExpiresAt   string                    `json:"expiresAt"`
	Stale       bool                      `json:"stale"`
	Resources   map[string]LimitsResource `json:"resources"`
}

type LimitsResource struct {
	Kind          string   `json:"kind"`
	Unit          string   `json:"unit"`
	Used          *float64 `json:"used"`
	Available     *float64 `json:"available"`
	Limit         *float64 `json:"limit"`
	Remaining     *float64 `json:"remaining"`
	Utilization   *float64 `json:"utilization"`
	ResetsAt      string   `json:"resetsAt"`
	WindowSeconds *float64 `json:"windowSeconds"`
	ExpiresAt     []string `json:"expiresAt"`
	Estimated     bool     `json:"estimated"`
}

type LimitsError struct {
	ProviderID string `json:"providerId"`
	Message    string `json:"message"`
}

// ErrProviderUnknown means OpenUsage answered 404: it has no provider or
// family with the requested ID.
var ErrProviderUnknown = fmt.Errorf("OpenUsage does not know this provider")

// FetchLimits reads GET /v1/limits/{id}. It never asks OpenUsage to refresh;
// the route serves whatever the app last fetched.
func (c Client) FetchLimits(ctx context.Context, providerID string) (LimitsEnvelope, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		return LimitsEnvelope{}, fmt.Errorf("OpenUsage base URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/limits/"+url.PathEscape(providerID), nil)
	if err != nil {
		return LimitsEnvelope{}, fmt.Errorf("build OpenUsage limits request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return LimitsEnvelope{}, fmt.Errorf("fetch OpenUsage limits: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return LimitsEnvelope{}, fmt.Errorf("read OpenUsage limits: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return LimitsEnvelope{}, ErrProviderUnknown
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return LimitsEnvelope{}, fmt.Errorf("OpenUsage limits returned HTTP %d", resp.StatusCode)
	}
	return ParseLimits(body)
}

// ParseLimits decodes and checks the envelope schema.
func ParseLimits(data []byte) (LimitsEnvelope, error) {
	var envelope LimitsEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return LimitsEnvelope{}, fmt.Errorf("decode OpenUsage limits: %w", err)
	}
	if envelope.Schema != LimitsSchema {
		return LimitsEnvelope{}, fmt.Errorf("OpenUsage limits schema %q is unsupported (want %s)", envelope.Schema, LimitsSchema)
	}
	return envelope, nil
}

// ParseTime parses an OpenUsage ISO 8601 instant.
func ParseTime(value string) (time.Time, error) {
	return parseTime("time", strings.TrimSpace(value))
}
