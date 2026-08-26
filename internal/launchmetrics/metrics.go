// Package launchmetrics turns Redline's persisted dispatch, run, and token
// evidence into auditable product metrics. Exact counters and empirical
// allowance estimates are deliberately kept separate.
package launchmetrics

import (
	"sort"
	"strings"
	"time"

	"github.com/jfox/redline/internal/accounting"
	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/domain"
)

type Status string

const (
	StatusAvailable   Status = "available"
	StatusPartial     Status = "partial"
	StatusUnavailable Status = "unavailable"
)

type ProviderCapacity struct {
	UsageProvider     string
	Confidence        capacity.Confidence
	Accounting        *capacity.AccountingEstimate
	UnavailableReason string
}

type Input struct {
	Since           time.Time
	Until           time.Time
	Providers       []string
	Attempts        []domain.DispatchAttempt
	Runs            map[string]domain.Run
	RunObservations map[string][]capacity.TokenObservation
	Capacity        map[string]ProviderCapacity
}

type DecisionMetrics struct {
	AutomaticChecks int     `json:"automatic_checks"`
	Run             int     `json:"run"`
	Wait            int     `json:"wait"`
	Unknown         int     `json:"unknown"`
	Errors          int     `json:"errors"`
	NoEligibleTask  int     `json:"no_eligible_task"`
	WaitRate        float64 `json:"wait_rate"`
}

type JobMetrics struct {
	Admitted       int     `json:"admitted"`
	Completed      int     `json:"completed"`
	Failed         int     `json:"failed"`
	Active         int     `json:"active"`
	CompletionRate float64 `json:"completion_rate"`
}

type AllowanceMetrics struct {
	Status                        Status              `json:"status"`
	CompletedJobs                 int                 `json:"completed_jobs"`
	JobsWithUsageEvidence         int                 `json:"jobs_with_usage_evidence"`
	JobsWithFullyPricedEvidence   int                 `json:"jobs_with_fully_priced_evidence"`
	EvidenceCoverage              float64             `json:"evidence_coverage"`
	PricingCoverage               float64             `json:"pricing_coverage"`
	CalibrationPricingCoverage    float64             `json:"calibration_pricing_coverage"`
	EntitlementPeriods            float64             `json:"entitlement_periods"`
	CompletedWeeklyEquivalentLow  float64             `json:"completed_weekly_equivalent_low"`
	CompletedWeeklyEquivalentHigh float64             `json:"completed_weekly_equivalent_high"`
	ConversionRateLow             float64             `json:"conversion_rate_low"`
	ConversionRateHigh            float64             `json:"conversion_rate_high"`
	ConversionPercentLow          float64             `json:"conversion_percent_low"`
	ConversionPercentHigh         float64             `json:"conversion_percent_high"`
	Unit                          accounting.Unit     `json:"accounting_unit,omitempty"`
	CalibrationConfidence         capacity.Confidence `json:"calibration_confidence"`
	Caveat                        string              `json:"caveat"`
}

type ReclaimedMetrics struct {
	CompletedJobs        int     `json:"completed_jobs"`
	JobsWithEvidence     int     `json:"jobs_with_usage_evidence"`
	WeeklyEquivalentLow  float64 `json:"weekly_equivalent_low"`
	WeeklyEquivalentHigh float64 `json:"weekly_equivalent_high"`
	Caveat               string  `json:"caveat"`
}

type ProviderMetrics struct {
	Provider  string           `json:"provider"`
	Decisions DecisionMetrics  `json:"decisions"`
	Jobs      JobMetrics       `json:"jobs"`
	Allowance AllowanceMetrics `json:"allowance_conversion"`
	Reclaimed ReclaimedMetrics `json:"capacity_reclaimed"`
}

type Report struct {
	Since       time.Time         `json:"since"`
	Until       time.Time         `json:"until"`
	Decisions   DecisionMetrics   `json:"decisions"`
	Jobs        JobMetrics        `json:"jobs"`
	Providers   []ProviderMetrics `json:"providers"`
	Methodology []string          `json:"methodology"`
}

type providerAccumulator struct {
	metrics       ProviderMetrics
	completedRuns map[string]domain.DispatchAttempt
}

