package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/store"
)

// Synthetic two-card OpenUsage limits response. Display names hold a fake
// email, as upstream does, so the test can prove none of it leaks.
const twoClaudeCards = `{
  "schema": "openusage.limits.v1",
  "generatedAt": "2026-07-16T18:00:00Z",
  "providers": {
    "claude": {
      "displayName": "Claude (fixture-one@example.invalid)", "plan": "Max 20x",
      "fetchedAt": "2026-07-16T18:00:00Z", "stale": false,
      "resources": {
        "session": {"used":36,"limit":100,"resetsAt":"2026-07-16T21:00:00Z"},
        "weekly":  {"used":59,"limit":100,"resetsAt":"2026-07-19T16:00:00Z"},
        "rateLimitResets": {"available":2,"expiresAt":["2026-07-30T00:00:00Z","2026-07-22T00:00:00Z"]}
      }
    },
    "claude@ab12cd34": {
      "displayName": "Claude (fixture-two@example.invalid)", "plan": "Team",
      "fetchedAt": "2026-07-16T17:59:00Z", "stale": true,
      "resources": {
        "session": {"used":100,"limit":100,"resetsAt":"2026-07-16T19:00:00Z"},
        "weekly":  {"used":42,"limit":100,"resetsAt":"2026-07-21T03:00:00Z"}
      }
    }
  },
  "errors": []
}`

type accountRow struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Label         string `json:"label"`
	OpenUsageCard string `json:"openusage_card"`
	AccountStatus string `json:"account_status"`
	SnapshotStale bool   `json:"snapshot_stale"`
	Snapshot      *struct {
		Account              string      `json:"account"`
		Plan                 string      `json:"plan"`
		SourceStale          bool        `json:"source_stale"`
		BankedResets         *int        `json:"banked_resets"`
		BankedResetsExpiries []time.Time `json:"banked_resets_expiries"`
		Weekly               struct {
			Remaining float64   `json:"remaining"`
			ResetsAt  time.Time `json:"resets_at"`
		} `json:"weekly"`
	} `json:"snapshot"`
}

func TestTwoClaudeAccountsReadTheirOwnCards(t *testing.T) {
	var paths []string
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if !strings.HasPrefix(r.URL.Path, "/v1/limits/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(twoClaudeCards))
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	cfg.Providers = map[string]config.Provider{
		"claude-personal": {Provider: "claude", UsageSource: "openusage", OpenUsageURL: usage.URL, OpenUsageCard: "claude",
			Label: "Personal", WindowWeeklyCost: 0.08},
		"claude-client": {Provider: "claude", UsageSource: "openusage", OpenUsageURL: usage.URL, OpenUsageCard: "claude@ab12cd34",
			Label: "Client", WindowWeeklyCost: 0.08},
	}
	if err := config.ValidateAccountIdentities(cfg.Providers); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	for _, id := range []string{"claude-personal", "claude-client"} {
		postJSON[map[string]any](t, server.URL+"/v1/providers/"+id+"/refresh", map[string]any{})
	}
	var dashboard struct {
		Providers []accountRow `json:"providers"`
	}
	getJSON(t, server.URL+"/v1/dashboard", &dashboard)
	rows := map[string]accountRow{}
	for _, row := range dashboard.Providers {
		rows[row.ID] = row
	}
	personal, client := rows["claude-personal"], rows["claude-client"]
	if personal.Snapshot == nil || client.Snapshot == nil {
		t.Fatalf("rows = %+v (paths %v)", dashboard.Providers, paths)
	}
	if personal.Label != "Personal" || personal.Snapshot.Plan != "Max 20x" || personal.Snapshot.Account != "claude-personal" ||
		personal.AccountStatus != "ok" || personal.Snapshot.BankedResets == nil || *personal.Snapshot.BankedResets != 2 ||
		len(personal.Snapshot.BankedResetsExpiries) != 2 {
		t.Fatalf("personal = %+v %+v", personal, personal.Snapshot)
	}
	if client.Label != "Client" || client.Snapshot.Plan != "Team" || client.Snapshot.Account != "claude-client" ||
		client.AccountStatus != "stale" || !client.SnapshotStale || client.Snapshot.BankedResets != nil {
		t.Fatalf("client = %+v %+v", client, client.Snapshot)
	}
	if personal.Snapshot.Weekly.ResetsAt.Equal(client.Snapshot.Weekly.ResetsAt) {
		t.Fatal("accounts must keep their own weekly reset times")
	}
	resp, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if body := string(raw); strings.Contains(body, "example.invalid") || strings.Contains(body, "fixture-one") {
		t.Fatal("dashboard leaked an OpenUsage display name")
	}
}

func TestDuplicateCardIsRejectedAtConfigTime(t *testing.T) {
	err := config.ValidateAccountIdentities(map[string]config.Provider{
		"a": {Provider: "claude", OpenUsageCard: "claude@ab12cd34"},
		"b": {Provider: "claude", OpenUsageCard: "CLAUDE@AB12CD34"},
	})
	if err == nil {
		t.Fatal("two accounts reading one card must be refused")
	}
}
