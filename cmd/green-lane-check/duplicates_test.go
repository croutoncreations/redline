package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/greenlane"
)

func TestTrustedDuplicateCacheIsRepositoryBoundAndExpires(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*duplicateSnapshot)
		valid bool
	}{
		{"fresh", func(s *duplicateSnapshot) {}, true},
		{"expired", func(s *duplicateSnapshot) { s.CollectedAt = time.Now().Add(-6 * time.Minute) }, false},
		{"future", func(s *duplicateSnapshot) { s.CollectedAt = time.Now().Add(time.Hour) }, false},
		{"wrong repo", func(s *duplicateSnapshot) { s.Repository = "other/repo" }, false},
		{"missing target", func(s *duplicateSnapshot) { s.PRs[0].Number = 2 }, false},
		{"duplicate IDs", func(s *duplicateSnapshot) { s.PRs = append(s.PRs, s.PRs[0]) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := duplicateSnapshot{Repository: "owner/repo", CollectedAt: time.Now().UTC(), PRs: []greenlane.PRSummary{{Number: 1, Files: []string{"x.go"}}}}
			tc.edit(&snapshot)
			file := filepath.Join(t.TempDir(), "snapshot.json")
			data, _ := json.Marshal(snapshot)
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			c := &GitHubClient{owner: "owner", repo: "repo"}
			_, err := c.duplicates(context.Background(), 1, file)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
