package tokenlog

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jfox/redline/internal/capacity"
)

var piSubscriptionProvider = map[string]string{
	"anthropic-cli": "claude",
	"openai-codex":  "codex",
}

var piProviderTransport = map[string]string{
	"claude": "anthropic-cli",
	"codex":  "openai-codex",
}

// LoadGatepostPi uses Gatepost's session index to find Pi JSONL files, then
// reads the raw records because Gatepost's current messages table omits Pi's
// provider and cache-token fields. Only explicit subscription transports are
// mapped; API and other providers are intentionally ignored.
func LoadGatepostPi(ctx context.Context, databasePath, targetProvider string, after time.Time) ([]capacity.TokenObservation, error) {
	resolved, err := expandHome(databasePath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(resolved); err != nil {
		return nil, fmt.Errorf("open Gatepost Pi index: %w", err)
	}
	db, err := sql.Open("sqlite", resolved)
	if err != nil {
		return nil, fmt.Errorf("open Gatepost Pi index: %w", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, source_path FROM sessions
WHERE agent = 'pi' AND source_path != '' AND COALESCE(ended_at, started_at, 0) > ?
ORDER BY started_at, id`, after.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query Gatepost Pi sessions: %w", err)
	}
	defer rows.Close()
	var result []capacity.TokenObservation
	for rows.Next() {
		var sessionID, sourcePath string
		if err := rows.Scan(&sessionID, &sourcePath); err != nil {
			return nil, fmt.Errorf("scan Gatepost Pi session: %w", err)
		}
		observations, err := loadPiFile(ctx, sessionID, sourcePath, targetProvider, after)
		if err != nil {
			return nil, err
		}
		result = append(result, observations...)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Gatepost Pi sessions: %w", err)
	}
	return result, nil
}

type piRecord struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp json.RawMessage `json:"timestamp"`
	Message   struct {
		Role      string          `json:"role"`
		Provider  string          `json:"provider"`
		Model     string          `json:"model"`
		Timestamp json.RawMessage `json:"timestamp"`
		Usage     struct {
			Input         int64 `json:"input"`
			Output        int64 `json:"output"`
			CacheRead     int64 `json:"cacheRead"`
			CacheWrite    int64 `json:"cacheWrite"`
			CacheCreation int64 `json:"cacheCreation"`
			Cache         struct {
				Read  int64 `json:"read"`
				Write int64 `json:"write"`
			} `json:"cache"`
		} `json:"usage"`
	} `json:"message"`
}

func loadPiFile(ctx context.Context, sessionID, path, targetProvider string, after time.Time) ([]capacity.TokenObservation, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Pi session %q: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	transport, ok := piProviderTransport[targetProvider]
	if !ok {
		return nil, nil
	}
	providerNeedle := []byte(`"provider":"` + transport + `"`)
	var result []capacity.TokenObservation
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Bytes()
		// Pi writes compact JSONL. Most lines are user/tool events or use an
		// unrelated provider; reject those without paying the JSON decode cost.
		if !bytes.Contains(line, providerNeedle) {
			continue
		}
		var record piRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		mapped, ok := piSubscriptionProvider[record.Message.Provider]
		if record.Type != "message" || record.Message.Role != "assistant" || !ok || mapped != targetProvider || record.ID == "" {
			continue
		}
		observedAt, ok := piRecordTime(record.Timestamp, record.Message.Timestamp)
		if !ok || !observedAt.After(after) {
			continue
		}
		cacheRead := record.Message.Usage.CacheRead + record.Message.Usage.Cache.Read
		cacheCreation := record.Message.Usage.CacheWrite + record.Message.Usage.CacheCreation + record.Message.Usage.Cache.Write
		if record.Message.Usage.Input < 0 || record.Message.Usage.Output < 0 || cacheRead < 0 || cacheCreation < 0 {
			continue
		}
		if record.Message.Usage.Input+record.Message.Usage.Output+cacheRead+cacheCreation == 0 {
			continue
		}
		result = append(result, capacity.TokenObservation{
			Provider: mapped, Source: "gatepost-pi", SourceID: sessionID + ":" + record.ID,
			ObservedAt: observedAt, Model: record.Message.Model, InputTokens: record.Message.Usage.Input,
			OutputTokens: record.Message.Usage.Output, CacheReadTokens: cacheRead,
			CacheCreationTokens: cacheCreation, Confidence: "high",
		})
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read Pi session %q: %w", path, err)
	}
	return result, nil
}

func piRecordTime(values ...json.RawMessage) (time.Time, bool) {
	for _, raw := range values {
		value := strings.TrimSpace(string(raw))
		if value == "" || value == "null" {
			continue
		}
		if value[0] == '"' {
			var text string
			if json.Unmarshal(raw, &text) == nil {
				if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
					return parsed.UTC(), true
				}
				if millis, err := strconv.ParseInt(text, 10, 64); err == nil {
					return time.UnixMilli(millis).UTC(), true
				}
			}
			continue
		}
		if millis, err := strconv.ParseInt(value, 10, 64); err == nil {
			return time.UnixMilli(millis).UTC(), true
		}
	}
	return time.Time{}, false
}
