// Package accounting converts immutable token observations into versioned,
// provider-native allowance proxies. Codex uses official subscription credits;
// Claude uses current API-dollar-equivalent pricing as an explicit proxy.
package accounting

import (
	"strings"
	"time"
)

type Unit string
type Quality string

const (
	UnitCodexCredits     Unit    = "codex_credits"
	UnitUSDAPIEquivalent Unit    = "usd_api_equivalent"
	QualityExact         Quality = "exact_token_classes"
	QualityRange         Quality = "collapsed_context_range"
)

type Usage struct {
	Provider            string
	Model               string
	Source              string
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

type QuoteResult struct {
	Priced          bool    `json:"priced"`
	Unit            Unit    `json:"unit,omitempty"`
	Low             float64 `json:"low"`
	High            float64 `json:"high"`
	Quality         Quality `json:"quality,omitempty"`
	RateCardVersion string  `json:"rate_card_version,omitempty"`
	Reason          string  `json:"reason,omitempty"`
}

type rate struct {
	unit                          Unit
	version                       string
	input, cacheRead, output      float64
	cacheWriteLow, cacheWriteHigh float64
}

// Quote applies rates per million tokens. The current cards are intentionally
// explicit and versioned; raw observations remain unchanged when cards evolve.
func Quote(usage Usage, observedAt time.Time) QuoteResult {
	rate, ok := lookupRate(usage.Provider, usage.Model, observedAt)
	if !ok {
		return QuoteResult{Reason: "unknown model"}
	}
	perMillion := func(tokens int64, price float64) float64 { return float64(tokens) * price / 1_000_000 }
	result := QuoteResult{Priced: true, Unit: rate.unit, Quality: QualityExact, RateCardVersion: rate.version}
	if usage.Source == "gatepost" {
		// Gatepost's broad Codex/Claude index stores context tokens but not the
		// cached subset. Bound that context between all-cached and all-uncached.
		result.Quality = QualityRange
		result.Low = perMillion(usage.InputTokens, rate.cacheRead) + perMillion(usage.OutputTokens, rate.output)
		highContextRate := rate.input
		if rate.cacheWriteHigh > highContextRate {
			highContextRate = rate.cacheWriteHigh
		}
		result.High = perMillion(usage.InputTokens, highContextRate) + perMillion(usage.OutputTokens, rate.output)
		return result
	}
	result.Low = perMillion(usage.InputTokens, rate.input) + perMillion(usage.CacheReadTokens, rate.cacheRead) +
		perMillion(usage.CacheCreationTokens, rate.cacheWriteLow) + perMillion(usage.OutputTokens, rate.output)
	result.High = perMillion(usage.InputTokens, rate.input) + perMillion(usage.CacheReadTokens, rate.cacheRead) +
		perMillion(usage.CacheCreationTokens, rate.cacheWriteHigh) + perMillion(usage.OutputTokens, rate.output)
	return result
}

func lookupRate(provider, model string, at time.Time) (rate, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	switch provider {
	case "codex":
		return codexRate(name)
	case "claude":
		return claudeRate(name, at)
	default:
		return rate{}, false
	}
}

func codexRate(model string) (rate, bool) {
	const version = "openai-codex-2026-04-02"
	makeRate := func(input, cached, output float64) rate {
		return rate{unit: UnitCodexCredits, version: version, input: input, cacheRead: cached,
			output: output, cacheWriteLow: input, cacheWriteHigh: input}
	}
	switch {
	case model == "gpt-5.6-sol" || strings.Contains(model, "5.6-sol"):
		return makeRate(125, 12.5, 750), true
	case model == "gpt-5.6-terra" || strings.Contains(model, "5.6-terra"):
		return makeRate(62.5, 6.25, 375), true
	case model == "gpt-5.6-luna" || strings.Contains(model, "5.6-luna"):
		return makeRate(25, 2.5, 150), true
	case model == "gpt-5.5-cyber" || strings.Contains(model, "5.5-cyber"):
		return makeRate(500, 50, 3000), true
	case model == "gpt-5.5" || strings.HasPrefix(model, "gpt-5.5-"):
		return makeRate(125, 12.5, 750), true
	case model == "gpt-5.4-mini" || strings.Contains(model, "5.4-mini"):
		return makeRate(18.75, 1.875, 113), true
	case model == "gpt-5.4" || strings.HasPrefix(model, "gpt-5.4-"):
		return makeRate(62.5, 6.25, 375), true
	case strings.Contains(model, "5.3-codex"):
		return makeRate(43.75, 4.375, 350), true
	case model == "gpt-5.2" || strings.HasPrefix(model, "gpt-5.2-"):
		return makeRate(43.75, 4.375, 350), true
	default:
		return rate{}, false
	}
}

func claudeRate(model string, at time.Time) (rate, bool) {
	makeRate := func(version string, input, cacheWrite5m, cacheWrite1h, cacheRead, output float64) rate {
		return rate{unit: UnitUSDAPIEquivalent, version: version, input: input, cacheRead: cacheRead,
			output: output, cacheWriteLow: cacheWrite5m, cacheWriteHigh: cacheWrite1h}
	}
	const version = "anthropic-api-2026-07-17"
	switch {
	case strings.Contains(model, "fable-5") || strings.Contains(model, "mythos-5"):
		return makeRate(version, 10, 12.5, 20, 1, 50), true
	case strings.Contains(model, "opus-4-8") || strings.Contains(model, "opus-4-7") ||
		strings.Contains(model, "opus-4-6") || strings.Contains(model, "opus-4-5"):
		return makeRate(version, 5, 6.25, 10, .5, 25), true
	case strings.Contains(model, "sonnet-5"):
		if at.Before(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
			return makeRate("anthropic-api-sonnet5-intro-2026-07-17", 2, 2.5, 4, .2, 10), true
		}
		return makeRate("anthropic-api-sonnet5-2026-09-01", 3, 3.75, 6, .3, 15), true
	case strings.Contains(model, "sonnet-4-6") || strings.Contains(model, "sonnet-4-5"):
		return makeRate(version, 3, 3.75, 6, .3, 15), true
	case strings.Contains(model, "haiku-4-5"):
		return makeRate(version, 1, 1.25, 2, .1, 5), true
	default:
		return rate{}, false
	}
}
