package calibration

import (
	"math"
	"time"

	"github.com/jfox/redline/internal/decision"
)

type Confidence string
type Source string

const (
	ConfidenceInsufficient Confidence = "insufficient"
	ConfidenceLow          Confidence = "low"
	ConfidenceMedium       Confidence = "medium"
	ConfidenceHigh         Confidence = "high"

	SourceConfigured Source = "configured"
	SourceObserved   Source = "observed"
)

type Estimate struct {
	Provider           string     `json:"provider"`
	ConfiguredCost     float64    `json:"configured_cost"`
	ObservedCost       *float64   `json:"observed_cost,omitempty"`
	EffectiveCost      float64    `json:"effective_cost"`
	Source             Source     `json:"source"`
	Confidence         Confidence `json:"confidence"`
	SnapshotCount      int        `json:"snapshot_count"`
	InformativeWindows int        `json:"informative_windows"`
	TotalShortUsage    float64    `json:"total_short_usage"`
	TotalWeeklyUsage   float64    `json:"total_weekly_usage"`
	CalculatedAt       time.Time  `json:"calculated_at"`
}

type windowKey struct {
	shortReset  int64
	weeklyReset int64
}

type windowEvidence struct {
	count                int
	minShort, maxShort   float64
	minWeekly, maxWeekly float64
}

func EstimateWindowWeeklyCost(
	provider string,
	snapshots []decision.UsageSnapshot,
	configuredCost float64,
	now time.Time,
) Estimate {
	estimate := Estimate{
		Provider: provider, ConfiguredCost: configuredCost, EffectiveCost: configuredCost,
		Source: SourceConfigured, Confidence: ConfidenceInsufficient,
		SnapshotCount: len(snapshots), CalculatedAt: now.UTC(),
	}
	windows := make(map[windowKey]*windowEvidence)
	for _, snapshot := range snapshots {
		if snapshot.Short == nil || snapshot.Provider != provider {
			continue
		}
		key := windowKey{
			shortReset:  snapshot.Short.ResetsAt.UTC().Round(time.Minute).Unix(),
			weeklyReset: snapshot.Weekly.ResetsAt.UTC().Round(time.Minute).Unix(),
		}
		evidence := windows[key]
		if evidence == nil {
			evidence = &windowEvidence{
				minShort: snapshot.Short.Remaining, maxShort: snapshot.Short.Remaining,
				minWeekly: snapshot.Weekly.Remaining, maxWeekly: snapshot.Weekly.Remaining,
			}
			windows[key] = evidence
		}
		evidence.count++
		evidence.minShort = math.Min(evidence.minShort, snapshot.Short.Remaining)
		evidence.maxShort = math.Max(evidence.maxShort, snapshot.Short.Remaining)
		evidence.minWeekly = math.Min(evidence.minWeekly, snapshot.Weekly.Remaining)
		evidence.maxWeekly = math.Max(evidence.maxWeekly, snapshot.Weekly.Remaining)
	}

	for _, evidence := range windows {
		if evidence.count < 2 {
			continue
		}
		shortUsage := evidence.maxShort - evidence.minShort
		weeklyUsage := evidence.maxWeekly - evidence.minWeekly
		if shortUsage < 0.05 || weeklyUsage <= 0 {
			continue
		}
		estimate.InformativeWindows++
		estimate.TotalShortUsage += shortUsage
		estimate.TotalWeeklyUsage += weeklyUsage
	}
	if estimate.InformativeWindows == 0 || estimate.TotalShortUsage <= 0 {
		return estimate
	}
	observed := estimate.TotalWeeklyUsage / estimate.TotalShortUsage
	if observed <= 0 || observed > 1 || math.IsNaN(observed) || math.IsInf(observed, 0) {
		return estimate
	}
	estimate.ObservedCost = &observed
	estimate.Confidence = ConfidenceLow
	if estimate.InformativeWindows >= 2 && estimate.TotalShortUsage >= 1.0 {
		estimate.Confidence = ConfidenceMedium
		estimate.EffectiveCost = observed
		estimate.Source = SourceObserved
	}
	if estimate.InformativeWindows >= 4 && estimate.TotalShortUsage >= 2.0 {
		estimate.Confidence = ConfidenceHigh
	}
	return estimate
}
