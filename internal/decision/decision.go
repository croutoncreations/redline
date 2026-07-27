package decision

import (
	"fmt"
	"math"
	"time"

	"github.com/jfox/redline/internal/domain"
)

const ShortWindowDuration = 5 * time.Hour

type Decision string
type Mode string

const (
	Run     Decision = "RUN"
	Wait    Decision = "WAIT"
	Unknown Decision = "UNKNOWN"

	ModeSlots  Mode = "window_slots"
	ModePace   Mode = "pace_threshold"
	ModePaused Mode = "paused"
	ModeActive Mode = "active_run"
)

type UsageWindow struct {
	Remaining float64   `json:"remaining"`
	ResetsAt  time.Time `json:"resets_at"`
}

type AllowanceWindow struct {
	Key                   string    `json:"key"`
	SourceLabel           string    `json:"source_label"`
	Scope                 string    `json:"scope"`
	Role                  string    `json:"role"`
	Remaining             float64   `json:"remaining"`
	ResetsAt              time.Time `json:"resets_at"`
	PeriodDurationSeconds int64     `json:"period_duration_seconds"`
	ResetInferred         bool      `json:"reset_inferred,omitempty"`
}

type UsageSnapshot struct {
	Provider   string            `json:"provider"`
	ObservedAt time.Time         `json:"observed_at"`
	Short      *UsageWindow      `json:"short,omitempty"`
	Weekly     UsageWindow       `json:"weekly"`
	Allowances []AllowanceWindow `json:"allowances,omitempty"`
	Source     string            `json:"source"`
	Confidence string            `json:"confidence,omitempty"`
}

func (s UsageSnapshot) Allowance(key string) (AllowanceWindow, bool) {
	for _, allowance := range s.Allowances {
		if allowance.Key == key {
			return allowance, true
		}
	}
	switch key {
	case "session":
		if s.Short != nil {
			return AllowanceWindow{Key: "session", SourceLabel: "Session", Scope: "account", Role: "short",
				Remaining: s.Short.Remaining, ResetsAt: s.Short.ResetsAt,
				PeriodDurationSeconds: int64(ShortWindowDuration / time.Second)}, true
		}
	case "weekly":
		if !s.Weekly.ResetsAt.IsZero() {
			return AllowanceWindow{Key: "weekly", SourceLabel: "Weekly", Scope: "account", Role: "weekly",
				Remaining: s.Weekly.Remaining, ResetsAt: s.Weekly.ResetsAt,
				PeriodDurationSeconds: int64((7 * 24 * time.Hour) / time.Second)}, true
		}
	}
	return AllowanceWindow{}, false
}

func (s UsageSnapshot) AllAllowances() []AllowanceWindow {
	result := append([]AllowanceWindow(nil), s.Allowances...)
	for _, key := range []string{"session", "weekly"} {
		if _, exists := findAllowance(result, key); exists {
			continue
		}
		if allowance, ok := s.Allowance(key); ok {
			result = append(result, allowance)
		}
	}
	return result
}

func findAllowance(allowances []AllowanceWindow, key string) (AllowanceWindow, bool) {
	for _, allowance := range allowances {
		if allowance.Key == key {
			return allowance, true
		}
	}
	return AllowanceWindow{}, false
}

func (s UsageSnapshot) Validate() error {
	if s.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if s.ObservedAt.IsZero() || s.Weekly.ResetsAt.IsZero() {
		return fmt.Errorf("observation and weekly reset timestamps are required")
	}
	if err := unitFraction("weekly remaining", s.Weekly.Remaining); err != nil {
		return err
	}
	if s.Short != nil {
		if s.Short.ResetsAt.IsZero() {
			return fmt.Errorf("short-window reset timestamp is required")
		}
		if err := unitFraction("short remaining", s.Short.Remaining); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(s.Allowances))
	for _, allowance := range s.Allowances {
		if allowance.Key == "" || allowance.SourceLabel == "" || allowance.Scope == "" || allowance.Role == "" {
			return fmt.Errorf("allowance key, source label, scope, and role are required")
		}
		if seen[allowance.Key] {
			return fmt.Errorf("duplicate allowance %q", allowance.Key)
		}
		seen[allowance.Key] = true
		if allowance.Scope != "account" && allowance.Scope != "model" {
			return fmt.Errorf("allowance %q scope must be account or model", allowance.Key)
		}
		if allowance.Role != "short" && allowance.Role != "weekly" {
			return fmt.Errorf("allowance %q role must be short or weekly", allowance.Key)
		}
		if allowance.ResetsAt.IsZero() || allowance.PeriodDurationSeconds <= 0 {
			return fmt.Errorf("allowance %q reset and period duration are required", allowance.Key)
		}
		if err := unitFraction("allowance remaining", allowance.Remaining); err != nil {
			return fmt.Errorf("allowance %q: %w", allowance.Key, err)
		}
	}
	return nil
}

