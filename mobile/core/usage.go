// Package core is the shared client core for the Redline mobile apps.
//
// It is compiled with gomobile and bound into Android and iOS, so every
// exported symbol must use types gomobile can bind: strings, numbers, bools,
// errors, and pointers to structs with those. Rich values cross the boundary as
// JSON strings, which is why the fetch methods return JSON rather than structs.
//
// Interpretation lives here rather than in the UI so the window arithmetic,
// staleness rules, and pool de-duplication are written and tested once instead
// of once per platform.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/jfox/redline/internal/decision"
)

// Window is one usage allowance rendered for display.
type Window struct {
	// RemainingPercent is 0-100, rounded, so the UI does not repeat the
	// conversion and risk disagreeing with itself.
	RemainingPercent int `json:"remaining_percent"`
	// ResetsInSeconds counts down to the reset and never goes negative.
	ResetsInSeconds int64     `json:"resets_in_seconds"`
	ResetsAt        time.Time `json:"resets_at"`
	// ResetInferred marks a reset the collector guessed rather than read.
	// Notification scheduling must skip these.
	ResetInferred bool `json:"reset_inferred,omitempty"`
}

// Pool is a non-canonical allowance, such as a model-scoped quota.
type Pool struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Scope string `json:"scope"`
	Window
}

// ProviderUsage is one provider's capacity as the app renders it.
type ProviderUsage struct {
	ID       string  `json:"id"`
	Provider string  `json:"provider"`
	Paused   bool    `json:"paused"`
	Stale    bool    `json:"stale"`
	Error    string  `json:"error,omitempty"`
	Session  *Window `json:"session,omitempty"`
	Weekly   *Window `json:"weekly,omitempty"`
	Pools    []Pool  `json:"pools,omitempty"`
}

// UsageView is the whole capacity screen.
type UsageView struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Providers   []ProviderUsage `json:"providers"`
}

// canonicalPoolKeys are the account pools the collectors also publish as the
// snapshot's short and weekly windows. They are rendered as dedicated windows,
// so repeating them as pools would duplicate every meter.
var canonicalPoolKeys = map[string]bool{"session": true, "weekly": true}

// isCanonicalPool reports whether an allowance is already shown as a dedicated
// window.
//
// Collectors emit the keys "session" and "weekly" today, but the domain model
// permits any key, so matching the semantics as well as the literal keys keeps
// a renamed account window from reappearing as a duplicate meter. Model-scoped
// pools such as model:fable:weekly are never account-scoped and so are never
// hidden by this.
func isCanonicalPool(allowance decision.AllowanceWindow) bool {
	if canonicalPoolKeys[allowance.Key] {
		return true
	}
	return allowance.Scope == "account" && (allowance.Role == "short" || allowance.Role == "weekly")
}

// dashboardPayload mirrors only the parts of GET /v1/dashboard the apps need.
// It deliberately does not embed the server's dashboardResponse, which is
// unexported and carries scheduler and task detail the usage screen ignores.
type dashboardPayload struct {
	GeneratedAt time.Time `json:"generated_at"`
	Providers   []struct {
		ID            string                  `json:"id"`
		Provider      string                  `json:"provider"`
		Paused        bool                    `json:"paused"`
		SnapshotStale bool                    `json:"snapshot_stale"`
		Error         string                  `json:"error,omitempty"`
		Snapshot      *decision.UsageSnapshot `json:"snapshot,omitempty"`
	} `json:"providers"`
}

// FetchUsage returns the capacity screen as JSON.
func (c *Client) FetchUsage() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var payload dashboardPayload
	if err := c.get(ctx, "/v1/dashboard", &payload); err != nil {
		return "", err
	}

	now := c.now()
	view := UsageView{GeneratedAt: payload.GeneratedAt, Providers: make([]ProviderUsage, 0, len(payload.Providers))}
	for _, item := range payload.Providers {
		provider := ProviderUsage{
			ID:       item.ID,
			Provider: item.Provider,
			Paused:   item.Paused,
			Stale:    item.SnapshotStale,
			Error:    item.Error,
		}
		if item.Snapshot != nil {
			provider.Session = shortWindow(item.Snapshot, now)
			provider.Weekly = weeklyWindow(item.Snapshot, now)
			provider.Pools = pools(item.Snapshot, now)
		}
		view.Providers = append(view.Providers, provider)
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("encode usage view: %w", err)
	}
	return string(encoded), nil
}

