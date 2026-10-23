package decision

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

const ShortWindowDuration = 5 * time.Hour

type Decision string
type Mode string

const (
	Admit   Decision = "RUN"
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
	// PeriodSeconds is the length of the allowance period. Zero means the
	// historical seven-day week. Cursor's subscription pools are monthly, and
	// pace must be measured against the period that actually resets.
	PeriodSeconds int64 `json:"period_seconds,omitempty"`
}

// WeekSeconds is the default allowance period.
const WeekSeconds = int64((7 * 24 * time.Hour) / time.Second)

// Period returns the allowance period, defaulting to one week.
func (w UsageWindow) Period() time.Duration {
	if w.PeriodSeconds <= 0 {
		return time.Duration(WeekSeconds) * time.Second
	}
	return time.Duration(w.PeriodSeconds) * time.Second
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
	Provider string `json:"provider"`
	// Account is the Redline provider account (config key) the numbers were
	// collected for. Empty only on rows written before accounts were
	// recorded; such rows are shown for an account only when it is the sole
	// account of its provider.
	Account    string       `json:"account,omitempty"`
	ObservedAt time.Time    `json:"observed_at"`
	Short      *UsageWindow `json:"short,omitempty"`
	// Weekly is the account-wide weekly (or monthly) allowance. It is the
	// zero window, and must not be read, when WeeklyUnavailable is set.
	Weekly     UsageWindow       `json:"weekly"`
	Allowances []AllowanceWindow `json:"allowances,omitempty"`
	Source     string            `json:"source"`
	Confidence string            `json:"confidence,omitempty"`
	// ShortWindowUnavailable marks a five hour window that the provider
	// reported but that had to be dropped, which happens when it arrives with
	// no reset time while upstream state is refreshing.
	//
	// Without this, a client cannot tell "this provider has no five hour
	// limit" from "it has one and the number is missing right now", and those
	// render very differently: the first is an absent row, the second is a row
	// that says it does not know. Confidence cannot carry the distinction
	// because it also drops to medium for an inferred model weekly reset.
	ShortWindowUnavailable bool `json:"short_window_unavailable,omitempty"`
	// ShortWindowNotStarted marks a five hour window that exists and is
	// entirely unused. The provider starts the window's clock on first use,
	// so until then there is no reset time -- which is not the same as the
	// number being unreadable. The whole window is available.
	//
	// Short stays nil in this state on purpose: there is no reset to put in
	// it, and inventing one (observation time plus five hours) would be a
	// time the provider never said. The scheduler already treats a nil Short
	// as "no short window to slot", which is correct for a window that has
	// not begun.
	ShortWindowNotStarted bool `json:"short_window_not_started,omitempty"`
	// WeeklyUnavailable marks an account whose source reports no
	// account-wide weekly limit at all -- OpenUsage's Team seats report only
	// the 5-hour window, model windows and reset credits. Weekly is then the
	// zero window: never a number to show, never something to schedule
	// against. Nothing is inferred from the 5-hour or model windows.
	WeeklyUnavailable bool `json:"weekly_unavailable,omitempty"`
	// BankedResets counts quota resets the account can spend on demand to
	// refill an exhausted window. Nil means the provider did not report it,
	// which is different from zero: none banked versus not known.
	BankedResets *int `json:"banked_resets,omitempty"`
	// BankedResetsExpireAt is when the soonest-expiring banked reset lapses.
	// Nil when there are none or the provider did not say.
	BankedResetDetails   []BankedReset `json:"banked_reset_details,omitempty"`
	BankedResetsExpireAt *time.Time    `json:"banked_resets_expire_at,omitempty"`
	// BankedResetsExpiries lists each banked reset's own deadline, soonest
	// first, when the source reports them individually (OpenUsage's limits
	// API does). Empty means not reported, not "no deadlines": a reset with
	// no deadline is simply absent from the list.
	BankedResetsExpiries []time.Time `json:"banked_resets_expiries,omitempty"`
	// Plan is the subscription the source reports for this account, such as
	// "Max 20x" or "Team". Display text only: it is never used to infer how
	// large an allowance is.
	Plan string `json:"plan,omitempty"`
	// SourceStale is the source's own statement that these numbers are past
	// their freshness interval (OpenUsage's "stale"). Redline still applies
	// its own max snapshot age on top.
	SourceStale bool `json:"source_stale,omitempty"`
}

