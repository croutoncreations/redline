package launchmetrics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/capacity"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func observation(provider, sourceID string, input, output, cacheRead, cacheCreation int64) capacity.TokenObservation {
	return capacity.TokenObservation{Provider: provider, Source: "redline-run", SourceID: sourceID,
		InputTokens: input, OutputTokens: output, CacheReadTokens: cacheRead, CacheCreationTokens: cacheCreation}
}

func TestBuildProfileUsageSeparatesTriggersAndSumsDisjointTokens(t *testing.T) {
	completed := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	records := []store.RunUsageRecord{
		{RunID: "auto", ProfileID: "p", ProviderAccountID: "claude-main", State: domain.RunCompleted, CompletedAt: completed,
			Trigger: "automatic", Observations: []capacity.TokenObservation{
				observation("claude", "auto:sonnet", 10, 20, 300, 40),
				observation("claude", "auto:haiku", 1, 2, 3, 4),
			}},
		{RunID: "manual", ProfileID: "p", ProviderAccountID: "claude-main", State: domain.RunCompleted, CompletedAt: completed,
			Trigger: "manual-task", Observations: []capacity.TokenObservation{observation("claude", "manual:sonnet", 5, 0, 0, 0)}},
		{RunID: "legacy", ProfileID: "p", ProviderAccountID: "claude-main", State: domain.RunFailed, CompletedAt: completed,
			ProfileInferred: true, Observations: []capacity.TokenObservation{observation("claude", "legacy", 0, 1, 0, 0)}},
	}
	got := BuildProfileUsage(records, "")
	if len(got) != 1 {
		t.Fatalf("profiles = %#v", got)
	}
	profile := got[0]
	if auto := profile.Automatic.Tokens; auto == nil || *auto != (TokenTotals{Input: 11, Output: 22, CacheRead: 303, CacheCreation: 44, Total: 380}) {
		t.Fatalf("automatic tokens = %#v", profile.Automatic.Tokens)
	}
	if profile.Manual.TerminalRuns != 1 || profile.Manual.Tokens.Total != 5 {
		t.Fatalf("manual = %#v", profile.Manual)
	}
	if profile.UnknownTrigger.FailedRuns != 1 || profile.UnknownTrigger.Tokens.Total != 1 || profile.InferredRuns != 1 {
		t.Fatalf("unknown trigger = %#v inferred=%d", profile.UnknownTrigger, profile.InferredRuns)
	}
	if profile.All.TerminalRuns != 3 || profile.All.CompletedRuns != 2 || profile.All.Tokens.Total != 386 ||
		*profile.All.EvidenceCoverage != 1 || strings.Join(profile.UsageProviders, ",") != "claude" {
		t.Fatalf("all = %#v providers=%v", profile.All, profile.UsageProviders)
	}
}

func TestBuildProfileUsageKeepsUnknownDistinctFromZero(t *testing.T) {
	records := []store.RunUsageRecord{
		{RunID: "pi-1", ProfileID: "pi", ProviderAccountID: "claude-main", HarnessType: "pi", State: domain.RunCompleted, Trigger: "automatic"},
		{RunID: "c-1", ProfileID: "claude", ProviderAccountID: "claude-main", State: domain.RunCompleted, Trigger: "automatic",
			Observations: []capacity.TokenObservation{observation("claude", "c-1:sonnet", 10, 0, 0, 0)}},
		{RunID: "c-2", ProfileID: "claude", ProviderAccountID: "claude-main", State: domain.RunCompleted, Trigger: "automatic"},
	}
	got := BuildProfileUsage(records, "")
	if len(got) != 2 || got[0].ProfileID != "claude" || got[1].ProfileID != "pi" {
		t.Fatalf("profiles = %#v", got)
	}
	if got[1].All.Tokens != nil || got[1].All.RunsWithEvidence != 0 || *got[1].All.EvidenceCoverage != 0 {
		t.Fatalf("missing evidence must be unknown: %#v", got[1].All)
	}
	if got[0].All.Tokens.Total != 10 || *got[0].All.EvidenceCoverage != .5 {
		t.Fatalf("partial coverage = %#v", got[0].All)
	}
	if got[0].Manual.Tokens != nil || got[0].Manual.EvidenceCoverage != nil {
		t.Fatalf("empty trigger class must have no coverage: %#v", got[0].Manual)
	}
	encoded, err := json.Marshal(got[1].All)
	if err != nil || !strings.Contains(string(encoded), `"tokens":null`) {
		t.Fatalf("encoded = %s err=%v", encoded, err)
	}
}

func TestBuildProfileUsageDropsRunWideDuplicateOfPerModelEvidence(t *testing.T) {
	records := []store.RunUsageRecord{{RunID: "r", ProfileID: "p", ProviderAccountID: "claude-main",
		State: domain.RunCompleted, Trigger: "automatic", Observations: []capacity.TokenObservation{
			observation("claude", "r", 10, 10, 0, 0),
			observation("claude", "r:sonnet", 10, 10, 0, 0),
			{Provider: "claude", Source: "gatepost", SourceID: "r:sonnet", InputTokens: 999},
		}}}
	got := BuildProfileUsage(records, "")
	if got[0].All.Tokens.Total != 20 || got[0].DuplicateObservationsIgnored != 1 {
		t.Fatalf("dedup = %#v ignored=%d", got[0].All.Tokens, got[0].DuplicateObservationsIgnored)
	}
}

func TestBuildProfileUsageKeysByProfileAndAccountAndHonorsFilter(t *testing.T) {
	records := []store.RunUsageRecord{
		{RunID: "a", ProfileID: "p", ProviderAccountID: "claude-main", State: domain.RunCompleted, Trigger: "automatic",
			Observations: []capacity.TokenObservation{observation("claude", "a:sonnet", 1, 0, 0, 0)}},
		{RunID: "b", ProfileID: "p", ProviderAccountID: "claude-work", State: domain.RunCompleted, Trigger: "automatic",
			Observations: []capacity.TokenObservation{observation("claude", "b:sonnet", 2, 0, 0, 0)}},
		{RunID: "h", ProfileID: "hermes", ProviderAccountID: "claude-main", HarnessType: "hermes", State: domain.RunCompleted,
			Trigger: "automatic", Observations: []capacity.TokenObservation{observation("codex", "h", 3, 0, 0, 0)}},
	}
	all := BuildProfileUsage(records, "")
	if len(all) != 3 {
		t.Fatalf("profiles = %#v", all)
	}
	filtered := BuildProfileUsage(records, "claude-main")
	if len(filtered) != 2 || filtered[0].ProfileID != "hermes" || filtered[1].All.Tokens.Total != 1 {
		t.Fatalf("filtered = %#v", filtered)
	}
	if strings.Join(filtered[0].UsageProviders, ",") != "codex" {
		t.Fatalf("hermes usage providers = %v", filtered[0].UsageProviders)
	}
}
