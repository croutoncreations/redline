package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/store"
)

// Cursor usage is display-only: it reaches the dashboard but never becomes a
// provider account, a stored scheduling snapshot or a scheduler decision.
func TestCursorUsageIsDisplayedButNeverScheduled(t *testing.T) {
	t.Parallel()
	fixture, err := os.ReadFile(filepath.Join("..", "cursorusage", "testdata", "openusage-cursor-limits.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var paths []string
	requested := make(chan struct{}, 1)
	openUsage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/v1/limits/cursor" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(fixture)
		select {
		case requested <- struct{}{}:
		default:
		}
	}))
	defer openUsage.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://127.0.0.1:1")
	cfg.CursorUsage.Source = "openusage"
	cfg.CursorUsage.OpenUsageURL = openUsage.URL
	// Inside the fixture's freshness window.
	now := time.Date(2026, 10, 9, 5, 47, 0, 0, time.UTC)
	handler := api.NewServer(cfg, db, func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartScheduler(ctx)
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("cursor tracker never asked OpenUsage")
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	var dashboard struct {
		Providers []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"providers"`
		CursorUsage *struct {
			Source string `json:"source"`
			Stale  bool   `json:"stale"`
			Meters []struct {
				Key   string `json:"key"`
				Label string `json:"label"`
				Group string `json:"group"`
			} `json:"meters"`
		} `json:"cursor_usage"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(server.URL + "/v1/dashboard")
		if err != nil {
			t.Fatal(err)
		}
		dashboard.CursorUsage = nil
		err = json.NewDecoder(resp.Body).Decode(&dashboard)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if dashboard.CursorUsage != nil && len(dashboard.CursorUsage.Meters) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dashboard never showed Cursor usage: %#v", dashboard.CursorUsage)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	handler.Wait()

	if dashboard.CursorUsage.Source != "openusage" || dashboard.CursorUsage.Stale {
		t.Fatalf("cursor usage = %#v", dashboard.CursorUsage)
	}
	if len(dashboard.Providers) != 2 {
		t.Fatalf("providers = %#v", dashboard.Providers)
	}
	for _, provider := range dashboard.Providers {
		if provider.Provider == "cursor" || provider.ID == "cursor" {
			t.Fatal("cursor must never become a scheduled provider account")
		}
	}
	if _, _, err := db.LatestSnapshot(t.Context(), "cursor", "cursor"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cursor usage was stored as a scheduling snapshot: %v", err)
	}
	for _, id := range []string{"codex-main", "claude-main"} {
		decisions, err := db.ListSchedulerDecisions(t.Context(), id, 10)
		if err != nil || len(decisions) != 0 {
			t.Fatalf("%s decisions=%d err=%v", id, len(decisions), err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, path := range paths {
		if path != "GET /v1/limits/cursor" {
			t.Fatalf("unexpected OpenUsage request %q (only the exact Cursor card may be read)", path)
		}
	}

	resp, err := http.Get(server.URL + "/v1/cursor-usage")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var direct struct {
		Cursor *struct {
			Meters []json.RawMessage `json:"meters"`
		} `json:"cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&direct); err != nil || direct.Cursor == nil || len(direct.Cursor.Meters) != 4 {
		t.Fatalf("GET /v1/cursor-usage = %#v err=%v", direct.Cursor, err)
	}
}

func TestCursorUsageIsOmittedWhenNotConfigured(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := httptest.NewServer(api.NewServer(testConfig("http://127.0.0.1:1"), db, func() time.Time { return apiNow }))
	defer server.Close()
	resp, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["cursor_usage"]; present {
		t.Fatal("cursor_usage must be omitted when Cursor tracking is off")
	}
}
