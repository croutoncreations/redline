package accounting_test

import (
	"math"
	"testing"
	"time"

	"github.com/jfox/redline/internal/accounting"
)

func TestClaudeQuotesModelAndCacheClassesAsAPIDollarEquivalent(t *testing.T) {
	usage := accounting.Usage{Provider: "claude", Model: "claude-fable-5", Source: "gatepost-pi",
		InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheCreationTokens: 1_000_000}
	got := accounting.Quote(usage, time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC))
	if !got.Priced || got.Unit != accounting.UnitUSDAPIEquivalent || got.Low != 73.5 || got.High != 81 || got.RateCardVersion != "anthropic-api-2026-07-17" {
		t.Fatalf("quote = %#v", got)
	}
	haiku := usage
	haiku.Model = "claude-haiku-4-5-20251001"
	cheap := accounting.Quote(haiku, time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC))
	if math.Abs(cheap.Low*10-got.Low) > .000001 || math.Abs(cheap.High*10-got.High) > .000001 {
		t.Fatalf("fable=%#v haiku=%#v", got, cheap)
	}
}

func TestCodexQuotesOfficialSubscriptionCredits(t *testing.T) {
	got := accounting.Quote(accounting.Usage{Provider: "codex", Model: "gpt-5.5", Source: "gatepost-pi",
		InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheCreationTokens: 1_000_000}, time.Now())
	if !got.Priced || got.Unit != accounting.UnitCodexCredits || got.Low != 1012.5 || got.High != 1012.5 || got.RateCardVersion != "openai-codex-2026-04-02" {
		t.Fatalf("quote = %#v", got)
	}
}

func TestCollapsedContextProducesCachedToUncachedRange(t *testing.T) {
	got := accounting.Quote(accounting.Usage{Provider: "codex", Model: "gpt-5.5", Source: "gatepost", InputTokens: 1_000_000}, time.Now())
	if !got.Priced || got.Low != 12.5 || got.High != 125 || got.Quality != accounting.QualityRange {
		t.Fatalf("quote = %#v", got)
	}
}

func TestUnknownModelRemainsUnpriced(t *testing.T) {
	got := accounting.Quote(accounting.Usage{Provider: "claude", Model: "private-model", InputTokens: 100}, time.Now())
	if got.Priced || got.Reason != "unknown model" {
		t.Fatalf("quote = %#v", got)
	}
}

func TestSonnetFiveUsesDateVersionedIntroductoryRate(t *testing.T) {
	usage := accounting.Usage{Provider: "claude", Model: "claude-sonnet-5", Source: "gatepost-pi", InputTokens: 1_000_000}
	intro := accounting.Quote(usage, time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC))
	standard := accounting.Quote(usage, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if intro.Low != 2 || standard.Low != 3 || intro.RateCardVersion == standard.RateCardVersion {
		t.Fatalf("intro=%#v standard=%#v", intro, standard)
	}
}

// TestCodexRateCardCoversAllModelTiers exercises every branch of codexRate so
// that a pricing mistake (e.g. a model added to the wrong tier, or a copy-paste
// error in the credit multiplier) is caught before it silently miscounts
// subscription credits for users.
func TestCodexRateCardCoversAllModelTiers(t *testing.T) {
	const version = "openai-codex-2026-04-02"
	at := time.Now()
	oneMillion := int64(1_000_000)

	cases := []struct {
		model      string
		wantInput  float64 // credits per 1M input tokens (uncached)
		wantCached float64 // credits per 1M cache-read tokens
		wantOutput float64 // credits per 1M output tokens
	}{
		// gpt-5.6-sol family — highest 5.6 tier
		{model: "gpt-5.6-sol", wantInput: 125, wantCached: 12.5, wantOutput: 750},
		{model: "gpt-5.6-sol-20260601", wantInput: 125, wantCached: 12.5, wantOutput: 750},

		// gpt-5.6-terra
		{model: "gpt-5.6-terra", wantInput: 62.5, wantCached: 6.25, wantOutput: 375},
		{model: "gpt-5.6-terra-20260601", wantInput: 62.5, wantCached: 6.25, wantOutput: 375},

		// gpt-5.6-luna
		{model: "gpt-5.6-luna", wantInput: 25, wantCached: 2.5, wantOutput: 150},
		{model: "gpt-5.6-luna-20260601", wantInput: 25, wantCached: 2.5, wantOutput: 150},

		// gpt-5.5-cyber — premium variant inside 5.5 family
		{model: "gpt-5.5-cyber", wantInput: 500, wantCached: 50, wantOutput: 3000},

		// gpt-5.5 default (also covered by existing test; verified here for completeness)
		{model: "gpt-5.5", wantInput: 125, wantCached: 12.5, wantOutput: 750},
		{model: "gpt-5.5-standard", wantInput: 125, wantCached: 12.5, wantOutput: 750},

		// gpt-5.4-mini — cheapest 5.4 variant
		{model: "gpt-5.4-mini", wantInput: 18.75, wantCached: 1.875, wantOutput: 113},
		{model: "gpt-5.4-mini-20260501", wantInput: 18.75, wantCached: 1.875, wantOutput: 113},

		// gpt-5.4 default
		{model: "gpt-5.4", wantInput: 62.5, wantCached: 6.25, wantOutput: 375},
		{model: "gpt-5.4-standard", wantInput: 62.5, wantCached: 6.25, wantOutput: 375},

		// 5.3-codex family
		{model: "gpt-5.3-codex", wantInput: 43.75, wantCached: 4.375, wantOutput: 350},
		{model: "gpt-5.3-codex-mini", wantInput: 43.75, wantCached: 4.375, wantOutput: 350},

		// gpt-5.2 family
		{model: "gpt-5.2", wantInput: 43.75, wantCached: 4.375, wantOutput: 350},
		{model: "gpt-5.2-standard", wantInput: 43.75, wantCached: 4.375, wantOutput: 350},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			usage := accounting.Usage{
				Provider:            "codex",
				Model:               tc.model,
				Source:              "gatepost-pi",
				InputTokens:         oneMillion,
				OutputTokens:        oneMillion,
				CacheReadTokens:     oneMillion,
				CacheCreationTokens: oneMillion,
			}
			got := accounting.Quote(usage, at)
			if !got.Priced {
				t.Fatalf("model %q: not priced (reason=%q)", tc.model, got.Reason)
			}
			if got.Unit != accounting.UnitCodexCredits {
				t.Fatalf("model %q: unit=%q, want %q", tc.model, got.Unit, accounting.UnitCodexCredits)
			}
			if got.RateCardVersion != version {
				t.Fatalf("model %q: version=%q, want %q", tc.model, got.RateCardVersion, version)
			}
			// For non-gatepost source, cache writes cost the same as uncached input
			// (codexRate sets cacheWriteLow == cacheWriteHigh == input), so Low == High.
			wantLow := tc.wantInput + tc.wantCached + tc.wantInput + tc.wantOutput
			if math.Abs(got.Low-wantLow) > 0.001 {
				t.Fatalf("model %q: Low=%v, want %v (quote=%#v)", tc.model, got.Low, wantLow, got)
			}
			if math.Abs(got.High-wantLow) > 0.001 {
				t.Fatalf("model %q: High=%v, want %v (quote=%#v)", tc.model, got.High, wantLow, got)
			}
		})
	}
}

func TestCodexUnknownModelIsUnpriced(t *testing.T) {
	got := accounting.Quote(accounting.Usage{Provider: "codex", Model: "gpt-4o", InputTokens: 100}, time.Now())
	if got.Priced || got.Reason != "unknown model" {
		t.Fatalf("quote = %#v", got)
	}
}