type PaceThreshold struct {
	TimeRemaining      time.Duration `json:"time_remaining"`
	MinWeeklyRemaining float64       `json:"min_weekly_remaining"`
}

type Input struct {
	Snapshot               UsageSnapshot
	WindowWeeklyCost       float64
	WindowWeeklyCostSource string
	CalibrationConfidence  string
	TriggerMargin          float64
	RollingReserve         float64
	PaceGapTrigger         *float64
	PaceThresholds         []PaceThreshold
	Now                    time.Time
	MaxSnapshotAge         time.Duration
}

type Slot struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Fraction float64   `json:"fraction"`
	Current  bool      `json:"current"`
}

type PoolResult struct {
	Pool         string              `json:"pool"`
	Decision     Decision            `json:"decision"`
	Mode         Mode                `json:"mode,omitempty"`
	Reason       string              `json:"reason"`
	Remaining    float64             `json:"remaining"`
	UnlockedTier domain.DispatchTier `json:"unlocked_tier,omitempty"`
}

type CandidateRejection struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}

type Result struct {
	Decision               Decision             `json:"decision"`
	Policy                 string               `json:"policy,omitempty"`
	Mode                   Mode                 `json:"mode"`
	Reason                 string               `json:"reason"`
	Slots                  []Slot               `json:"slots,omitempty"`
	FutureFullWindows      int                  `json:"future_full_windows"`
	CurrentWindowCapacity  float64              `json:"current_window_capacity"`
	MaximumConsumable      float64              `json:"maximum_consumable"`
	Overflow               float64              `json:"overflow"`
	RollingDispatchable    float64              `json:"rolling_dispatchable"`
	MatchedPaceThreshold   *PaceThreshold       `json:"matched_pace_threshold,omitempty"`
	WindowWeeklyCost       float64              `json:"window_weekly_cost,omitempty"`
	WindowWeeklyCostSource string               `json:"window_weekly_cost_source,omitempty"`
	CalibrationConfidence  string               `json:"calibration_confidence,omitempty"`
	Model                  string               `json:"model,omitempty"`
	ModelRouting           string               `json:"model_routing,omitempty"`
	RequiredPools          []string             `json:"required_pools,omitempty"`
	TriggeringPools        []string             `json:"triggering_pools,omitempty"`
	PoolResults            []PoolResult         `json:"pool_results,omitempty"`
	CandidateRejections    []CandidateRejection `json:"candidate_rejections,omitempty"`
	TaskSelectionReason    string               `json:"task_selection_reason,omitempty"`
	PaceGap                float64              `json:"pace_gap"`
	UnlockedTier           domain.DispatchTier  `json:"unlocked_tier,omitempty"`
}

func Evaluate(in Input) Result {
	if err := in.Snapshot.Validate(); err != nil {
		return unknown(err.Error())
	}
	if err := validateInput(in); err != nil {
		return unknown(err.Error())
	}
	if in.Now.Sub(in.Snapshot.ObservedAt) > in.MaxSnapshotAge {
		return unknown("usage snapshot is stale")
	}
	if in.Snapshot.ObservedAt.After(in.Now) {
		return unknown("usage snapshot is from the future")
	}
	if !in.Snapshot.Weekly.ResetsAt.After(in.Now) {
		return unknown("weekly reset is not in the future")
	}
	if in.Snapshot.Short == nil {
		return evaluatePace(in)
	}
	if !in.Snapshot.Short.ResetsAt.After(in.Now) {
		return unknown("short-window reset is not in the future")
	}
	return evaluateSlots(in)
}