// shortWindow prefers the snapshot's short window and falls back to the
// canonical session allowance, since not every collector populates both.
//
// A window counts as usable only when it carries a reset time. Both windows
// apply that same test: gating short on `Short != nil` alone would accept a
// non-nil but empty window and never consult the allowance, which is the
// asymmetry that made the two paths behave differently.
func shortWindow(snapshot *decision.UsageSnapshot, now time.Time) *Window {
	if snapshot.Short != nil && !snapshot.Short.ResetsAt.IsZero() {
		return newWindow(snapshot.Short.Remaining, snapshot.Short.ResetsAt, false, now)
	}
	return allowanceWindow(snapshot, "session", now)
}

func weeklyWindow(snapshot *decision.UsageSnapshot, now time.Time) *Window {
	if !snapshot.Weekly.ResetsAt.IsZero() {
		return newWindow(snapshot.Weekly.Remaining, snapshot.Weekly.ResetsAt, false, now)
	}
	return allowanceWindow(snapshot, "weekly", now)
}

// allowanceWindow finds a canonical account allowance by key.
func allowanceWindow(snapshot *decision.UsageSnapshot, key string, now time.Time) *Window {
	for _, allowance := range snapshot.Allowances {
		if allowance.Key == key {
			return newWindow(allowance.Remaining, allowance.ResetsAt, allowance.ResetInferred, now)
		}
	}
	return nil
}

// pools returns every allowance that is not already shown as a dedicated
// window.
func pools(snapshot *decision.UsageSnapshot, now time.Time) []Pool {
	var result []Pool
	for _, allowance := range snapshot.Allowances {
		if isCanonicalPool(allowance) {
			continue
		}
		label := allowance.SourceLabel
		if label == "" {
			label = allowance.Key
		}
		result = append(result, Pool{
			Key:    allowance.Key,
			Label:  label,
			Scope:  allowance.Scope,
			Window: *newWindow(allowance.Remaining, allowance.ResetsAt, allowance.ResetInferred, now),
		})
	}
	return result
}

func newWindow(remaining float64, resetsAt time.Time, inferred bool, now time.Time) *Window {
	return &Window{
		RemainingPercent: percent(remaining),
		ResetsInSeconds:  secondsUntil(resetsAt, now),
		ResetsAt:         resetsAt,
		ResetInferred:    inferred,
	}
}

// percent clamps to 0-100 so a provider reporting a value outside that range
// cannot produce a nonsensical meter.
//
// Rounding matches the web dashboard exactly (`Math.round`, clamped), so the
// two surfaces never disagree about the same snapshot. That means a nearly-full
// window can read 100%, which is deliberate: notifications key off resets_at
// and the policy engine's own pressure model, never off this display integer.
func percent(remaining float64) int {
	if math.IsNaN(remaining) {
		return 0
	}
	value := int(math.Round(remaining * 100))
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// secondsUntil never returns a negative countdown: a window whose reset has
// passed reads as zero rather than counting up.
func secondsUntil(target, now time.Time) int64 {
	if target.IsZero() {
		return 0
	}
	remaining := target.Sub(now)
	if remaining < 0 {
		return 0
	}
	return int64(remaining / time.Second)
}

// ErrUnauthorized marks a rejected credential, so the UI can prompt to pair
// again instead of showing a generic failure.
var ErrUnauthorized = errors.New("redline authentication is required")

// IsUnauthorized reports whether an error came from a rejected credential.
func IsUnauthorized(err error) bool { return errors.Is(err, ErrUnauthorized) }

type apiError struct {
	StatusCode int
	Message    string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("redline api: %d %s", e.StatusCode, e.Message)
}

func (e *apiError) Unwrap() error {
	if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	return nil
}
