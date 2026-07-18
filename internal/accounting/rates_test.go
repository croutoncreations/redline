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
