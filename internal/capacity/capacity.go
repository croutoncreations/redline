// Package capacity estimates provider allowance bucket sizes by correlating
// local token observations with changes in provider-reported percentages.
// Estimates are empirical processed-token equivalents, not provider billing.
package capacity

import (
	"math"
	"sort"
	"time"

	"github.com/jfox/redline/internal/decision"
)

type Confidence string

const (
	ConfidenceInsufficient Confidence = "insufficient"
	ConfidenceLow          Confidence = "low"
	ConfidenceMedium       Confidence = "medium"
	ConfidenceHigh         Confidence = "high"
)

type TokenObservation struct {
	Provider            string    `json:"provider"`
	Source              string    `json:"source"`
	SourceID            string    `json:"source_id"`
	ObservedAt          time.Time `json:"observed_at"`
	Model               string    `json:"model,omitempty"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	Confidence          string    `json:"confidence,omitempty"`
}

func (o TokenObservation) Tokens() TokenBreakdown {
	return TokenBreakdown{
		Input: float64(o.InputTokens), Output: float64(o.OutputTokens),
		CacheRead: float64(o.CacheReadTokens), CacheCreation: float64(o.CacheCreationTokens),
		Total: float64(o.InputTokens + o.OutputTokens + o.CacheReadTokens + o.CacheCreationTokens),
	}
}

type TokenBreakdown struct {
	Input         float64 `json:"input"`
	Output        float64 `json:"output"`
	CacheRead     float64 `json:"cache_read"`
	CacheCreation float64 `json:"cache_creation"`
	Total         float64 `json:"total"`
}

func (b *TokenBreakdown) add(other TokenBreakdown) {
	b.Input += other.Input
	b.Output += other.Output
	b.CacheRead += other.CacheRead
	b.CacheCreation += other.CacheCreation
	b.Total += other.Total
}

func (b TokenBreakdown) scaled(scale float64) TokenBreakdown {
	return TokenBreakdown{Input: b.Input * scale, Output: b.Output * scale,
		CacheRead: b.CacheRead * scale, CacheCreation: b.CacheCreation * scale, Total: b.Total * scale}
}

type WindowEstimate struct {
	Window              string         `json:"window"`
	EstimatedTokens     TokenBreakdown `json:"estimated_tokens"`
	MeasuredTokens      TokenBreakdown `json:"measured_tokens"`
	ObservedUsage       float64        `json:"observed_usage"`
	ClosedSpans         int            `json:"closed_spans"`
	Confidence          Confidence     `json:"confidence"`
	TokensPerOnePercent float64        `json:"tokens_per_one_percent"`
}

type EstimateResult struct {
	Provider                 string          `json:"provider"`
	Short                    *WindowEstimate `json:"short,omitempty"`
	Weekly                   *WindowEstimate `json:"weekly,omitempty"`
	WindowWeeklyCost         float64         `json:"window_weekly_cost"`
	RatioDerivedWeeklyTokens *float64        `json:"ratio_derived_weekly_tokens,omitempty"`
	Confidence               Confidence      `json:"confidence"`
	SnapshotCount            int             `json:"snapshot_count"`
	TokenObservationCount    int             `json:"token_observation_count"`
	CalculatedAt             time.Time       `json:"calculated_at"`
	Caveat                   string          `json:"caveat"`
}

// Estimate correlates tokens occurring between snapshots with a subsequently
// visible percentage decrease. Zero-change snapshots accumulate evidence until
// the provider's quantized percentage moves. Reset cohorts are never crossed.
func Estimate(provider string, snapshots []decision.UsageSnapshot, observations []TokenObservation, windowWeeklyCost float64, now time.Time) EstimateResult {
	result := EstimateResult{Provider: provider, WindowWeeklyCost: windowWeeklyCost,
		Confidence: ConfidenceInsufficient, SnapshotCount: len(snapshots), CalculatedAt: now.UTC(),
		Caveat: "Empirical processed-token equivalent from local logs; provider accounting may weight models and cache tokens differently.",
	}
	filtered := make([]TokenObservation, 0, len(observations))
	for _, observation := range observations {
		if observation.Provider == provider && observation.ObservedAt.After(time.Time{}) {
			filtered = append(filtered, observation)
		}
	}
	result.TokenObservationCount = len(filtered)
	result.Short = estimateWindow("short", provider, snapshots, filtered)
	result.Weekly = estimateWindow("weekly", provider, snapshots, filtered)
	if result.Short != nil && windowWeeklyCost > 0 {
		derived := result.Short.EstimatedTokens.Total / windowWeeklyCost
		if finitePositive(derived) {
			result.RatioDerivedWeeklyTokens = &derived
		}
	}
	result.Confidence = combinedConfidence(result.Short, result.Weekly)
	return result
}

type point struct {
	at        time.Time
	remaining float64
	reset     time.Time
}

func estimateWindow(kind, provider string, snapshots []decision.UsageSnapshot, observations []TokenObservation) *WindowEstimate {
	groups := make(map[int64][]point)
	for _, snapshot := range snapshots {
		if snapshot.Provider != provider {
			continue
		}
		var remaining float64
		var reset time.Time
		if kind == "short" {
			if snapshot.Short == nil {
				continue
			}
			remaining, reset = snapshot.Short.Remaining, snapshot.Short.ResetsAt
		} else {
			remaining, reset = snapshot.Weekly.Remaining, snapshot.Weekly.ResetsAt
		}
		key := reset.UTC().Round(time.Minute).Unix()
		groups[key] = append(groups[key], point{at: snapshot.ObservedAt, remaining: remaining, reset: reset})
	}

	var measured TokenBreakdown
	var usage float64
	spans := 0
	for _, points := range groups {
		sort.Slice(points, func(i, j int) bool { return points[i].at.Before(points[j].at) })
		if len(points) < 2 {
			continue
		}
		baseline := points[0]
		for _, current := range points[1:] {
			drain := baseline.remaining - current.remaining
			if drain < -0.000001 { // correction/reversal: start a fresh correlation span
				baseline = current
				continue
			}
			if drain <= 0.000001 {
				continue
			}
			spanTokens := tokensBetween(observations, baseline.at, current.at)
			if spanTokens.Total > 0 {
				measured.add(spanTokens)
				usage += drain
				spans++
			}
			baseline = current
		}
	}
	if spans == 0 || usage <= 0 || measured.Total <= 0 {
		return nil
	}
	estimated := measured.scaled(1 / usage)
	if !finitePositive(estimated.Total) {
		return nil
	}
	confidence := ConfidenceLow
	if spans >= 5 && usage >= .10 {
		confidence = ConfidenceMedium
	}
	if spans >= 12 && usage >= .25 {
		confidence = ConfidenceHigh
	}
	// A statistical sample cannot be more trustworthy than its local source.
	// Gatepost's normalized session index is medium confidence because it is
	// machine-local and collapses cached context into input-like tokens.
	sourceCap := observationConfidenceCap(observations)
	if confidenceRank(confidence) > confidenceRank(sourceCap) {
		confidence = sourceCap
	}
	return &WindowEstimate{Window: kind, EstimatedTokens: estimated, MeasuredTokens: measured,
		ObservedUsage: usage, ClosedSpans: spans, Confidence: confidence,
		TokensPerOnePercent: estimated.Total / 100}
}

func tokensBetween(observations []TokenObservation, after, through time.Time) TokenBreakdown {
	var result TokenBreakdown
	for _, observation := range observations {
		if observation.ObservedAt.After(after) && !observation.ObservedAt.After(through) {
			result.add(observation.Tokens())
		}
	}
	return result
}

func combinedConfidence(short, weekly *WindowEstimate) Confidence {
	best := ConfidenceInsufficient
	for _, estimate := range []*WindowEstimate{short, weekly} {
		if estimate != nil && confidenceRank(estimate.Confidence) > confidenceRank(best) {
			best = estimate.Confidence
		}
	}
	return best
}

func confidenceRank(c Confidence) int {
	switch c {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	case ConfidenceLow:
		return 1
	default:
		return 0
	}
}

func observationConfidenceCap(observations []TokenObservation) Confidence {
	cap := ConfidenceHigh
	for _, observation := range observations {
		var current Confidence
		switch observation.Confidence {
		case "high":
			current = ConfidenceHigh
		case "medium":
			current = ConfidenceMedium
		default:
			current = ConfidenceLow
		}
		if confidenceRank(current) < confidenceRank(cap) {
			cap = current
		}
	}
	return cap
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