// ProjectTriggerAt returns the first scheduler poll at which unchanged weekly
// usage would become eligible to run. Short windows reset to full capacity at
// their fixed boundaries. The projection intentionally makes no claim about
// future interactive usage and returns nil when flat usage never qualifies.
func ProjectTriggerAt(in Input, pollInterval time.Duration) *time.Time {
	if pollInterval <= 0 || in.Now.IsZero() || !in.Snapshot.Weekly.ResetsAt.After(in.Now) {
		return nil
	}
	if result := Evaluate(in); result.Decision == Run {
		projected := in.Now
		return &projected
	}
	for projected := in.Now.Add(pollInterval); projected.Before(in.Snapshot.Weekly.ResetsAt); projected = projected.Add(pollInterval) {
		future := projectInput(in, projected)
		if result := Evaluate(future); result.Decision == Run {
			at := projected
			return &at
		}
	}
	return nil
}

func projectInput(in Input, at time.Time) Input {
	future := in
	future.Now = at
	future.Snapshot.ObservedAt = at
	if in.Snapshot.Short == nil {
		return future
	}
	short := *in.Snapshot.Short
	if !at.Before(short.ResetsAt) {
		elapsed := at.Sub(short.ResetsAt)
		resets := elapsed/ShortWindowDuration + 1
		short.ResetsAt = short.ResetsAt.Add(resets * ShortWindowDuration)
		short.Remaining = 1
	}
	future.Snapshot.Short = &short
	return future
}

func evaluateSlots(in Input) Result {
	slots := buildSlots(in.Now, *in.Snapshot.Short, in.Snapshot.Weekly.ResetsAt)
	maximumConsumable := 0.0
	currentCapacity := 0.0
	fullWindows := 0
	for _, slot := range slots {
		maximumConsumable += slot.Fraction * in.WindowWeeklyCost
		if !slot.Current && slot.Fraction == 1 {
			fullWindows++
		}
		if slot.Current {
			currentCapacity = slot.Fraction * in.WindowWeeklyCost
		}
	}
	maximumConsumable = math.Min(1, maximumConsumable)
	overflow := in.Snapshot.Weekly.Remaining - maximumConsumable
	dispatchable := in.Snapshot.Short.Remaining - in.RollingReserve
	paceGap := weeklyPaceGap(in.Snapshot.Weekly, in.Now)
	result := Result{
		Mode:                   ModeSlots,
		Slots:                  slots,
		FutureFullWindows:      fullWindows,
		CurrentWindowCapacity:  currentCapacity,
		MaximumConsumable:      maximumConsumable,
		Overflow:               overflow,
		RollingDispatchable:    dispatchable,
		WindowWeeklyCost:       in.WindowWeeklyCost,
		WindowWeeklyCostSource: in.WindowWeeklyCostSource,
		CalibrationConfidence:  in.CalibrationConfidence,
		PaceGap:                paceGap,
	}
	if dispatchable <= 0 {
		result.Decision = Wait
		result.Reason = "current 5-hour reserve protected"
		return result
	}
	if overflow > in.TriggerMargin {
		result.Decision = Run
		result.Reason = "weekly remaining exceeds prorated short-window throughput"
		result.UnlockedTier = domain.DispatchExpiring
		return result
	}
	if in.PaceGapTrigger != nil && paceGap >= *in.PaceGapTrigger {
		result.Decision = Run
		result.Reason = "weekly remaining is well behind pace"
		result.UnlockedTier = tierForPaceGap(paceGap)
		return result
	}
	if threshold := matchingPaceThreshold(in); threshold != nil {
		result.Decision = Run
		result.Reason = "weekly remaining is behind configured pace"
		result.MatchedPaceThreshold = threshold
		result.UnlockedTier = tierForPaceGap(paceGap)
		return result
	}
	result.Decision = Wait
	result.Reason = "no actionable weekly overflow"
	return result
}

