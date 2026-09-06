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
	"sort"
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
	// ElapsedPercent is how far through the window now is, 0-100. It is what
	// makes RemainingPercent readable: half left is on pace at the midpoint,
	// comfortable near the end, and a problem near the start. The UI draws it
	// as a mark on the bar. Computed here, like the countdown, so both
	// platforms put the mark in the same place for the same snapshot.
	ElapsedPercent int `json:"elapsed_percent"`
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
	// Scheduling is where Redline will act, drawn on the meters. Nil from a
	// desktop that does not send its policy, in which case no zones are drawn
	// rather than guessed.
	Scheduling *SchedulingView `json:"scheduling,omitempty"`
}

// SchedulingView is the scheduler's policy for one provider, interpreted for
// the meters. The bars show what is left; this says where Redline draws its
// own lines on them, and what it last decided.
type SchedulingView struct {
	// ReservePercent is the floor on the 5-hour bar: below it Redline never
	// dispatches. That much of every window is kept for the person.
	ReservePercent int `json:"reserve_percent"`
	// WeeklyFloors are lines on the weekly bar, each live only once the reset
	// is within its time window. Ordered by when they arm, soonest first.
	WeeklyFloors []WeeklyFloor `json:"weekly_floors,omitempty"`
	// Decision and Reason are the scheduler's last verdict for this provider,
	// so a screen showing the lines can also say which side of them it is on.
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// ProjectedTriggerAt is when flat usage would first qualify to run, or
	// zero when it never does before the reset.
	ProjectedTriggerAt time.Time `json:"projected_trigger_at,omitempty"`
}

// WeeklyFloor is one pace threshold: once the reset is ArmsInSeconds away or
// less, the scheduler dispatches while the weekly bar is at or above Percent.
type WeeklyFloor struct {
	Percent int `json:"percent"`
	// ArmsInSeconds is how long until this floor is live, zero when it is.
	ArmsInSeconds int64 `json:"arms_in_seconds"`
	Armed         bool  `json:"armed"`
}

// UsageView is the whole capacity screen.
type UsageView struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Providers   []ProviderUsage `json:"providers"`
	// Health rides along because the same payload already carries it and the
	// header shows a health pill; fetching it separately would ask the desktop
	// twice for something it already sent.
	Health *HealthView `json:"health,omitempty"`
	// SampledAgeSeconds is how old the oldest provider's numbers are.
	//
	// The stream pushes every five seconds while the collector polls the
	// provider every five minutes, so a connected stream sat above numbers
	// that were minutes old and the header said only "live". Both facts were
	// true and the screen showed one of them.
	//
	// The oldest rather than the freshest, because the header speaks for the
	// whole screen and claiming the freshest would overstate the rest.
	// Seconds rather than a phrase, so the wording stays with the UI.
	SampledAgeSeconds int `json:"sampled_age_seconds,omitempty"`
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
		Scheduling        *struct {
			RollingReserve float64 `json:"rolling_reserve"`
			PaceThresholds []struct {
				TimeRemainingSeconds int64   `json:"time_remaining_seconds"`
				MinWeeklyRemaining   float64 `json:"min_weekly_remaining"`
			} `json:"pace_thresholds"`
		} `json:"scheduling,omitempty"`
		LatestDecision *struct {
			Decision           string    `json:"decision"`
			Reason             string    `json:"reason"`
			ProjectedTriggerAt time.Time `json:"projected_trigger_at"`
		} `json:"latest_decision,omitempty"`
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
			// The oldest sample across providers, so the header can say how
			// stale the numbers are rather than only that the stream is up.
			if observed := item.Snapshot.ObservedAt; !observed.IsZero() {
				if age := int(now.Sub(observed).Seconds()); age > view.SampledAgeSeconds {
					view.SampledAgeSeconds = age
				}
			}
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
		if item.Scheduling != nil {
			scheduling := &SchedulingView{ReservePercent: percent(item.Scheduling.RollingReserve)}
			if provider.Weekly != nil {
				for _, threshold := range item.Scheduling.PaceThresholds {
					// The floor arms when the time left in the week falls to
					// the threshold's window. Time left is the countdown the
					// meter already shows, so the two agree by construction.
					armsIn := provider.Weekly.ResetsInSeconds - threshold.TimeRemainingSeconds
					if armsIn < 0 {
						armsIn = 0
					}
					scheduling.WeeklyFloors = append(scheduling.WeeklyFloors, WeeklyFloor{
						Percent:       percent(threshold.MinWeeklyRemaining),
						ArmsInSeconds: armsIn,
						Armed:         armsIn == 0,
					})
				}
				sort.Slice(scheduling.WeeklyFloors, func(i, j int) bool {
					return scheduling.WeeklyFloors[i].ArmsInSeconds < scheduling.WeeklyFloors[j].ArmsInSeconds
				})
			}
			if item.LatestDecision != nil {
				scheduling.Decision = item.LatestDecision.Decision
				scheduling.Reason = item.LatestDecision.Reason
				scheduling.ProjectedTriggerAt = item.LatestDecision.ProjectedTriggerAt
			}
			provider.Scheduling = scheduling
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
		// The canonical short window has no period of its own on the wire; it
		// is the five-hour window by definition.
		return newWindow(snapshot.Short.Remaining, snapshot.Short.ResetsAt, decision.ShortWindowDuration, false, now)
	}
	return allowanceWindow(snapshot, "session", now)
}

