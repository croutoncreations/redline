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
	"strings"
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
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Paused   bool   `json:"paused"`
	Stale    bool   `json:"stale"`
	Error    string `json:"error,omitempty"`
	// SourceLabel says where the numbers came from and how fresh they are,
	// which is what to check first when two surfaces disagree.
	SourceLabel string  `json:"source_label,omitempty"`
	Session     *Window `json:"session,omitempty"`
	// SessionUnknown marks a five hour window that exists but could not be
	// read. OpenUsage reports it without a reset time while provider state is
	// refreshing, and the collector drops it rather than inventing one, so the
	// screen has to distinguish "this provider has no such limit" from "this
	// limit exists and the number is missing right now". Omitting the row for
	// both looks like the limit does not exist, which is the wrong thing to
	// tell someone deciding whether to start a run.
	SessionUnknown bool `json:"session_unknown,omitempty"`
	// BankedResets counts quota resets the account can spend on demand.
	// Absent when the provider does not report them, which is not the same as
	// having none.
	BankedResets *int    `json:"banked_resets,omitempty"`
	Weekly       *Window `json:"weekly,omitempty"`
	Pools        []Pool  `json:"pools,omitempty"`
}

// UsageView is the whole capacity screen.
type UsageView struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Providers   []ProviderUsage `json:"providers"`
	// Health rides along because the same payload already carries it and the
	// header shows a health pill; fetching it separately would ask the desktop
	// twice for something it already sent.
	Health *HealthView `json:"health,omitempty"`
	// Relayed says this particular response crossed the relay rather than the
	// tailnet, so the screen can report the route it actually used.
	//
	// Carried in the payload rather than read back from the client afterwards.
	// Three view models share one client, so a flag on the client is
	// last-write-wins: a Runs refresh going direct would clear what a Usage
	// refresh had just set, and the pill would claim the tailnet over data
	// that crossed the paid relay. This is the same defect as the status race,
	// one layer up, and the same cure -- travel with the answer.
	Relayed bool `json:"relayed,omitempty"`
}

// sourceLabel describes where a provider's numbers came from, how many runs it
// is carrying, and how recently it was sampled.
//
// This mirrors the line the web dashboard shows. When the phone and the
// dashboard disagree, the first question is which snapshot each was looking at,
// and this is the answer.
func sourceLabel(usageSource, snapshotSource string, active, maximum int, observedAt, now time.Time) string {
	source := usageSource
	if source == "" {
		source = snapshotSource
	}

	parts := make([]string, 0, 3)
	if source != "" {
		parts = append(parts, source+" source")
	}
	if maximum > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d active", active, maximum))
	}
	if !observedAt.IsZero() {
		parts = append(parts, "sampled "+relativeLabel(observedAt, now))
	}
	return strings.Join(parts, " \u00b7 ")
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
	GeneratedAt time.Time      `json:"generated_at"`
	Health      *healthPayload `json:"health,omitempty"`
	Providers   []struct {
		ID            string `json:"id"`
		Provider      string `json:"provider"`
		Paused        bool   `json:"paused"`
		SnapshotStale bool   `json:"snapshot_stale"`
		Error         string `json:"error,omitempty"`
		// usage_source is an object describing the collector in use, not a
		// bare name: it also records when the collector last changed and how
		// many times it has failed in a row.
		UsageSource struct {
			Active string `json:"active"`
		} `json:"usage_source"`
		ActiveRuns        int                     `json:"active_runs"`
		MaxConcurrentRuns int                     `json:"max_concurrent_runs"`
		Snapshot          *decision.UsageSnapshot `json:"snapshot,omitempty"`
	} `json:"providers"`
}

// renderUsage turns a dashboard payload into the capacity screen.
//
// Shared by the polling and streaming paths so a live update and a manual
// refresh cannot render differently.
func renderUsage(payload dashboardPayload, now time.Time) UsageView {
	view := UsageView{
		GeneratedAt: payload.GeneratedAt,
		Providers:   make([]ProviderUsage, 0, len(payload.Providers)),
	}
	if payload.Health != nil {
		health := payload.Health.render()
		view.Health = &health
	}
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
			// A provider that reports a short window at all is expected to keep
			// reporting one, so its absence here is a gap rather than a
			// permanent property. Claude sends one and Codex never does, and
			// the difference is what stops Codex growing a phantom row.
			provider.SessionUnknown = provider.Session == nil &&
				providesShortWindow(item.Snapshot)
			provider.BankedResets = item.Snapshot.BankedResets
			provider.Weekly = weeklyWindow(item.Snapshot, now)
			provider.Pools = pools(item.Snapshot, now)
			provider.SourceLabel = sourceLabel(
				item.UsageSource.Active, item.Snapshot.Source,
				item.ActiveRuns, item.MaxConcurrentRuns,
				item.Snapshot.ObservedAt, now,
			)
		}
		view.Providers = append(view.Providers, provider)
	}
	return view
}

// FetchUsage returns the capacity screen as JSON.
func (c *Client) FetchUsage() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// The route is recorded against this request, so the answer reports the
	// way it actually travelled rather than whatever another screen's refresh
	// did most recently.
	ctx, route := withRoute(ctx)

	var payload dashboardPayload
	// The read model also carries every run, task, and dispatch attempt, which
	// is roughly 97% of its weight and none of what this screen renders. Asking
	// for the two members it uses keeps a refresh to a few kilobytes, which
	// matters on mobile data and matters more through a relay.
	if err := c.get(ctx, "/v1/dashboard?fields=providers,health", &payload); err != nil {
		return "", err
	}

	view := renderUsage(payload, c.now())
	view.Relayed = route.relayed
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

// providesShortWindow reports whether this provider is one that has a five
// hour window at all.
//
// The distinction matters because the row must appear as "unknown" for a
// provider whose window is temporarily unreadable, and must not appear at all
// for one that has no such limit. Claude has a five hour window; Codex has
// only a weekly allowance, and a phantom "unknown" row on Codex would be its
// own kind of wrong.
//
// The desktop says so directly: ShortWindowUnavailable is set by the collector
// at the one point that knows a window was offered and refused.
//
// Confidence is deliberately not used, though it is tempting. The collector
// does drop to "medium" when it skips a short window, but it also drops to
// "medium" when a model weekly reset is inferred, and keying off it gave Codex
// -- which has no five hour window at all -- a phantom "unknown" row.
//
// An older desktop sends neither field, so this reads false and the row is
// simply absent: the behaviour before this existed, rather than a wrong claim.
func providesShortWindow(snapshot *decision.UsageSnapshot) bool {
	if snapshot.ShortWindowUnavailable {
		return true
	}
	if snapshot.Short != nil {
		return true
	}
	_, ok := snapshot.Allowance("session")
	return ok
}
