package decision

import (
	"fmt"
	"math"
	"time"
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

type UsageSnapshot struct {
	Provider   string       `json:"provider"`
	ObservedAt time.Time    `json:"observed_at"`
	Short      *UsageWindow `json:"short,omitempty"`
	Weekly     UsageWindow  `json:"weekly"`
	Source     string       `json:"source"`
	Confidence string       `json:"confidence,omitempty"`
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

type Result struct {
	Decision               Decision       `json:"decision"`
	Mode                   Mode           `json:"mode"`
	Reason                 string         `json:"reason"`
	Slots                  []Slot         `json:"slots,omitempty"`
	FutureFullWindows      int            `json:"future_full_windows"`
	CurrentWindowCapacity  float64        `json:"current_window_capacity"`
	MaximumConsumable      float64        `json:"maximum_consumable"`
	Overflow               float64        `json:"overflow"`
	RollingDispatchable    float64        `json:"rolling_dispatchable"`
	MatchedPaceThreshold   *PaceThreshold `json:"matched_pace_threshold,omitempty"`
	WindowWeeklyCost       float64        `json:"window_weekly_cost,omitempty"`
	WindowWeeklyCostSource string         `json:"window_weekly_cost_source,omitempty"`
	CalibrationConfidence  string         `json:"calibration_confidence,omitempty"`
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
	}
	if overflow <= in.TriggerMargin {
		result.Decision = Wait
		result.Reason = "no actionable weekly overflow"
		return result
	}
	if dispatchable <= 0 {
		result.Decision = Wait
		result.Reason = "current 5-hour reserve protected"
		return result
	}
	result.Decision = Run
	result.Reason = "weekly remaining exceeds prorated short-window throughput"
	return result
}

func evaluatePace(in Input) Result {
	if len(in.PaceThresholds) == 0 {
		return unknown("unrestricted provider has no pace thresholds")
	}
	timeRemaining := in.Snapshot.Weekly.ResetsAt.Sub(in.Now)
	result := Result{Mode: ModePace}
	for i := range in.PaceThresholds {
		threshold := in.PaceThresholds[i]
		if timeRemaining <= threshold.TimeRemaining &&
			in.Snapshot.Weekly.Remaining >= threshold.MinWeeklyRemaining {
			result.Decision = Run
			result.Reason = "weekly remaining meets pace threshold"
			result.MatchedPaceThreshold = &threshold
			return result
		}
	}
	result.Decision = Wait
	result.Reason = "no pace threshold matched"
	return result
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
