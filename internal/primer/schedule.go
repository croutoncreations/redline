// Package primer opens a provider's 5-hour usage window at a chosen moment
// by sending one tiny message. The window starts at the first message after
// the previous one expires, so a ping controls when it resets.
package primer

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Windows has no system zone database.
	"unicode/utf8"

	"github.com/croutoncreations/redline/internal/domain"
)

const (
	DefaultPrompt  = "Reply with only: ok"
	DefaultModel   = "haiku"
	DefaultCatchUp = 45 * time.Minute
	MaxCatchUp     = 6 * time.Hour
	// WindowLength is the provider's short usage window.
	WindowLength = 5 * time.Hour
	// FireDelay is how long after a target the ping is sent. Provider resets
	// land within a second of the boundary; a ping even slightly early falls
	// inside the expiring window and does nothing.
	FireDelay = time.Minute
	// idleRetry bounds how often reset mode retries when no window is known.
	// A retry that finds a window open costs only a usage lookup.
	idleRetry = 15 * time.Minute
	// resetGrace is how long after a reset its slot stays the one to use;
	// after that the stored reset is stale and reset mode retries on idle.
	resetGrace = 10 * time.Minute
)

var weekdayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// A model must not start with "-": the CLI could read it as a flag.
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,99}$`)

// ErrInvalid marks a settings validation failure, as opposed to a storage
// or lookup error.
var ErrInvalid = errors.New("invalid primer settings")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Defaults is the preset used before the operator saves anything.
func Defaults(provider string) domain.PrimerSettings {
	return domain.PrimerSettings{
		ProviderAccountID: provider, Mode: domain.PrimerSchedule,
		Times: []string{"06:00"}, Days: []string{},
		Prompt: DefaultPrompt, Model: DefaultModel, CatchUpSeconds: int64(DefaultCatchUp / time.Second),
	}
}

// Normalize validates settings and puts them in canonical form.
func Normalize(settings domain.PrimerSettings) (domain.PrimerSettings, error) {
	switch settings.Mode {
	case domain.PrimerSchedule, domain.PrimerReset:
	default:
		return settings, invalid("mode must be schedule or reset")
	}
	times, err := normalizeTimes(settings.Times)
	if err != nil {
		return settings, err
	}
	settings.Times = times
	days, err := NormalizeDays(settings.Days)
	if err != nil {
		return settings, err
	}
	settings.Days = days
	if settings.Enabled && settings.Mode == domain.PrimerSchedule && len(settings.Times) == 0 {
		return settings, invalid("schedule mode requires at least one time")
	}
	settings.Timezone = strings.TrimSpace(settings.Timezone)
	if strings.EqualFold(settings.Timezone, "local") {
		settings.Timezone = ""
	}
	if settings.Timezone != "" {
		if _, err := time.LoadLocation(settings.Timezone); err != nil {
			return settings, invalid("timezone must be an IANA name such as America/Chicago")
		}
	}
	settings.Prompt = strings.TrimSpace(settings.Prompt)
	if settings.Prompt == "" {
		settings.Prompt = DefaultPrompt
	}
	if utf8.RuneCountInString(settings.Prompt) > 500 {
		return settings, invalid("prompt must be at most 500 characters")
	}
	settings.Model = strings.TrimSpace(settings.Model)
	if settings.Model == "" {
		settings.Model = DefaultModel
	}
	if !modelPattern.MatchString(settings.Model) {
		return settings, invalid("model must be a model name or alias such as haiku")
	}
	if settings.CatchUpSeconds < 0 || settings.CatchUpSeconds > int64(MaxCatchUp/time.Second) {
		return settings, invalid("catch_up_seconds must be between 0 and %d", int64(MaxCatchUp/time.Second))
	}
	return settings, nil
}

func normalizeTimes(values []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := time.Parse("15:04", value)
		if err != nil {
			if parsed, err = time.Parse("3:04pm", strings.ToLower(value)); err != nil {
				return nil, invalid("time %q must be HH:MM", value)
			}
		}
		canonical := parsed.Format("15:04")
		if !seen[canonical] {
			seen[canonical] = true
			result = append(result, canonical)
		}
	}
	sort.Strings(result)
	if len(result) > 12 {
		return nil, invalid("at most 12 times are supported")
	}
	return result, nil
}

// NormalizeDays accepts day names plus the shorthands daily, weekdays, and
// weekends. Every day selected is stored as an empty list.
func NormalizeDays(values []string) ([]string, error) {
	selected := map[int]bool{}
	for _, raw := range values {
		for _, value := range strings.Split(raw, ",") {
			value = strings.ToLower(strings.TrimSpace(value))
			switch value {
			case "":
			case "daily", "all", "every", "everyday":
				for day := range weekdayNames {
					selected[day] = true
				}
			case "weekdays":
				for day := 1; day <= 5; day++ {
					selected[day] = true
				}
			case "weekends":
				selected[0], selected[6] = true, true
			default:
				found := false
				for index, name := range weekdayNames {
					if value == name || (len(value) > 3 && strings.HasPrefix(fullDayName(index), value)) {
						selected[index], found = true, true
					}
				}
				if !found {
					return nil, invalid("day %q must be a weekday name, weekdays, weekends, or daily", value)
				}
			}
		}
	}
	if len(selected) == 0 || len(selected) == len(weekdayNames) {
		return []string{}, nil
	}
	result := make([]string, 0, len(selected))
	for _, index := range []int{1, 2, 3, 4, 5, 6, 0} {
		if selected[index] {
			result = append(result, weekdayNames[index])
		}
	}
	return result, nil
}

func fullDayName(index int) string { return strings.ToLower(time.Weekday(index).String()) }

// Location resolves the settings' zone, falling back to the service's.
func Location(settings domain.PrimerSettings) *time.Location {
	if settings.Timezone != "" {
		if location, err := time.LoadLocation(settings.Timezone); err == nil {
			return location
		}
	}
	return time.Local
}

// ZoneName names the zone in use. Go reports the service's own zone as
// "Local", which clients cannot resolve, so the IANA name is recovered where
// the platform exposes it.
func ZoneName(settings domain.PrimerSettings) string {
	if settings.Timezone != "" {
		return Location(settings).String()
	}
	return localZoneName()
}

var localZoneName = sync.OnceValue(func() string {
	if name := strings.TrimSpace(os.Getenv("TZ")); name != "" && !strings.HasPrefix(name, ":") {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, found := strings.Cut(target, "zoneinfo/"); found {
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return time.Local.String()
})

// SlotKind says what put a slot on the calendar.
type SlotKind string

const (
	SlotScheduled SlotKind = "schedule" // a configured time
	SlotReset     SlotKind = "reset"    // the end of a known window
	SlotIdle      SlotKind = "idle"     // reset mode with no window known
	SlotBackoff   SlotKind = "backoff"  // waiting out failed pings
	SlotManual    SlotKind = "manual"
)

// Slot is one occasion to ping: the moment the window should start and the
// moment the ping is sent.
type Slot struct {
	Kind   SlotKind
	Key    string
	Target time.Time
	FireAt time.Time
}

// ScheduledSlots lists schedule-mode slots whose targets fall in [from, to).
func ScheduledSlots(settings domain.PrimerSettings, from, to time.Time) []Slot {
	location := Location(settings)
	days := map[string]bool{}
	for _, day := range settings.Days {
		days[day] = true
	}
	slots := make([]Slot, 0)
	start := from.In(location).AddDate(0, 0, -1)
	for day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, location); !day.After(to.In(location)); day = day.AddDate(0, 0, 1) {
		if len(days) > 0 && !days[weekdayNames[day.Weekday()]] {
			continue
		}
		for _, clock := range settings.Times {
			parsed, err := time.Parse("15:04", clock)
			if err != nil {
				continue
			}
			target := time.Date(day.Year(), day.Month(), day.Day(), parsed.Hour(), parsed.Minute(), 0, 0, location)
			// A wall-clock time that does not exist on a spring-forward
			// day is normalised by time.Date to an hour earlier; that
			// day has no such slot rather than a wrong one.
			if target.Hour() != parsed.Hour() || target.Minute() != parsed.Minute() {
				continue
			}
			if target.Before(from) || !target.Before(to) {
				continue
			}
			slots = append(slots, Slot{Kind: SlotScheduled, Key: "schedule:" + target.UTC().Format(time.RFC3339), Target: target.UTC(), FireAt: target.Add(FireDelay).UTC()})
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Target.Before(slots[j].Target) })
	return slots
}

// EffectiveCatchUp is the catch-up allowance actually applied: even with
// none configured, a slot must survive until the next tick sees it.
func EffectiveCatchUp(settings domain.PrimerSettings) time.Duration {
	return max(time.Duration(settings.CatchUpSeconds)*time.Second, 2*TickInterval)
}

// DueScheduled returns the latest schedule slot that is due at now and still
// within the catch-up allowance, plus the slots missed beyond it since
// lastCheck. Slots whose target precedes the settings' save time are never
// due: a time added after it has passed must wait for its next occurrence,
// or the window would open at the wrong time.
func DueScheduled(settings domain.PrimerSettings, now, lastCheck time.Time) (*Slot, []Slot) {
	catchUp := EffectiveCatchUp(settings)
	lookback := catchUp + FireDelay
	if !lastCheck.IsZero() && now.Sub(lastCheck) > lookback {
		lookback = now.Sub(lastCheck) + FireDelay
	}
	if lookback > 48*time.Hour {
		lookback = 48 * time.Hour
	}
	var due *Slot
	missed := make([]Slot, 0)
	for _, slot := range ScheduledSlots(settings, now.Add(-lookback), now) {
		if slot.FireAt.After(now) || slot.Target.Before(settings.UpdatedAt) {
			continue
		}
		if now.Sub(slot.FireAt) <= catchUp {
			found := slot
			due = &found
			continue
		}
		if !lastCheck.IsZero() && slot.FireAt.After(lastCheck) {
			missed = append(missed, slot)
		}
	}
	return due, missed
}

// NextScheduled returns the first schedule slot that fires after now and
// after openUntil, since a slot inside an open window is skipped. Slots
// older than the settings are passed over, as DueScheduled passes them.
func NextScheduled(settings domain.PrimerSettings, now time.Time, openUntil *time.Time) *Slot {
	for _, slot := range ScheduledSlots(settings, now.Add(-FireDelay), now.Add(8*24*time.Hour)) {
		if slot.Target.Before(settings.UpdatedAt) {
			continue
		}
		if slot.FireAt.After(now) && (openUntil == nil || !slot.FireAt.Before(*openUntil)) {
			found := slot
			return &found
		}
	}
	return nil
}

// ResetSlot returns reset mode's slot given the latest known short window
// reset. A nil reset means the provider reports no open window; a reset long
// past means the sample is stale. Both retry on the idle cadence.
func ResetSlot(resetsAt *time.Time, now time.Time) Slot {
	if resetsAt != nil && now.Sub(*resetsAt) <= resetGrace {
		target := resetsAt.UTC().Round(time.Minute)
		// Samples of one reset jitter by about a second either side of the
		// boundary (11:19:59.9 and 11:20:00.3); a coarser key keeps them
		// one slot.
		key := resetsAt.UTC().Round(5 * time.Minute)
		return Slot{Kind: SlotReset, Key: "reset:" + key.Format(time.RFC3339), Target: target, FireAt: target.Add(FireDelay)}
	}
	return IdleSlot(now)
}

// IdleSlot is reset mode's retry when no window is known to be open. Each
// retry bucket is attempted at most once.
func IdleSlot(now time.Time) Slot {
	bucket := now.UTC().Truncate(idleRetry)
	return Slot{Kind: SlotIdle, Key: "idle:" + bucket.Format(time.RFC3339), Target: now.UTC(), FireAt: now.UTC()}
}

// NextIdleRetry is when the next idle bucket opens.
func NextIdleRetry(now time.Time) time.Time { return now.UTC().Truncate(idleRetry).Add(idleRetry) }

// FailureRun describes the run of failures since the last ping known (or
// presumed) to work.
type FailureRun struct {
	Count int
	// Hard is set when a ping in the run failed outright (signed out, CLI
	// missing) rather than being sent and opening no window.
	Hard bool
	// Last is when the newest failure became known; backoff counts from it.
	Last time.Time
}

// Alarming reports whether the run deserves a notification: one hard
// failure, or two pings in a row that opened no window (one could be a
// stale sample).
func (r FailureRun) Alarming() bool { return r.Hard || r.Count >= 2 }

// ConsecutiveFailures counts the pings since the last one known to work
// that either failed or were sent but opened no window. It ignores skips
// (nothing was sent), attempts interrupted by a restart (the ping may well
// have worked), and pings still awaiting verification; a ping whose
// verification never got a sample ("unknown") is presumed to have worked.
func ConsecutiveFailures(recent []domain.PrimerAttempt) FailureRun {
	var run FailureRun
	for _, attempt := range recent {
		if attempt.Outcome == domain.PrimerSkipped || attempt.Interrupted() ||
			(attempt.Outcome == domain.PrimerFired && attempt.Verification == domain.PrimerVerifyPending) {
			continue
		}
		if !attempt.Failed() {
			break
		}
		if run.Count == 0 {
			run.Last = attempt.CompletedAt
			if attempt.VerifiedAt != nil && attempt.VerifiedAt.After(run.Last) {
				run.Last = *attempt.VerifiedAt
			}
		}
		run.Count++
		run.Hard = run.Hard || attempt.Outcome == domain.PrimerFailed
	}
	return run
}

// Window is a forecast usage window.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// ForecastScheduled chains the windows the next day's schedule would open.
// A target inside an earlier forecast window is skipped, as it would be live.
func ForecastScheduled(settings domain.PrimerSettings, now time.Time, openUntil *time.Time) []Window {
	windows := make([]Window, 0)
	var busyUntil time.Time
	if openUntil != nil {
		busyUntil = *openUntil
	}
	for _, slot := range ScheduledSlots(settings, now, now.Add(24*time.Hour)) {
		if slot.FireAt.Before(busyUntil) {
			continue
		}
		window := Window{Start: slot.Target, End: slot.Target.Add(WindowLength)}
		windows = append(windows, window)
		busyUntil = window.End
	}
	return windows
}

// forecastWindows is how many back-to-back windows reset mode forecasts.
const forecastWindows = 4

// ForecastReset chains back-to-back windows from the current reset.
func ForecastReset(now time.Time, openUntil *time.Time) []Window {
	start := now.UTC()
	if openUntil != nil && openUntil.After(now) {
		start = openUntil.UTC().Round(time.Minute)
	}
	windows := make([]Window, 0, forecastWindows)
	for index := 0; index < forecastWindows; index++ {
		windows = append(windows, Window{Start: start, End: start.Add(WindowLength)})
		start = start.Add(WindowLength)
	}
	return windows
}