// BankedReset describes one grant or credit, not additional scheduling capacity.
type BankedReset struct {
	ID            string     `json:"id"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Clears        []string   `json:"clears,omitempty"`
	UsableNow     *bool      `json:"usable_now,omitempty"`
	RequiresLimit *bool      `json:"requires_limit,omitempty"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	ResetsLeft    int        `json:"resets_left"`
	// Only Claude's next_grant_id is claimable.
	Claimable bool `json:"claimable,omitempty"`
}

// BankedResets is a provider's report of on-demand quota resets.
type BankedResets struct {
	Available     int
	NextExpiresAt *time.Time
	// Expiries is every reported deadline, soonest first. Optional.
	Expiries []time.Time
	Details  []BankedReset
}

// ApplyBankedResets copies a reset report onto the snapshot. A nil report
// leaves the snapshot saying nothing about resets.
func (s *UsageSnapshot) ApplyBankedResets(resets *BankedResets) {
	s.BankedResetDetails = nil
	if resets == nil {
		s.BankedResets, s.BankedResetsExpireAt, s.BankedResetsExpiries = nil, nil, nil
		return
	}
	for _, detail := range resets.Details {
		var clears []string
		seen := make(map[string]bool)
		for _, window := range detail.Clears {
			if window == "seven_day" || window == "seven_day_overage_included" {
				window = "weekly"
			}
			if (window == "five_hour" || window == "weekly") && !seen[window] {
				clears = append(clears, window)
				seen[window] = true
			}
		}
		detail.Clears = clears
		s.BankedResetDetails = append(s.BankedResetDetails, detail)
	}
	count := resets.Available
	s.BankedResets = &count
	s.BankedResetsExpireAt = nil
	s.BankedResetsExpiries = nil
	if count > 0 && resets.NextExpiresAt != nil && !resets.NextExpiresAt.IsZero() {
		expires := resets.NextExpiresAt.UTC()
		s.BankedResetsExpireAt = &expires
	}
	if count > 0 {
		expiries := resets.Expiries
		if len(expiries) == 0 {
			for _, detail := range s.BankedResetDetails {
				if detail.ExpiresAt != nil && detail.ResetsLeft > 0 {
					expiries = append(expiries, *detail.ExpiresAt)
				}
			}
		}
		for _, expiry := range expiries {
			if !expiry.IsZero() {
				s.BankedResetsExpiries = append(s.BankedResetsExpiries, expiry.UTC())
			}
		}
		sort.Slice(s.BankedResetsExpiries, func(i, j int) bool { return s.BankedResetsExpiries[i].Before(s.BankedResetsExpiries[j]) })
		if len(s.BankedResetsExpiries) > count {
			s.BankedResetsExpiries = s.BankedResetsExpiries[:count]
		}
		if s.BankedResetsExpireAt == nil && len(s.BankedResetsExpiries) > 0 {
			soonest := s.BankedResetsExpiries[0]
			s.BankedResetsExpireAt = &soonest
		}
	}
}

