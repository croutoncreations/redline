package launchmetrics

import (
	"testing"
	"time"

	"github.com/jfox/redline/internal/accounting"
	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/domain"
)

func TestBuildCountsAutomaticDecisionsAndTerminalRuns(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-time.Hour)
	failed := now.Add(-2 * time.Hour)
	input := Input{
		Since: now.Add(-7 * 24 * time.Hour), Until: now,
		Attempts: []domain.DispatchAttempt{
			{Trigger: "automatic", ProviderAccountID: "claude", Outcome: domain.DispatchWait, Decision: "WAIT"},
			{Trigger: "automatic", ProviderAccountID: "claude", Outcome: domain.DispatchNoTask, Decision: "RUN"},
			{Trigger: "automatic", ProviderAccountID: "claude", Outcome: domain.DispatchWait, Decision: "UNKNOWN"},
			{Trigger: "automatic", ProviderAccountID: "claude", Outcome: domain.DispatchAdmitted, Decision: "RUN", RunID: "ok"},
			{Trigger: "automatic", ProviderAccountID: "claude", Outcome: domain.DispatchAdmitted, Decision: "RUN", RunID: "bad"},
			{Trigger: "manual", ProviderAccountID: "claude", Outcome: domain.DispatchWait, Decision: "WAIT"},
		},
		Runs: map[string]domain.Run{
			"ok":  {ID: "ok", ProviderAccountID: "claude", State: domain.RunCompleted, CompletedAt: &completed},
			"bad": {ID: "bad", ProviderAccountID: "claude", State: domain.RunFailed, CompletedAt: &failed},
		},
	}
	report := Build(input)
	if report.Decisions.AutomaticChecks != 5 || report.Decisions.Run != 3 || report.Decisions.Wait != 1 || report.Decisions.Unknown != 1 {
		t.Fatalf("decision counts = %#v", report.Decisions)
	}
	if report.Decisions.WaitRate != .2 {
		t.Fatalf("wait rate = %v", report.Decisions.WaitRate)
	}
	if report.Jobs.Admitted != 2 || report.Jobs.Completed != 1 || report.Jobs.Failed != 1 {
		t.Fatalf("job counts = %#v", report.Jobs)
	}
}

func TestBuildEstimatesCompletedAllowanceAndOverflowCapacity(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-time.Hour)
	input := Input{
		Since: now.Add(-14 * 24 * time.Hour), Until: now,
		Attempts: []domain.DispatchAttempt{{
			Trigger: "automatic", ProviderAccountID: "claude", Outcome: domain.DispatchAdmitted,
			Decision: "RUN", Mode: "window_slots", RunID: "run-1",
		}},
		Runs: map[string]domain.Run{"run-1": {
			ID: "run-1", ProviderAccountID: "claude", State: domain.RunCompleted, CompletedAt: &completed,
		}},
		RunObservations: map[string][]capacity.TokenObservation{"run-1": {{
			Provider: "claude", Source: "redline-run", SourceID: "run-1:claude-sonnet-5",
			ObservedAt: completed, Model: "claude-sonnet-5", InputTokens: 1_000_000,
		}}},
		Capacity: map[string]ProviderCapacity{"claude": {
			Confidence: capacity.ConfidenceMedium,
			Accounting: &capacity.AccountingEstimate{
				Unit: accounting.UnitUSDAPIEquivalent, EstimatedCapacityLow: 20, EstimatedCapacityHigh: 40,
				PricingCoverage: 1,
			},
		}},
	}
	report := Build(input)
	provider := report.Providers[0]
	// Sonnet 5 introductory input pricing is $2/M: $2 / $40 to $2 / $20.
	if provider.Allowance.CompletedWeeklyEquivalentLow != .05 || provider.Allowance.CompletedWeeklyEquivalentHigh != .1 {
		t.Fatalf("completed allowance = %#v", provider.Allowance)
	}
	if provider.Allowance.ConversionRateLow != .025 || provider.Allowance.ConversionRateHigh != .05 {
		t.Fatalf("two-week conversion rate = %#v", provider.Allowance)
	}
	if provider.Allowance.ConversionPercentLow != 2.5 || provider.Allowance.ConversionPercentHigh != 5 ||
		provider.Allowance.Status != StatusAvailable {
		t.Fatalf("conversion percent/status = %#v", provider.Allowance)
	}
	if provider.Reclaimed.CompletedJobs != 1 || provider.Reclaimed.WeeklyEquivalentLow != .05 {
		t.Fatalf("reclaimed = %#v", provider.Reclaimed)
	}
}

func TestBuildMarksAllowanceUnavailableWithoutCalibration(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-time.Hour)
	report := Build(Input{
		Since: now.Add(-7 * 24 * time.Hour), Until: now,
		Attempts: []domain.DispatchAttempt{{Trigger: "automatic", ProviderAccountID: "codex", Outcome: domain.DispatchAdmitted, Decision: "RUN", RunID: "r"}},
		Runs:     map[string]domain.Run{"r": {ID: "r", ProviderAccountID: "codex", State: domain.RunCompleted, CompletedAt: &completed}},
	})
	if got := report.Providers[0].Allowance.Status; got != StatusUnavailable {
		t.Fatalf("status = %q", got)
	}
	if report.Providers[0].Allowance.Caveat == "" {
		t.Fatal("missing unavailable caveat")
	}
}
