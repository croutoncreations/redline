package tokenlog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/store"
)

type runUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CachedInputTokens        int64 `json:"cached_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	Input                    int64 `json:"input"`
	Output                   int64 `json:"output"`
	CacheRead                int64 `json:"cache_read"`
	CacheWrite               int64 `json:"cache_write"`
}

type claudeModelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

type runRecord struct {
	Type       string                      `json:"type"`
	Usage      runUsage                    `json:"usage"`
	ModelUsage map[string]claudeModelUsage `json:"modelUsage"`
	Model      string                      `json:"model"`
	Provider   string                      `json:"provider"`
}

// LoadRunArtifact reads the terminal usage record emitted by a Redline-owned
// harness. It deliberately ignores per-message usage because terminal records
// contain run totals and counting both would inflate usage.
func LoadRunArtifact(path, harnessType, runID, configuredModel string, observedAt time.Time) ([]capacity.TokenObservation, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open run artifact: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	var result []capacity.TokenObservation
	for scanner.Scan() {
		var record runRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		switch harnessType {
		case "claude-code":
			if record.Type != "result" {
				continue
			}
			result = claudeRunObservations(record, runID, configuredModel, observedAt)
		case "codex-cli":
			if record.Type != "turn.completed" {
				continue
			}
			input := record.Usage.InputTokens - record.Usage.CachedInputTokens
			if input < 0 {
				input = 0
			}
			result = []capacity.TokenObservation{{
				Provider: "codex", Source: "redline-run", SourceID: runID,
				ObservedAt: observedAt.UTC(), Model: configuredModel, InputTokens: input,
				OutputTokens: record.Usage.OutputTokens, CacheReadTokens: record.Usage.CachedInputTokens,
				Confidence: "high",
			}}
		case "hermes":
			if record.Type != "hermes.result" {
				continue
			}
			model := record.Model
			if model == "" {
				model = configuredModel
			}
			provider := normalizeHermesProvider(record.Provider, model)
			input := record.Usage.InputTokens
			if record.Usage.Input > 0 {
				input = record.Usage.Input
			}
			output := record.Usage.OutputTokens
			if record.Usage.Output > 0 {
				output = record.Usage.Output
			}
			cacheRead := record.Usage.CacheReadInputTokens
			if record.Usage.CachedInputTokens > cacheRead {
				cacheRead = record.Usage.CachedInputTokens
			}
			if record.Usage.CacheRead > cacheRead {
				cacheRead = record.Usage.CacheRead
			}
			cacheWrite := record.Usage.CacheCreationInputTokens
			if record.Usage.CacheWrite > cacheWrite {
				cacheWrite = record.Usage.CacheWrite
			}
			result = []capacity.TokenObservation{{
				Provider: provider, Source: "redline-run", SourceID: runID,
				ObservedAt: observedAt.UTC(), Model: model, InputTokens: input,
				OutputTokens: output, CacheReadTokens: cacheRead,
				CacheCreationTokens: cacheWrite, Confidence: "high",
			}}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read run artifact: %w", err)
	}
	return nonzeroObservations(result), nil
}

func normalizeHermesProvider(provider, model string) string {
	switch provider {
	case "openai-codex":
		return "codex"
	case "anthropic", "anthropic-cli":
		return "claude"
	default:
		lowerModel := strings.ToLower(model)
		if strings.HasPrefix(provider, "custom:") && strings.Contains(lowerModel, "claude") {
			return "claude"
		}
		if strings.HasPrefix(provider, "custom:") &&
			(strings.Contains(lowerModel, "gpt-") || strings.Contains(lowerModel, "codex")) {
			return "codex"
		}
		return provider
	}
}

func claudeRunObservations(record runRecord, runID, configuredModel string, observedAt time.Time) []capacity.TokenObservation {
	if len(record.ModelUsage) == 0 {
		return []capacity.TokenObservation{{
			Provider: "claude", Source: "redline-run", SourceID: runID,
			ObservedAt: observedAt.UTC(), Model: configuredModel,
			InputTokens: record.Usage.InputTokens, OutputTokens: record.Usage.OutputTokens,
			CacheReadTokens:     record.Usage.CacheReadInputTokens,
			CacheCreationTokens: record.Usage.CacheCreationInputTokens, Confidence: "high",
		}}
	}
	models := make([]string, 0, len(record.ModelUsage))
	for model := range record.ModelUsage {
		models = append(models, model)
	}
	sort.Strings(models)
	result := make([]capacity.TokenObservation, 0, len(models))
	for _, model := range models {
		usage := record.ModelUsage[model]
		result = append(result, capacity.TokenObservation{
			Provider: "claude", Source: "redline-run", SourceID: runID + ":" + model,
			ObservedAt: observedAt.UTC(), Model: model, InputTokens: usage.InputTokens,
			OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadInputTokens,
			CacheCreationTokens: usage.CacheCreationInputTokens, Confidence: "high",
		})
	}
	return result
}

func nonzeroObservations(items []capacity.TokenObservation) []capacity.TokenObservation {
	result := items[:0]
	for _, item := range items {
		if item.InputTokens+item.OutputTokens+item.CacheReadTokens+item.CacheCreationTokens > 0 {
			result = append(result, item)
		}
	}
	return result
}

type RunUsageRecorder struct{ Store *store.DB }

func (r RunUsageRecorder) RecordRunUsage(
	ctx context.Context,
	run domain.Run,
	profile domain.ExecutionProfile,
	outputFile string,
	observedAt time.Time,
) (int, error) {
	if r.Store == nil || outputFile == "" ||
		(profile.HarnessType != "claude-code" && profile.HarnessType != "codex-cli" && profile.HarnessType != "hermes") {
		return 0, nil
	}
	observations, err := LoadRunArtifact(outputFile, profile.HarnessType, run.ID, profile.Model, observedAt)
	if err != nil {
		return 0, err
	}
	return r.Store.SaveTokenObservations(ctx, observations)
}