// MarshalJSON leaves the weekly window out entirely when the source did not
// report one, so no client can read its zero value as "0% left". Every
// other field is encoded as before.
func (s UsageSnapshot) MarshalJSON() ([]byte, error) {
	type plain UsageSnapshot
	if !s.WeeklyUnavailable {
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		plain
		Weekly *UsageWindow `json:"weekly,omitempty"`
	}{plain: plain(s)})
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
		if !s.WeeklyUnavailable && !s.Weekly.ResetsAt.IsZero() {
			return AllowanceWindow{Key: "weekly", SourceLabel: "Weekly", Scope: "account", Role: "weekly",
				Remaining: s.Weekly.Remaining, ResetsAt: s.Weekly.ResetsAt,
				PeriodDurationSeconds: int64(s.Weekly.Period() / time.Second)}, true
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
	if s.ObservedAt.IsZero() {
		return fmt.Errorf("observation timestamp is required")
	}
	if s.WeeklyUnavailable {
		// Not reported is a state of its own: a weekly value beside it would
		// be a number some surface could show.
		if s.Weekly != (UsageWindow{}) {
			return fmt.Errorf("an unreported weekly window cannot carry a value")
		}
		for _, allowance := range s.Allowances {
			if allowance.Key == "weekly" {
				return fmt.Errorf("an unreported weekly window cannot have a weekly allowance")
			}
		}
		if s.Short == nil && !s.ShortWindowNotStarted && !s.ShortWindowUnavailable && len(s.Allowances) == 0 && s.BankedResets == nil {
			return fmt.Errorf("a snapshot without a weekly window must report something else")
		}
	} else {
		if s.Weekly.ResetsAt.IsZero() {
			return fmt.Errorf("observation and weekly reset timestamps are required")
		}
		if err := unitFraction("weekly remaining", s.Weekly.Remaining); err != nil {
			return err
		}
		if s.Weekly.PeriodSeconds < 0 {
			return fmt.Errorf("allowance period cannot be negative")
		}
	}
	if s.BankedResets != nil && *s.BankedResets < 0 {
		return fmt.Errorf("banked resets cannot be negative")
	}
	if s.BankedResetsExpireAt != nil && (s.BankedResets == nil || *s.BankedResets == 0) {
		return fmt.Errorf("banked reset expiry requires at least one banked reset")
	}
	if s.ShortWindowNotStarted && (s.Short != nil || s.ShortWindowUnavailable) {
		// Not started means untouched and full; it cannot coexist with a
		// timed window or an unreadable one, and every surface would draw a
		// full bar over whichever of those was true.
		return fmt.Errorf("a not-started short window cannot also be timed or unavailable")
	}
	for _, detail := range s.BankedResetDetails {
		if detail.ID == "" || detail.ResetsLeft < 0 {
			return fmt.Errorf("banked reset details require an id and non-negative resets left")
		}
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
		if allowance.Role != "short" && allowance.Role != "weekly" && allowance.Role != "monthly" {
			return fmt.Errorf("allowance %q role must be short, weekly or monthly", allowance.Key)
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

type ResetSource string

const (
	ResetWeekly          ResetSource = "weekly"
	ResetBankedReset     ResetSource = "banked_reset"
	ResetPredictedGlobal ResetSource = "predicted_global"
)

type ResetEvent struct {
	Source     ResetSource `json:"source"`
	At         time.Time   `json:"at"`
	Clears     []string    `json:"clears"`
	Confidence string      `json:"confidence"`
	Detail     string      `json:"detail"`
}

// effectiveReset defaults to the real weekly reset; other windows cannot shorten it.
func effectiveReset(in Input) ResetEvent {
	reset := ResetEvent{Source: ResetWeekly, At: in.Snapshot.Weekly.ResetsAt, Clears: []string{"weekly"}}
	for _, event := range in.ResetEvents {
		if !event.At.After(in.Now) || !event.At.Before(reset.At) {
			continue
		}
		for _, window := range event.Clears {
			if window == "weekly" {
				reset = event
				break
			}
		}
	}
	return reset
}

type Input struct {
	ResetEvents            []ResetEvent
	FiresaleTrigger        *float64
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
	Firesale               bool                 `json:"firesale"`
	EffectiveResetAt       time.Time            `json:"effective_reset_at"`
	EffectiveResetSource   ResetSource          `json:"effective_reset_source"`
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
	if in.Snapshot.SourceStale {
		// The source says these numbers are old however recently we read
		// them; spending against them would be a guess.
		return unknown("usage source reports this account's numbers as out of date")
	}
	if in.Snapshot.ObservedAt.After(in.Now) {
		return unknown("usage snapshot is from the future")
	}
	if in.Snapshot.WeeklyUnavailable {
		// Every dispatch rule here is paced against the weekly allowance.
		// Without one there is nothing safe to pace against, so dispatch
		// holds until a policy exists for accounts without a weekly limit.
		return unknown("weekly limit not reported; dispatch holds until a policy exists for this account type")
	}
	if !in.Snapshot.Weekly.ResetsAt.After(in.Now) {
		return unknown("weekly reset is not in the future")
	}
	if in.Snapshot.ShortWindowUnavailable {
		return unknown("five-hour window is active but its reset time is unavailable; dispatch holds until it is readable")
	}
	reset := effectiveReset(in)
	in.Snapshot.Weekly.ResetsAt = reset.At
	var result Result
	if in.Snapshot.Short == nil {
		result = evaluatePace(in)
	} else if !in.Snapshot.Short.ResetsAt.After(in.Now) {
		result = unknown("short-window reset is not in the future")
	} else {
		result = evaluateSlots(in)
	}
	pressure := result.PaceGap
	if in.Snapshot.Short != nil {
		pressure = math.Max(pressure, result.Overflow)
	}
	if result.Decision == Admit && in.FiresaleTrigger != nil && pressure >= *in.FiresaleTrigger {
		result.Firesale = true
		result.UnlockedTier = domain.DispatchFiresale
		result.Reason += "; firesale pressure reached"
	}
	result.EffectiveResetAt = reset.At
	result.EffectiveResetSource = reset.Source
	return result
}

// ProjectTriggerAt returns the first scheduler poll at which unchanged weekly
// usage would become eligible to run. Short windows reset to full capacity at
// their fixed boundaries. The projection intentionally makes no claim about
// future interactive usage and returns nil when flat usage never qualifies.
func ProjectTriggerAt(in Input, pollInterval time.Duration) *time.Time {
	if pollInterval <= 0 || in.Now.IsZero() || !in.Snapshot.Weekly.ResetsAt.After(in.Now) {
		return nil
	}
	if result := Evaluate(in); result.Decision == Admit {
		projected := in.Now
		return &projected
	}
	horizon := effectiveReset(in).At
	for projected := in.Now.Add(pollInterval); projected.Before(horizon); projected = projected.Add(pollInterval) {
		future := projectInput(in, projected)
		if result := Evaluate(future); result.Decision == Admit {
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
		result.Decision = Admit
		result.Reason = "weekly remaining exceeds prorated short-window throughput"
		result.UnlockedTier = domain.DispatchExpiring
		return result
	}
	if in.PaceGapTrigger != nil && paceGap >= *in.PaceGapTrigger {
		result.Decision = Admit
		result.Reason = "weekly remaining is well behind pace"
		result.UnlockedTier = tierForPaceGap(paceGap)
		return result
	}
	if threshold := matchingPaceThreshold(in); threshold != nil {
		result.Decision = Admit
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
		result.Decision = Admit
		result.Reason = "weekly remaining is well behind pace"
		result.UnlockedTier = tierForPaceGap(paceGap)
		return result
	}
	if threshold := matchingPaceThreshold(in); threshold != nil {
		result.Decision = Admit
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

// weeklyPaceGap is allowance remaining minus period remaining, measured
// against the window's own period (a week unless the source says otherwise).
func weeklyPaceGap(weekly UsageWindow, now time.Time) float64 {
	timeRemaining := weekly.ResetsAt.Sub(now).Seconds() / weekly.Period().Seconds()
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
	if in.FiresaleTrigger != nil {
		if err := unitFraction("firesale trigger", *in.FiresaleTrigger); err != nil {
			return err
		}
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