func Build(in Input) Report {
	report := Report{Since: in.Since.UTC(), Until: in.Until.UTC(), Methodology: []string{
		"Decision rates include automatic scheduler checks only; manual evaluations and executions are excluded.",
		"Allowance conversion uses run-scoped token evidence and the empirical weekly bucket calibration; unavailable evidence is never imputed.",
		"Capacity reclaimed includes completed jobs admitted by the discrete window-slots overflow rule. It is measured managed usage, not a counterfactual claim that all usage would otherwise have expired.",
	}}
	providers := map[string]*providerAccumulator{}
	get := func(provider string) *providerAccumulator {
		item := providers[provider]
		if item == nil {
			item = &providerAccumulator{metrics: ProviderMetrics{Provider: provider}, completedRuns: map[string]domain.DispatchAttempt{}}
			providers[provider] = item
		}
		return item
	}
	for _, provider := range in.Providers {
		get(provider)
	}
	for _, attempt := range in.Attempts {
		if attempt.Trigger != "automatic" {
			continue
		}
		item := get(attempt.ProviderAccountID)
		countDecision(&report.Decisions, attempt)
		countDecision(&item.metrics.Decisions, attempt)
		if attempt.Outcome != domain.DispatchAdmitted || attempt.RunID == "" {
			continue
		}
		report.Jobs.Admitted++
		item.metrics.Jobs.Admitted++
		run, ok := in.Runs[attempt.RunID]
		if !ok {
			continue
		}
		switch run.State {
		case domain.RunCompleted:
			if run.CompletedAt != nil && !run.CompletedAt.Before(in.Since) && run.CompletedAt.Before(in.Until) {
				report.Jobs.Completed++
				item.metrics.Jobs.Completed++
				item.completedRuns[run.ID] = attempt
			} else {
				report.Jobs.Active++
				item.metrics.Jobs.Active++
			}
		case domain.RunFailed:
			if run.CompletedAt != nil && !run.CompletedAt.Before(in.Since) && run.CompletedAt.Before(in.Until) {
				report.Jobs.Failed++
				item.metrics.Jobs.Failed++
			} else {
				report.Jobs.Active++
				item.metrics.Jobs.Active++
			}
		case domain.RunPreparing, domain.RunRunning:
			report.Jobs.Active++
			item.metrics.Jobs.Active++
		}
	}
	finalizeJobs(&report.Jobs)
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		item := providers[name]
		finalizeJobs(&item.metrics.Jobs)
		item.metrics.Allowance, item.metrics.Reclaimed = estimateAllowance(
			in, name, item.completedRuns,
		)
		report.Providers = append(report.Providers, item.metrics)
	}
	return report
}

func countDecision(metrics *DecisionMetrics, attempt domain.DispatchAttempt) {
	metrics.AutomaticChecks++
	switch attempt.Decision {
	case "RUN":
		metrics.Run++
	case "WAIT":
		metrics.Wait++
	case "UNKNOWN":
		metrics.Unknown++
	}
	if attempt.Outcome == domain.DispatchError {
		metrics.Errors++
	}
	if attempt.Outcome == domain.DispatchNoTask {
		metrics.NoEligibleTask++
	}
	denominator := metrics.Run + metrics.Wait + metrics.Unknown
	if denominator > 0 {
		metrics.WaitRate = float64(metrics.Wait) / float64(denominator)
	}
}

func finalizeJobs(metrics *JobMetrics) {
	terminal := metrics.Completed + metrics.Failed
	if terminal > 0 {
		metrics.CompletionRate = float64(metrics.Completed) / float64(terminal)
	}
}

