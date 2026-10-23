package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/scheduler"
	"github.com/croutoncreations/redline/internal/store"
)

func runReminderMonitor(t *testing.T, cfg config.Config, db *store.DB) scheduler.Status {
	t.Helper()
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); handler.Wait() }()
	handler.StartScheduler(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status scheduler.Status
		getJSON(t, server.URL+"/v1/usage-monitor/status", &status)
		if status.LastCycleAt != nil && !status.Running {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("usage monitor did not finish")
	return scheduler.Status{}
}

func TestUsageMonitorBankedResetReminderFailures(t *testing.T) {
	for _, failure := range []string{"hook", "fetch", "snapshot_store", "notice_store"} {
		t.Run(failure, func(t *testing.T) {
			expires := apiNow.Add(2 * time.Hour)
			payload := strings.Replace(codexPayload, `"lines":[`, fmt.Sprintf(`"lines":[{"type":"text","label":"Rate Limit Resets","value":"2 available","resetsAt":%q},`, expires.Format(time.RFC3339)), 1)
			usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if failure == "fetch" {
					http.Error(w, "offline", http.StatusServiceUnavailable)
					return
				}
				fmt.Fprint(w, payload)
			}))
			defer usage.Close()
			path := filepath.Join(t.TempDir(), "redline.db")
			db, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if failure == "snapshot_store" || failure == "notice_store" {
				raw, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				table := "usage_snapshots"
				if failure == "notice_store" {
					table = "banked_reset_notices"
				}
				_, err = raw.Exec("CREATE TRIGGER reject_insert BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(FAIL, 'store failed'); END")
				raw.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			cfg := testConfig(usage.URL)
			p := cfg.Providers["codex-main"]
			p.UsageSource = "openusage"
			cfg.Providers = map[string]config.Provider{"codex-main": p}
			cfg.UsageMonitor = config.UsageMonitor{Enabled: true, PollInterval: "1h"}
			cfg.Notifications = config.Notifications{Enabled: true, Command: "exit 9", Events: []string{domain.EventResetExpiring}}
			status := runReminderMonitor(t, cfg, db)
			wantUsageError := failure == "fetch" || failure == "snapshot_store"
			if len(status.Providers) != 1 || (status.Providers[0].Error != "") != wantUsageError {
				t.Fatalf("status=%+v", status)
			}
			_, _, snapshotErr := db.LatestSnapshot(t.Context(), "codex-main", "codex")
			if (snapshotErr != nil) != wantUsageError {
				t.Fatalf("snapshot error=%v", snapshotErr)
			}
			deliveries, err := db.ListNotificationDeliveries(t.Context(), 10)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "hook" {
				if len(deliveries) != 1 || deliveries[0].Status != "failed" {
					t.Fatalf("deliveries=%v", deliveries)
				}
			} else if len(deliveries) != 0 {
				t.Fatalf("deliveries=%v", deliveries)
			}
			if wantUsageError {
				inserted, err := db.RecordBankedResetNotice(t.Context(), "codex-main", "aggregate", expires, "remind:24h", apiNow)
				if err != nil || !inserted {
					t.Fatalf("notice claimed after failed fetch/store: inserted=%v err=%v", inserted, err)
				}
			}
		})
	}
}

func TestUsageMonitorBankedResetRemindersWithoutScheduler(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"providerId":"codex","fetchedAt":%q,"lines":[
		{"type":"progress","label":"Weekly","used":10,"limit":100,"resetsAt":%q},
		{"type":"text","label":"Rate Limit Resets","value":"2 available","resetsAt":%q}]}`,
			apiNow.Format(time.RFC3339), apiNow.Add(48*time.Hour).Format(time.RFC3339), apiNow.Add(2*time.Hour).Format(time.RFC3339))
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	delete(cfg.Providers, "claude-main")
	p := cfg.Providers["codex-main"]
	p.UsageSource = "openusage"
	cfg.Providers["codex-main"] = p
	cfg.UsageMonitor = config.UsageMonitor{Enabled: true, PollInterval: "1h"}
	for i, notifications := range []config.Notifications{
		{Command: ":"},
		{Enabled: true, Command: ":", Events: []string{domain.EventRunStarted}},
		{Enabled: true, Command: ":", Events: []string{domain.EventResetExpiring}},
		{Enabled: true, Command: ":", Events: []string{domain.EventResetExpiring}},
	} {
		cfg.Notifications = notifications
		status := runReminderMonitor(t, cfg, db)
		if status.LastCycleAt == nil || len(status.Providers) != 1 || status.Providers[0].Error != "" {
			t.Fatalf("monitor status=%+v", status)
		}
		if i < 2 {
			deliveries, err := db.ListNotificationDeliveries(t.Context(), 10)
			if err != nil || len(deliveries) != 0 {
				t.Fatalf("disabled notifications: deliveries=%v err=%v", deliveries, err)
			}
		}
	}
	deliveries, err := db.ListNotificationDeliveries(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].Status != "delivered" {
		t.Fatalf("deliveries=%+v", deliveries)
	}
	var event domain.NotificationEvent
	if err := json.Unmarshal(deliveries[0].Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != domain.EventResetExpiring || event.ProviderAccountID != "codex-main" || event.Data["reset_id"] != "aggregate" || event.Data["count"] != "2" {
		t.Fatalf("event=%+v", event)
	}
}
