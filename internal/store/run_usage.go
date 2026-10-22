package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/capacity"
	"github.com/croutoncreations/redline/internal/domain"
)

// RunUsageRecord is one terminal Redline run together with the identity it
// actually ran under and the run-scoped token evidence recorded for it.
type RunUsageRecord struct {
	RunID             string
	ProviderAccountID string
	State             domain.RunState
	CompletedAt       time.Time
	// ProfileID and HarnessType come from the run.started audit snapshot,
	// which records the profile as it was when the run began. When that
	// snapshot is missing they fall back to the task's current profile and
	// ProfileInferred is set, because tasks can be re-pointed after a run.
	ProfileID       string
	HarnessType     string
	ProfileInferred bool
	// Trigger is the trigger of the first dispatch attempt that admitted the
	// run ("automatic", "manual", "manual-task"), or "" when none is retained.
	Trigger      string
	Observations []capacity.TokenObservation
}

// runUsageObservationsQuery reads the redline-run evidence of the runs
// completed in [since, until). Evidence is keyed run_id (Codex, Hermes,
// single-model Claude) or run_id:model (one row per model in a Claude result),
// and run IDs never contain ':'. Each run is one range seek on the
// (provider, source, source_id) unique index, so cost follows the window, not
// the history of a table dominated by ambient Gatepost rows. The recursive CTE
// lists distinct providers by index seeks, and CROSS JOIN pins the join order.
// Observations are matched to runs, not time-filtered: observed_at can precede
// completed_at.
const runUsageObservationsQuery = `WITH RECURSIVE providers(provider) AS (
    SELECT MIN(provider) FROM token_observations
    UNION ALL
    SELECT (SELECT MIN(provider) FROM token_observations WHERE provider > providers.provider)
    FROM providers WHERE providers.provider IS NOT NULL),
selected AS (
    SELECT id FROM runs WHERE state IN ('completed', 'failed') AND completed_at >= ? AND completed_at < ?)
SELECT o.provider, o.source, o.source_id, o.observed_at, o.model, o.input_tokens, o.output_tokens,
o.cache_read_tokens, o.cache_creation_tokens, o.confidence
FROM selected r CROSS JOIN providers CROSS JOIN token_observations o
WHERE providers.provider IS NOT NULL AND o.provider = providers.provider AND o.source = 'redline-run'
    AND o.source_id >= r.id AND o.source_id < r.id || ';'
    AND (o.source_id = r.id OR substr(o.source_id, length(r.id) + 1, 1) = ':')
ORDER BY o.id`

// ListRunUsage returns completed and failed runs whose completion time falls
// in [since, until), each with its redline-run token observations. Ambient
// sources such as Gatepost are never included.
func (d *DB) ListRunUsage(ctx context.Context, since, until time.Time) ([]RunUsageRecord, error) {
	if since.IsZero() || until.IsZero() || !until.After(since) {
		return nil, fmt.Errorf("run usage requires a valid time range")
	}
	rows, err := d.db.QueryContext(ctx, `WITH first_attempt AS (
    SELECT run_id, MIN(id) AS id FROM dispatch_attempts WHERE run_id IS NOT NULL GROUP BY run_id)
SELECT r.id, r.provider_account_id, r.state, r.completed_at,
COALESCE(json_extract(CAST(e.payload_json AS TEXT), '$.profile.id'), ''),
COALESCE(json_extract(CAST(e.payload_json AS TEXT), '$.profile.harness_type'), ''),
t.execution_profile_id, COALESCE(p.harness_type, ''), COALESCE(a.trigger, '')
FROM runs r
JOIN tasks t ON t.id = r.task_id
LEFT JOIN execution_profiles p ON p.id = t.execution_profile_id
LEFT JOIN run_events e ON e.id = (
    SELECT MIN(id) FROM run_events WHERE run_id = r.id AND event_type = 'run.started')
LEFT JOIN first_attempt fa ON fa.run_id = r.id
LEFT JOIN dispatch_attempts a ON a.id = fa.id
WHERE r.state IN ('completed', 'failed') AND r.completed_at >= ? AND r.completed_at < ?
ORDER BY r.completed_at, r.id`, formatTime(since), formatTime(until))
	if err != nil {
		return nil, fmt.Errorf("list run usage: %w", err)
	}
	defer rows.Close()
	var records []RunUsageRecord
	index := map[string]int{}
	for rows.Next() {
		var record RunUsageRecord
		var completed sql.NullString
		var currentProfile, currentHarness string
		if err := rows.Scan(&record.RunID, &record.ProviderAccountID, &record.State, &completed,
			&record.ProfileID, &record.HarnessType, &currentProfile, &currentHarness, &record.Trigger); err != nil {
			return nil, fmt.Errorf("scan run usage: %w", err)
		}
		if !completed.Valid {
			continue
		}
		if record.CompletedAt, err = parseStoredTimeField("run completion time", completed.String); err != nil {
			return nil, err
		}
		if record.ProfileID == "" {
			record.ProfileID, record.HarnessType, record.ProfileInferred = currentProfile, currentHarness, true
		}
		index[record.RunID] = len(records)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run usage: %w", err)
	}
	if len(records) == 0 {
		return records, nil
	}
	observationRows, err := d.db.QueryContext(ctx, runUsageObservationsQuery, formatTime(since), formatTime(until))
	if err != nil {
		return nil, fmt.Errorf("list run usage observations: %w", err)
	}
	defer observationRows.Close()
	for observationRows.Next() {
		var observedAt string
		var observation capacity.TokenObservation
		if err := observationRows.Scan(&observation.Provider, &observation.Source, &observation.SourceID,
			&observedAt, &observation.Model, &observation.InputTokens, &observation.OutputTokens,
			&observation.CacheReadTokens, &observation.CacheCreationTokens, &observation.Confidence); err != nil {
			return nil, fmt.Errorf("scan run usage observation: %w", err)
		}
		runID, _, _ := strings.Cut(observation.SourceID, ":")
		position, ok := index[runID]
		if !ok {
			continue
		}
		if observation.ObservedAt, err = parseStoredTimeField("token observation time", observedAt); err != nil {
			return nil, err
		}
		records[position].Observations = append(records[position].Observations, observation)
	}
	if err := observationRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run usage observations: %w", err)
	}
	return records, nil
}