func estimateAllowance(in Input, provider string, completed map[string]domain.DispatchAttempt) (AllowanceMetrics, ReclaimedMetrics) {
	allowance := AllowanceMetrics{CompletedJobs: len(completed), Status: StatusUnavailable,
		Caveat: "A weekly capacity calibration and priced run-scoped token evidence are required."}
	reclaimed := ReclaimedMetrics{Caveat: "Only discrete window-slots admissions are included; pace-threshold admissions are excluded."}
	period := in.Until.Sub(in.Since).Hours() / (7 * 24)
	if period > 0 {
		allowance.EntitlementPeriods = period
	}
	calibrated := in.Capacity[provider]
	allowance.CalibrationConfidence = calibrated.Confidence
	if calibrated.Accounting == nil || calibrated.Accounting.EstimatedCapacityLow <= 0 || calibrated.Accounting.EstimatedCapacityHigh <= 0 {
		if calibrated.UnavailableReason != "" {
			allowance.Caveat = "Weekly capacity estimate unavailable: " + calibrated.UnavailableReason
		}
		return allowance, reclaimed
	}
	allowance.Unit = calibrated.Accounting.Unit
	allowance.CalibrationPricingCoverage = calibrated.Accounting.PricingCoverage
	for runID, attempt := range completed {
		if attempt.Mode == "window_slots" {
			reclaimed.CompletedJobs++
		}
		usageProvider := calibrated.UsageProvider
		if usageProvider == "" {
			usageProvider = provider
		}
		low, high, ok, fullyPriced := pricedRunUsage(usageProvider, calibrated.Accounting.Unit, in.RunObservations[runID])
		if !ok {
			continue
		}
		fractionLow := low / calibrated.Accounting.EstimatedCapacityHigh
		fractionHigh := high / calibrated.Accounting.EstimatedCapacityLow
		allowance.JobsWithUsageEvidence++
		if fullyPriced {
			allowance.JobsWithFullyPricedEvidence++
		}
		allowance.CompletedWeeklyEquivalentLow += fractionLow
		allowance.CompletedWeeklyEquivalentHigh += fractionHigh
		if attempt.Mode == "window_slots" {
			reclaimed.JobsWithEvidence++
			reclaimed.WeeklyEquivalentLow += fractionLow
			reclaimed.WeeklyEquivalentHigh += fractionHigh
		}
	}
	if allowance.CompletedJobs > 0 {
		allowance.EvidenceCoverage = float64(allowance.JobsWithUsageEvidence) / float64(allowance.CompletedJobs)
	}
	if allowance.JobsWithUsageEvidence > 0 {
		allowance.PricingCoverage = float64(allowance.JobsWithFullyPricedEvidence) / float64(allowance.JobsWithUsageEvidence)
	}
	if allowance.JobsWithUsageEvidence == 0 {
		return allowance, reclaimed
	}
	allowance.Status = StatusAvailable
	if allowance.JobsWithUsageEvidence < allowance.CompletedJobs ||
		allowance.JobsWithFullyPricedEvidence < allowance.JobsWithUsageEvidence ||
		calibrated.Accounting.PricingCoverage < .999 ||
		calibrated.Confidence == capacity.ConfidenceLow || calibrated.Confidence == capacity.ConfidenceInsufficient {
		allowance.Status = StatusPartial
	}
	if period > 0 {
		allowance.ConversionRateLow = allowance.CompletedWeeklyEquivalentLow / period
		allowance.ConversionRateHigh = allowance.CompletedWeeklyEquivalentHigh / period
		allowance.ConversionPercentLow = allowance.ConversionRateLow * 100
		allowance.ConversionPercentHigh = allowance.ConversionRateHigh * 100
	}
	allowance.Caveat = "Empirical estimate. The range reflects model/cache pricing uncertainty and weekly bucket calibration; coverage reports how many completed jobs had usable run evidence."
	return allowance, reclaimed
}

func pricedRunUsage(provider string, unit accounting.Unit, observations []capacity.TokenObservation) (float64, float64, bool, bool) {
	var low, high float64
	priced := false
	fullyPriced := true
	relevant := false
	for _, observation := range observations {
		if observation.Provider != provider || observation.Source != "redline-run" {
			continue
		}
		relevant = true
		quote := accounting.Quote(accounting.Usage{Provider: observation.Provider, Model: observation.Model,
			Source: observation.Source, InputTokens: observation.InputTokens, OutputTokens: observation.OutputTokens,
			CacheReadTokens: observation.CacheReadTokens, CacheCreationTokens: observation.CacheCreationTokens,
		}, observation.ObservedAt)
		if !quote.Priced || quote.Unit != unit || strings.TrimSpace(observation.Model) == "" {
			fullyPriced = false
			continue
		}
		low += quote.Low
		high += quote.High
		priced = true
	}
	return low, high, priced, relevant && fullyPriced
}
