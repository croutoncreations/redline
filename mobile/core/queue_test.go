package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchQueueOrdersAndExplains(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	body := `{
	  "provider_account_id":"claude-main",
	  "generated_at":"2026-09-02T11:55:00Z",
	  "snapshot_observed_at":"2026-09-02T11:55:00Z",
	  "snapshot_stale":false,
	  "dispatch_available":true,
	  "provider_reason":"",
	  "selected_task_id":"docs-refresh",
	  "candidates":[
	    {"task_id":"bug-hunt","name":"Find one real bug","priority":80,"eligible":false,
	     "reason":"cooldown until 2026-09-03T03:03:49Z"},
	    {"task_id":"docs-refresh","name":"Fix stale docs","priority":55,"eligible":true,"reason":""}
	  ]}`
	server, _ := runsServer(t, body, http.StatusOK)

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, err := client.FetchQueue("claude-main")
	if err != nil {
		t.Fatalf("FetchQueue: %v", err)
	}

	var view QueueView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if view.ProviderAccountID != "claude-main" {
		t.Errorf("provider = %q", view.ProviderAccountID)
	}
	if !view.DispatchAvailable {
		t.Error("dispatch should be available")
	}
	// "Snapshot 5 mins ago" is what the web dashboard shows, and a timestamp
	// would make the reader do the arithmetic.
	if view.SnapshotLabel != "5m ago" {
		t.Errorf("snapshot label = %q, want 5m ago", view.SnapshotLabel)
	}
	if view.ReadyCount != 1 || view.BlockedCount != 1 {
		t.Errorf("ready/blocked = %d/%d, want 1/1", view.ReadyCount, view.BlockedCount)
	}
	// The task the scheduler would pick next is the single most useful fact on
	// the screen, so it is named rather than left to be inferred.
	if view.NextUpTaskID != "docs-refresh" || view.NextUpName != "Fix stale docs" {
		t.Errorf("next up = %q/%q", view.NextUpTaskID, view.NextUpName)
	}

	blocked := view.Candidates[0]
	if blocked.Eligible {
		t.Error("first candidate is blocked")
	}
	// A raw timestamp in a blocking reason is unreadable at a glance; the
	// point is how long the wait is.
	if blocked.Reason != "cooldown for 15h 3m" {
		t.Errorf("reason = %q, want a humanised cooldown", blocked.Reason)
	}
}

// A reason that is not a cooldown must survive untouched: "repository has not
// changed since the last successful run" is already the explanation.
func TestFetchQueueKeepsNonCooldownReasons(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	body := `{"provider_account_id":"c","dispatch_available":true,"candidates":[
	  {"task_id":"t","name":"T","priority":70,"eligible":false,
	   "reason":"repository has not changed since the last successful run"}]}`
	server, _ := runsServer(t, body, http.StatusOK)

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, _ := client.FetchQueue("c")
	var view QueueView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Candidates[0].Reason != "repository has not changed since the last successful run" {
		t.Errorf("reason = %q", view.Candidates[0].Reason)
	}
}

// A cooldown that has already passed must not render as a negative wait.
func TestFetchQueueHandlesElapsedCooldown(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	body := `{"provider_account_id":"c","dispatch_available":true,"candidates":[
	  {"task_id":"t","name":"T","priority":70,"eligible":false,
	   "reason":"cooldown until 2026-09-02T11:00:00Z"}]}`
	server, _ := runsServer(t, body, http.StatusOK)

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, _ := client.FetchQueue("c")
	var view QueueView
	_ = json.Unmarshal([]byte(raw), &view)
	if strings.Contains(view.Candidates[0].Reason, "-") {
		t.Errorf("an elapsed cooldown must not show a negative wait: %q", view.Candidates[0].Reason)
	}
}

// A stale snapshot means the numbers behind the queue are old, which changes
// whether the ordering can be trusted.
func TestFetchQueueMarksAStaleSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	body := `{"provider_account_id":"c","snapshot_observed_at":"2026-09-02T10:00:00Z",
	  "snapshot_stale":true,"dispatch_available":false,
	  "provider_reason":"provider is paused","candidates":[]}`
	server, _ := runsServer(t, body, http.StatusOK)

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, _ := client.FetchQueue("c")
	var view QueueView
	_ = json.Unmarshal([]byte(raw), &view)

	if !view.SnapshotStale {
		t.Error("stale snapshot must be marked")
	}
	if view.ProviderReason != "provider is paused" {
		t.Errorf("provider reason = %q", view.ProviderReason)
	}
	if view.DispatchAvailable {
		t.Error("dispatch is not available")
	}
}

// Provider controls are what make the app a replacement for the web dashboard
// rather than a viewer.
func TestProviderControls(t *testing.T) {
	for _, testCase := range []struct {
		control string
		path    string
	}{
		{"pause", "/v1/providers/claude-main/pause"},
		{"resume", "/v1/providers/claude-main/resume"},
		{"refresh", "/v1/providers/claude-main/refresh"},
	} {
		t.Run(testCase.control, func(t *testing.T) {
			var seen string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.URL.Path
				if r.Method != http.MethodPost {
					t.Errorf("method = %s", r.Method)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			client := NewClient(server.URL, "token")
			if err := client.ControlProvider("claude-main", testCase.control); err != nil {
				t.Fatalf("ControlProvider: %v", err)
			}
			if seen != testCase.path {
				t.Errorf("path = %q, want %q", seen, testCase.path)
			}
		})
	}
}

// An unknown control must be refused locally rather than sent, since the URL
// is built from the value and a typo would hit an unintended endpoint.
func TestControlProviderRejectsUnknownControls(t *testing.T) {
	client := NewClient("http://example.invalid", "token")
	if err := client.ControlProvider("claude-main", "delete"); err == nil {
		t.Error("an unsupported control must be refused")
	}
}

func TestTaskControls(t *testing.T) {
	for _, control := range []string{"enable", "disable"} {
		t.Run(control, func(t *testing.T) {
			var seen string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.URL.Path
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			client := NewClient(server.URL, "token")
			if err := client.ControlTask("docs-refresh", control); err != nil {
				t.Fatalf("ControlTask: %v", err)
			}
			if seen != "/v1/tasks/docs-refresh/"+control {
				t.Errorf("path = %q", seen)
			}
		})
	}
}

func TestControlTaskRejectsUnknownControls(t *testing.T) {
	client := NewClient("http://example.invalid", "token")
	if err := client.ControlTask("t", "archive"); err == nil {
		t.Error("an unsupported task control must be refused")
	}
}
