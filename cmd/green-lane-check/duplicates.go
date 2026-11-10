package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/croutoncreations/redline/internal/greenlane"
)

// Only the trusted reconciliation job sets the cache path. Full PR runs
// fetch live duplicate data. Reusing one sweep snapshot avoids O(N²) API use.
type duplicateSnapshot struct {
	Repository  string                `json:"repository"`
	CollectedAt time.Time             `json:"collected_at"`
	PRs         []greenlane.PRSummary `json:"prs"`
}

func (c *GitHubClient) duplicates(ctx context.Context, target int, cache string) ([]greenlane.PRSummary, error) {
	if cache == "" {
		return c.GetOpenPRs(ctx, target)
	}
	data, err := os.ReadFile(cache)
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("duplicate snapshot too large")
	}
	var snapshot duplicateSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	age := time.Since(snapshot.CollectedAt)
	if snapshot.Repository != c.owner+"/"+c.repo || snapshot.CollectedAt.IsZero() || age < -time.Minute || age > 5*time.Minute {
		return nil, fmt.Errorf("duplicate snapshot is stale or belongs to another repository")
	}
	found := false
	seen := map[int]bool{}
	for _, pr := range snapshot.PRs {
		if pr.Number <= 0 || seen[pr.Number] {
			return nil, fmt.Errorf("invalid duplicate snapshot")
		}
		seen[pr.Number] = true
		found = found || pr.Number == target
	}
	if !found {
		return nil, fmt.Errorf("target not present in duplicate snapshot")
	}
	return snapshot.PRs, nil
}
