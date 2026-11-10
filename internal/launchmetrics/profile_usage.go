package launchmetrics

import (
	"sort"
	"strings"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

// TokenTotals is a disjoint breakdown of run-scoped tokens. Harness records
// are normalized before storage so input excludes cache reads and cache
// writes; Total is therefore a plain sum with no cache double-counting.
type TokenTotals struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_tokens"`
	CacheCreation int64 `json:"cache_creation_tokens"`
	Total         int64 `json:"total_tokens"`
}

// TriggerUsage summarizes terminal runs of one trigger class. Tokens is nil,
// not zero, when no run in the class has usage evidence.
type TriggerUsage struct {
	TerminalRuns     int          `json:"terminal_runs"`
	CompletedRuns    int          `json:"completed_runs"`
	FailedRuns       int          `json:"failed_runs"`
	RunsWithEvidence int          `json:"runs_with_usage_evidence"`
	EvidenceCoverage *float64     `json:"evidence_coverage"`
	Tokens           *TokenTotals `json:"tokens"`
}

// ProfileUsage is the Redline-managed token total for one execution profile on
// one provider account. A profile re-pointed to another account during the
// window appears once per account.
type ProfileUsage struct {
	ProfileID         string `json:"profile_id"`
	ProviderAccountID string `json:"provider_account_id"`
	HarnessType       string `json:"harness_type,omitempty"`
	// UsageProviders lists the usage families (claude, codex, ...) seen in
	// evidence. A Hermes profile can bill a different family than its account.
	UsageProviders []string `json:"usage_providers"`
	// InferredRuns counts runs with no run.started profile snapshot whose
	// profile was taken from the task's current profile instead.
	InferredRuns int `json:"runs_with_inferred_profile"`
	// DuplicateObservationsIgnored counts run-wide rows dropped because the
	// same run also has per-model rows for that provider.
	DuplicateObservationsIgnored int          `json:"duplicate_observations_ignored"`
	Automatic                    TriggerUsage `json:"automatic"`
	Manual                       TriggerUsage `json:"manual"`
	UnknownTrigger               TriggerUsage `json:"unknown_trigger"`
	All                          TriggerUsage `json:"all"`
}

// ProfileUsageMethodology is reported alongside profile totals so the token
// semantics stay attached to the numbers.
const ProfileUsageMethodology = "Profile usage counts completed and failed runs whose completion falls in the window, using only redline-run token evidence recorded from Redline-owned harness output; interactive and Gatepost usage is excluded. input_tokens excludes cache reads and writes, so total_tokens = input + output + cache_read + cache_creation. Cache reads usually dominate the total and are priced far below fresh input, so a token total is not an allowance or cost measure. Tokens are null, not zero, when no run has evidence."

// BuildProfileUsage aggregates run usage records into per-profile totals.
// accountFilter, when non-empty, keeps only runs on that provider account.
func BuildProfileUsage(records []store.RunUsageRecord, accountFilter string) []ProfileUsage {
	type key struct{ profile, account string }
	byKey := map[key]*ProfileUsage{}
	providers := map[key]map[string]bool{}
	for _, record := range records {
		if accountFilter != "" && record.ProviderAccountID != accountFilter {
			continue
		}
		if record.State != domain.RunCompleted && record.State != domain.RunFailed {
			continue
		}
		k := key{record.ProfileID, record.ProviderAccountID}
		item := byKey[k]
		if item == nil {
			item = &ProfileUsage{ProfileID: record.ProfileID, ProviderAccountID: record.ProviderAccountID, HarnessType: record.HarnessType}
			byKey[k] = item
			providers[k] = map[string]bool{}
		}
		if record.ProfileInferred {
			item.InferredRuns++
		}
		tokens, seen, duplicates := runTokens(record)
		item.DuplicateObservationsIgnored += duplicates
		for provider := range seen {
			providers[k][provider] = true
		}
		addRun(&item.All, record.State, tokens)
		switch {
		case record.Trigger == "automatic":
			addRun(&item.Automatic, record.State, tokens)
		case strings.HasPrefix(record.Trigger, "manual"):
			addRun(&item.Manual, record.State, tokens)
		default:
			addRun(&item.UnknownTrigger, record.State, tokens)
		}
	}
	result := make([]ProfileUsage, 0, len(byKey))
	for k, item := range byKey {
		item.UsageProviders = make([]string, 0, len(providers[k]))
		for provider := range providers[k] {
			item.UsageProviders = append(item.UsageProviders, provider)
		}
		sort.Strings(item.UsageProviders)
		for _, usage := range []*TriggerUsage{&item.Automatic, &item.Manual, &item.UnknownTrigger, &item.All} {
			if usage.TerminalRuns > 0 {
				coverage := float64(usage.RunsWithEvidence) / float64(usage.TerminalRuns)
				usage.EvidenceCoverage = &coverage
			}
		}
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ProviderAccountID != result[j].ProviderAccountID {
			return result[i].ProviderAccountID < result[j].ProviderAccountID
		}
		return result[i].ProfileID < result[j].ProfileID
	})
	return result
}

// runTokens sums a run's redline-run evidence. Claude results are stored as
// one run_id:model row per model; other harnesses store one run_id row. If a
// provider has both shapes for the same run (re-recording across versions),
// the per-model rows win and the run-wide row is counted as a duplicate.
func runTokens(record store.RunUsageRecord) (*TokenTotals, map[string]bool, int) {
	scoped := map[string]bool{}
	for _, observation := range record.Observations {
		if observation.Source == "redline-run" && observation.SourceID != record.RunID {
			scoped[observation.Provider] = true
		}
	}
	var totals *TokenTotals
	providers := map[string]bool{}
	duplicates := 0
	for _, observation := range record.Observations {
		if observation.Source != "redline-run" {
			continue
		}
		if observation.SourceID == record.RunID && scoped[observation.Provider] {
			duplicates++
			continue
		}
		if totals == nil {
			totals = &TokenTotals{}
		}
		providers[observation.Provider] = true
		totals.Input += observation.InputTokens
		totals.Output += observation.OutputTokens
		totals.CacheRead += observation.CacheReadTokens
		totals.CacheCreation += observation.CacheCreationTokens
	}
	if totals != nil {
		totals.Total = totals.Input + totals.Output + totals.CacheRead + totals.CacheCreation
	}
	return totals, providers, duplicates
}

func addRun(usage *TriggerUsage, state domain.RunState, tokens *TokenTotals) {
	usage.TerminalRuns++
	if state == domain.RunCompleted {
		usage.CompletedRuns++
	} else {
		usage.FailedRuns++
	}
	if tokens == nil {
		return
	}
	usage.RunsWithEvidence++
	if usage.Tokens == nil {
		usage.Tokens = &TokenTotals{}
	}
	usage.Tokens.Input += tokens.Input
	usage.Tokens.Output += tokens.Output
	usage.Tokens.CacheRead += tokens.CacheRead
	usage.Tokens.CacheCreation += tokens.CacheCreation
	usage.Tokens.Total += tokens.Total
}