func weeklyWindow(snapshot *decision.UsageSnapshot, now time.Time) *Window {
	if !snapshot.Weekly.ResetsAt.IsZero() {
		return newWindow(snapshot.Weekly.Remaining, snapshot.Weekly.ResetsAt, weeklyPeriod, false, now)
	}
	return allowanceWindow(snapshot, "weekly", now)
}

// weeklyPeriod is the length of the canonical weekly window, which the wire
// format states only for allowances.
const weeklyPeriod = 7 * 24 * time.Hour

// allowanceWindow finds a canonical account allowance by key.
func allowanceWindow(snapshot *decision.UsageSnapshot, key string, now time.Time) *Window {
	for _, allowance := range snapshot.Allowances {
		if allowance.Key == key {
			return newWindow(allowance.Remaining, allowance.ResetsAt, allowancePeriod(allowance), allowance.ResetInferred, now)
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
			Window: *newWindow(allowance.Remaining, allowance.ResetsAt, allowancePeriod(allowance), allowance.ResetInferred, now),
		})
	}
	return result
}

// allowancePeriod is the window length an allowance states for itself, or the
// canonical length for its role when it states none. Every allowance the
// desktop validates has a period; the fallback is for one that arrived from
// an older or looser source.
func allowancePeriod(allowance decision.AllowanceWindow) time.Duration {
	if allowance.PeriodDurationSeconds > 0 {
		return time.Duration(allowance.PeriodDurationSeconds) * time.Second
	}
	if allowance.Role == "weekly" {
		return weeklyPeriod
	}
	return decision.ShortWindowDuration
}

func newWindow(remaining float64, resetsAt time.Time, period time.Duration, inferred bool, now time.Time) *Window {
	return &Window{
		RemainingPercent: percent(remaining),
		ResetsInSeconds:  secondsUntil(resetsAt, now),
		ResetsAt:         resetsAt,
		ResetInferred:    inferred,
		ElapsedPercent:   elapsedPercent(resetsAt, period, now),
	}
}

// elapsedPercent places now within the window that ends at resetsAt, clamped
// to 0-100: before the window began reads as 0, past the reset as 100, and a
// window with no usable period as 0 rather than a division by nothing.
func elapsedPercent(resetsAt time.Time, period time.Duration, now time.Time) int {
	if period <= 0 || resetsAt.IsZero() {
		return 0
	}
	started := resetsAt.Add(-period)
	fraction := float64(now.Sub(started)) / float64(period)
	return percent(fraction)
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
