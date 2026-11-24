package decision

import (
	"fmt"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

// ModeCredits marks a decision about an OpenAI API credits balance.
const ModeCredits Mode = "api_credits"

// ResetCreditExpiry is a credit grant's expiry used as the reset horizon.
const ResetCreditExpiry ResetSource = "credit_expiry"

// Credits pacing, mirroring config's api_credits pacing values.
const (
	creditsPacingFiresale = "firesale"
	creditsPacingEven     = "even"
)

// defaultCreditsFiresaleWindow applies when a snapshot does not carry the
// account's window.
const defaultCreditsFiresaleWindow = 72 * time.Hour

// EvaluateCredits decides whether an API credits account may start work.
//
// Only promotional credit counts, and only above the user's dollar floor.
// Firesale pacing (the default) holds the balance until its soonest grant
// is inside the firesale window and then runs it down; even pacing runs
// whenever more is left than the share of the cycle still to go. A manual
// firesale overrides pacing, never the floor. Stale, future or expired data
// is unknown, so money is never spent against a guess.
func EvaluateCredits(in Input) Result {
	credits := in.Snapshot.Credits
	if credits == nil {
		return unknown("snapshot has no API credits balance")
	}
	if err := in.Snapshot.Validate(); err != nil {
		return unknown(err.Error())
	}
	switch {
	case in.Now.Sub(in.Snapshot.ObservedAt) > in.MaxSnapshotAge:
		return unknown("API credits balance is stale")
	case in.Snapshot.ObservedAt.After(in.Now):
		return unknown("API credits balance is from the future")
	case in.Snapshot.SourceStale:
		return unknown("API credits balance is out of date")
	}
	result := Result{Mode: ModeCredits, EffectiveResetSource: ResetCreditExpiry}
	if credits.PromoRemaining <= 0 || in.Snapshot.WeeklyUnavailable {
		result.Decision = Wait
		result.Reason = "no promotional API credit is available"
		return applyForcedFiresale(in, result, false)
	}
	expiry := in.Snapshot.Weekly.ResetsAt
	if !expiry.After(in.Now) {
		return unknown("API credit grant has expired; waiting for a new balance")
	}
	result.EffectiveResetAt = expiry
	result.PaceGap = weeklyPaceGap(in.Snapshot.Weekly, in.Now)
	floor := credits.MinPromoBalance
	if credits.PromoRemaining <= floor {
		result.Decision = Wait
		result.Reason = fmt.Sprintf("promotional credit is at or below the %s floor", dollars(floor))
		return applyForcedFiresale(in, result, false)
	}
	window := time.Duration(credits.FiresaleWindowSeconds) * time.Second
	if window <= 0 {
		window = defaultCreditsFiresaleWindow
	}
	left := expiry.Sub(in.Now)
	switch {
	case left <= window:
		result.Decision = Admit
		result.Firesale = true
		result.UnlockedTier = domain.DispatchFiresale
		result.Reason = fmt.Sprintf("%s of promotional credit expires in %s; running queued work before it lapses",
			dollars(credits.NextExpiring), roundDuration(left))
	case credits.Pacing == creditsPacingEven && result.PaceGap > 0:
		result.Decision = Admit
		result.UnlockedTier = tierForPaceGap(result.PaceGap)
		result.Reason = "promotional credit is behind an even pace for this cycle"
	case credits.Pacing == creditsPacingEven:
		result.Decision = Wait
		result.Reason = "promotional credit is on pace for this cycle"
	default:
		result.Decision = Wait
		result.Reason = fmt.Sprintf("holding promotional credit until it is within %s of expiring", roundDuration(window))
	}
	return applyForcedFiresale(in, result, true)
}

func dollars(amount float64) string { return fmt.Sprintf("$%.2f", amount) }

func roundDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d minutes", max(int(d.Minutes()), 1))
	}
}
