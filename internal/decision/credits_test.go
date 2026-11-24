package decision_test

import (
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
)

// creditsSnapshot is a $100 grant over 30 days with expiresIn left and
// remaining dollars unspent.
func creditsSnapshot(remaining float64, expiresIn time.Duration, pacing string) decision.UsageSnapshot {
	expiry := now.Add(expiresIn)
	start := expiry.Add(-30 * 24 * time.Hour)
	return decision.UsageSnapshot{
		Provider: "openai-api-credits", ObservedAt: now.Add(-time.Minute), Source: "native",
		Weekly: decision.UsageWindow{Remaining: remaining / 100, ResetsAt: expiry, PeriodSeconds: int64(30 * 24 * time.Hour / time.Second)},
		Credits: &decision.CreditsBalance{PromoRemaining: remaining, PromoGranted: 100, NextExpiring: remaining,
			EffectiveAt: &start, ExpiresAt: &expiry, MinPromoBalance: 5, Pacing: pacing,
			FiresaleWindowSeconds: int64(72 * time.Hour / time.Second)},
	}
}

func creditsInput(s decision.UsageSnapshot) decision.Input {
	in := input(s)
	in.WindowWeeklyCost = 0.05
	return in
}

// Firesale pacing (the default) holds money until the grant is close to
// expiry, then spends what is left.
func TestCreditsFiresaleWaitsUntilNearExpiry(t *testing.T) {
	early := decision.EvaluateCredits(creditsInput(creditsSnapshot(80, 10*24*time.Hour, "firesale")))
	if early.Decision != decision.Wait || !strings.Contains(early.Reason, "expir") {
		t.Fatalf("early = %s: %s", early.Decision, early.Reason)
	}
	late := decision.EvaluateCredits(creditsInput(creditsSnapshot(80, 48*time.Hour, "firesale")))
	if late.Decision != decision.Admit || !late.Firesale || late.UnlockedTier != domain.DispatchFiresale {
		t.Fatalf("late = %+v", late)
	}
	if !late.EffectiveResetAt.Equal(now.Add(48*time.Hour)) || late.EffectiveResetSource != decision.ResetCreditExpiry {
		t.Fatalf("reset = %v %s", late.EffectiveResetAt, late.EffectiveResetSource)
	}
}

// Even pacing runs whenever more money is left than the share of the cycle
// still to go, and unlocks higher tiers the further behind it is.
func TestCreditsEvenPacingRunsWhenBehind(t *testing.T) {
	// 15 of 30 days left (50%) with $80 (80%) unspent: 30 points behind.
	behind := decision.EvaluateCredits(creditsInput(creditsSnapshot(80, 15*24*time.Hour, "even")))
	if behind.Decision != decision.Admit || behind.UnlockedTier != domain.DispatchExpiring {
		t.Fatalf("behind = %+v", behind)
	}
	// 15 of 30 days left with $40 unspent: ahead of pace.
	ahead := decision.EvaluateCredits(creditsInput(creditsSnapshot(40, 15*24*time.Hour, "even")))
	if ahead.Decision != decision.Wait {
		t.Fatalf("ahead = %+v", ahead)
	}
	// Even pacing still runs everything that is left in the last stretch.
	last := decision.EvaluateCredits(creditsInput(creditsSnapshot(40, 24*time.Hour, "even")))
	if last.Decision != decision.Admit || !last.Firesale {
		t.Fatalf("last = %+v", last)
	}
}

// The floor is a dollar amount the user chose. At or below it nothing new
// starts, however close the expiry.
func TestCreditsFloorHoldsWork(t *testing.T) {
	result := decision.EvaluateCredits(creditsInput(creditsSnapshot(5, time.Hour, "firesale")))
	if result.Decision != decision.Wait || !strings.Contains(result.Reason, "$5.00") {
		t.Fatalf("result = %s: %s", result.Decision, result.Reason)
	}
	above := decision.EvaluateCredits(creditsInput(creditsSnapshot(5.5, time.Hour, "firesale")))
	if above.Decision != decision.Admit {
		t.Fatalf("above = %s: %s", above.Decision, above.Reason)
	}
}

// No promotional credit is a wait, not unknown: the number is known to be
// zero.
func TestCreditsWithoutPromoWait(t *testing.T) {
	snapshot := decision.UsageSnapshot{Provider: "openai-api-credits", ObservedAt: now.Add(-time.Minute), Source: "native",
		WeeklyUnavailable: true, Credits: &decision.CreditsBalance{PaidAvailable: 20, MinPromoBalance: 5}}
	result := decision.EvaluateCredits(creditsInput(snapshot))
	if result.Decision != decision.Wait || !strings.Contains(result.Reason, "no promotional") {
		t.Fatalf("result = %s: %s", result.Decision, result.Reason)
	}
}

// Stale or future data, or a snapshot that is not a credits snapshot, is
// unknown: never spend money against a guess.
func TestCreditsFailClosed(t *testing.T) {
	stale := creditsSnapshot(80, time.Hour, "firesale")
	stale.ObservedAt = now.Add(-time.Hour)
	if got := decision.EvaluateCredits(creditsInput(stale)); got.Decision != decision.Unknown {
		t.Fatalf("stale = %+v", got)
	}
	future := creditsSnapshot(80, time.Hour, "firesale")
	future.ObservedAt = now.Add(time.Minute)
	if got := decision.EvaluateCredits(creditsInput(future)); got.Decision != decision.Unknown {
		t.Fatalf("future = %+v", got)
	}
	expired := creditsSnapshot(80, -time.Minute, "firesale")
	if got := decision.EvaluateCredits(creditsInput(expired)); got.Decision != decision.Unknown {
		t.Fatalf("expired = %+v", got)
	}
	plain := creditsSnapshot(80, time.Hour, "firesale")
	plain.Credits = nil
	if got := decision.EvaluateCredits(creditsInput(plain)); got.Decision != decision.Unknown {
		t.Fatalf("no credits = %+v", got)
	}
}

// A manual firesale overrides pacing but never the floor.
func TestCreditsManualFiresaleRespectsFloor(t *testing.T) {
	in := creditsInput(creditsSnapshot(80, 20*24*time.Hour, "firesale"))
	in.ForceFiresale, in.ForceFiresaleTrigger = true, "manual"
	if got := decision.EvaluateCredits(in); got.Decision != decision.Admit || !got.Firesale {
		t.Fatalf("manual = %+v", got)
	}
	in = creditsInput(creditsSnapshot(4, 20*24*time.Hour, "firesale"))
	in.ForceFiresale, in.ForceFiresaleTrigger = true, "manual"
	if got := decision.EvaluateCredits(in); got.Decision != decision.Wait {
		t.Fatalf("manual below floor = %+v", got)
	}
}

// Evaluate routes credits snapshots to the credits rules, so every caller
// (dispatch, dashboard projections) gets the same answer.
func TestEvaluateRoutesCreditsSnapshots(t *testing.T) {
	in := creditsInput(creditsSnapshot(80, 10*24*time.Hour, "firesale"))
	in.PaceGapTrigger = new(float64) // would admit any subscription window
	if got := decision.Evaluate(in); got.Decision != decision.Wait || got.Mode != decision.ModeCredits {
		t.Fatalf("evaluate = %+v", got)
	}
}
