package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func TestRunCompletionsEndpointAuthBaselineValidationAndPaging(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://unused")
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	get := func(path, token string, wantStatus int) struct {
		Cursor int64        `json:"cursor"`
		Runs   []domain.Run `json:"runs"`
	} {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != wantStatus {
			t.Fatalf("GET %s: status=%d, want %d", path, resp.StatusCode, wantStatus)
		}
		var result struct {
			Cursor int64        `json:"cursor"`
			Runs   []domain.Run `json:"runs"`
		}
		if wantStatus == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	path := "/v1/runs/completions"
	get(path+"?baseline=true", "", http.StatusUnauthorized)
	get(path+"?after=0", "wrong-token", http.StatusUnauthorized)
	initial := get(path+"?baseline=true", cfg.APIToken, http.StatusOK)
	if initial.Cursor != 0 || len(initial.Runs) != 0 {
		t.Fatalf("empty baseline = %#v", initial)
	}
	for _, pair := range []struct {
		id        string
		completed time.Time
	}{
		{"z", apiNow},
		{"a", apiNow},                   // lower lexical ID and tied timestamp
		{"m", apiNow.Add(-time.Second)}, // later commit with earlier wall time
	} {
		profile := domain.ExecutionProfile{ID: "p-" + pair.id, ProviderAccountID: "provider-" + pair.id,
			HarnessType: "claude-code", WorkspaceProvider: "existing-directory"}
		if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateTask(t.Context(), domain.Task{ID: pair.id, Name: pair.id,
			ExecutionProfileID: profile.ID, Type: domain.OneOff}, apiNow); err != nil {
			t.Fatal(err)
		}
		if _, err := db.AdmitTask(t.Context(), "run-"+pair.id, pair.id, profile.ProviderAccountID, "", apiNow); err != nil {
			t.Fatal(err)
		}
		if err := db.CompleteRun(t.Context(), "run-"+pair.id, domain.RunCompletion{State: domain.RunCompleted}, pair.completed); err != nil {
			t.Fatal(err)
		}
	}
	baseline := get(path+"?baseline=true", cfg.APIToken, http.StatusOK)
	if baseline.Cursor != 3 || len(baseline.Runs) != 0 {
		t.Fatalf("baseline after completions = %#v", baseline)
	}
	first := get(path+"?after=0&limit=2", cfg.APIToken, http.StatusOK)
	if first.Cursor != 2 || len(first.Runs) != 2 || first.Runs[0].ID != "run-z" || first.Runs[1].ID != "run-a" {
		t.Fatalf("first page = %#v", first)
	}
	second := get(path+"?after=2&limit=2", cfg.APIToken, http.StatusOK)
	if second.Cursor != 3 || len(second.Runs) != 1 || second.Runs[0].ID != "run-m" {
		t.Fatalf("second page = %#v", second)
	}
	empty := get(path+"?after=3&limit=2", cfg.APIToken, http.StatusOK)
	if empty.Cursor != 3 || len(empty.Runs) != 0 {
		t.Fatalf("empty page = %#v", empty)
	}
	for _, invalid := range []string{"-1", "not-a-cursor", "9223372036854775808"} {
		get(path+"?after="+url.QueryEscape(invalid), cfg.APIToken, http.StatusBadRequest)
	}
	for _, limit := range []int{0, -1, 101} {
		get(fmt.Sprintf("%s?after=0&limit=%d", path, limit), cfg.APIToken, http.StatusBadRequest)
	}
}
