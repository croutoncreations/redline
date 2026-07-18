// Package tokenlog imports local harness usage records into Redline's normalized
// token observation model.
package tokenlog

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jfox/redline/internal/capacity"
	_ "modernc.org/sqlite"
)

// LoadGatepost reads Gatepost's normalized assistant-message index. Its
// context_tokens field represents processed context/input-like tokens and does
// not preserve cache-read/cache-creation classes, so observations are marked
// medium confidence and should not be interpreted as provider billing tokens.
func LoadGatepost(ctx context.Context, path, provider string, after time.Time) ([]capacity.TokenObservation, error) {
	resolved, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(resolved); err != nil {
		return nil, fmt.Errorf("open Gatepost token log: %w", err)
	}
	db, err := sql.Open("sqlite", resolved)
	if err != nil {
		return nil, fmt.Errorf("open Gatepost token log: %w", err)
	}
	defer db.Close()
	query := `SELECT m.session_id, m.ordinal, m.ts, COALESCE(m.model, ''),
COALESCE(m.context_tokens, 0), COALESCE(m.output_tokens, 0)
FROM messages m JOIN sessions s ON s.id = m.session_id
WHERE s.agent = ? AND m.role = 'assistant' AND m.ts > ?
AND (COALESCE(m.context_tokens, 0) > 0 OR COALESCE(m.output_tokens, 0) > 0)
ORDER BY m.ts, m.session_id, m.ordinal`
	rows, err := db.QueryContext(ctx, query, provider, after.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query Gatepost token log: %w", err)
	}
	defer rows.Close()
	var result []capacity.TokenObservation
	for rows.Next() {
		var sessionID, model string
		var ordinal, timestamp, input, output int64
		if err := rows.Scan(&sessionID, &ordinal, &timestamp, &model, &input, &output); err != nil {
			return nil, fmt.Errorf("scan Gatepost token log: %w", err)
		}
		if timestamp <= 0 || input < 0 || output < 0 {
			continue
		}
		result = append(result, capacity.TokenObservation{
			Provider: provider, Source: "gatepost", SourceID: fmt.Sprintf("%s:%d", sessionID, ordinal),
			ObservedAt: time.UnixMilli(timestamp).UTC(), Model: model, InputTokens: input,
			OutputTokens: output, Confidence: "medium",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Gatepost token log: %w", err)
	}
	return result, nil
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve Gatepost database path: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}