func evaluatePace(in Input) Result {
	if len(in.PaceThresholds) == 0 && in.PaceGapTrigger == nil {
		return unknown("unrestricted provider has no pace thresholds")
	}
	paceGap := weeklyPaceGap(in.Snapshot.Weekly, in.Now)
	result := Result{Mode: ModePace, PaceGap: paceGap}
	if in.PaceGapTrigger != nil && paceGap >= *in.PaceGapTrigger {
		result.Decision = Run
		result.Reason = "weekly remaining is well behind pace"
		result.UnlockedTier = tierForPaceGap(paceGap)
		return result
	}
	if threshold := matchingPaceThreshold(in); threshold != nil {
		result.Decision = Run
		result.Reason = "weekly remaining meets pace threshold"
		result.MatchedPaceThreshold = threshold
		result.UnlockedTier = tierForPaceGap(paceGap)
		return result
	}
	result.Decision = Wait
	result.Reason = "no pace threshold matched"
	return result
}

func matchingPaceThreshold(in Input) *PaceThreshold {
	timeRemaining := in.Snapshot.Weekly.ResetsAt.Sub(in.Now)
	for i := range in.PaceThresholds {
		threshold := in.PaceThresholds[i]
		if timeRemaining <= threshold.TimeRemaining && in.Snapshot.Weekly.Remaining >= threshold.MinWeeklyRemaining {
			matched := threshold
			return &matched
		}
	}
	return nil
}

func weeklyPaceGap(weekly UsageWindow, now time.Time) float64 {
	timeRemaining := weekly.ResetsAt.Sub(now).Seconds() / (7 * 24 * time.Hour).Seconds()
	return weekly.Remaining - clampFraction(timeRemaining)
}

func tierForPaceGap(gap float64) domain.DispatchTier {
	switch {
	case gap >= 0.30:
		return domain.DispatchExpiring
	case gap >= 0.15:
		return domain.DispatchWellBehind
	default:
		return domain.DispatchBehind
	}
}

func buildSlots(now time.Time, short UsageWindow, weeklyReset time.Time) []Slot {
	currentEnd := minTime(short.ResetsAt, weeklyReset)
	currentTimeFraction := currentEnd.Sub(now).Seconds() / ShortWindowDuration.Seconds()
	currentFraction := math.Min(short.Remaining, clampFraction(currentTimeFraction))
	slots := make([]Slot, 0)
	if currentFraction > 0 {
		slots = append(slots, Slot{
			StartsAt: now,
			EndsAt:   currentEnd,
			Fraction: currentFraction,
			Current:  true,
		})
	}
	for start := short.ResetsAt; start.Before(weeklyReset); start = start.Add(ShortWindowDuration) {
		end := minTime(start.Add(ShortWindowDuration), weeklyReset)
		fraction := clampFraction(end.Sub(start).Seconds() / ShortWindowDuration.Seconds())
		if fraction > 0 {
			slots = append(slots, Slot{StartsAt: start, EndsAt: end, Fraction: fraction})
		}
	}
	return slots
}

func validateInput(in Input) error {
	if err := unitFraction("window weekly cost", in.WindowWeeklyCost); err != nil {
		return err
	}
	if in.WindowWeeklyCost == 0 {
		return fmt.Errorf("window weekly cost must be greater than 0")
	}
	if err := unitFraction("trigger margin", in.TriggerMargin); err != nil {
		return err
	}
	if err := unitFraction("rolling reserve", in.RollingReserve); err != nil {
		return err
	}
	if in.PaceGapTrigger != nil {
		if err := unitFraction("pace gap trigger", *in.PaceGapTrigger); err != nil {
			return err
		}
	}
	for _, threshold := range in.PaceThresholds {
		if threshold.TimeRemaining <= 0 {
			return fmt.Errorf("pace threshold time remaining must be greater than 0")
		}
		if err := unitFraction("pace threshold weekly remaining", threshold.MinWeeklyRemaining); err != nil {
			return err
		}
	}
	if in.Now.IsZero() {
		return fmt.Errorf("current time is required")
	}
	if in.MaxSnapshotAge <= 0 {
		return fmt.Errorf("max snapshot age must be greater than 0")
	}
	return nil
}

func unitFraction(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return fmt.Errorf("%s must be between 0 and 1", name)
	}
	return nil
}

func clampFraction(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func unknown(reason string) Result {
	return Result{Decision: Unknown, Reason: reason}
}
