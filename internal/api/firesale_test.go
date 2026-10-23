package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func newFiresaleServer(t *testing.T, firesale bool, executor api.Executor) (*api.Server, *httptest.Server, *store.DB) {
	t.Helper()
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, codexPayload) }))
	t.Cleanup(usage.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := testConfig(usage.URL)
	delete(cfg.Providers, "claude-main")
	trigger := 0.30
	if firesale {
		policy := cfg.Policies["standard"]
		policy.FiresaleTrigger = &trigger
		cfg.Policies["standard"] = policy
	}
	provider := cfg.Providers["codex-main"]
	provider.FiresaleMaxConcurrentRuns = 2
	cfg.Providers["codex-main"] = provider
	cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
	cfg.Notifications = config.Notifications{Enabled: true, Command: "true", Events: []string{"firesale.started", "firesale.ended"}}
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "profile", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory"}, apiNow); err != nil {
		t.Fatal(err)
	}
	handler := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, executor)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return handler, server, db
}

func TestFiresaleUsesTaskIntervalAndUnlocksTier(t *testing.T) {
	for _, firesale := range []bool{false, true} {
		t.Run(fmt.Sprint(firesale), func(t *testing.T) {
			_, server, db := newFiresaleServer(t, firesale, fakeExecutor{})
			completed := apiNow.Add(-time.Hour)
			if err := db.CreateTask(t.Context(), domain.Task{ID: "job", Name: "Job", ExecutionProfileID: "profile", Type: domain.Recurring, DispatchTier: domain.DispatchFiresale, MinInterval: 24 * time.Hour, FiresaleMinInterval: 30 * time.Minute, LastCompletedAt: &completed, LastSuccessfulSourceRevision: "same"}, apiNow); err != nil {
				t.Fatal(err)
			}
			response := postJSON[struct {
				Result       decision.Result `json:"result"`
				SelectedTask *domain.Task    `json:"selected_task"`
			}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
			if (response.SelectedTask != nil) != firesale || response.Result.Firesale != firesale {
				t.Fatalf("response=%+v", response)
			}
			task, _ := db.GetTask(t.Context(), "job")
			task.RequireRepoChange = true
			task.LastSuccessfulSourceRevision = "same"
			if err := db.UpdateTask(t.Context(), task, apiNow); err != nil {
				t.Fatal(err)
			}
			response = postJSON[struct {
				Result       decision.Result `json:"result"`
				SelectedTask *domain.Task    `json:"selected_task"`
			}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main", "current_revision": "same"})
			if response.SelectedTask != nil {
				t.Fatal("firesale bypassed repository gate")
			}
		})
	}
}

func TestFiresaleCooldownAdmission(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, firesale := range []bool{false, true} {
			t.Run(fmt.Sprintf("automatic=%v/firesale=%v", automatic, firesale), func(t *testing.T) {
				started := make(chan struct{}, 1)
				release := make(chan struct{})
				handler, server, db := newFiresaleServer(t, firesale, fakeExecutor{execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error {
					started <- struct{}{}
					<-release
					return nil
				}})
				completed := apiNow.Add(-time.Hour)
				if err := db.CreateTask(t.Context(), domain.Task{ID: "cooling", Name: "Cooling", ExecutionProfileID: "profile", Type: domain.Recurring, MinInterval: 24 * time.Hour, FiresaleMinInterval: 30 * time.Minute, LastCompletedAt: &completed}, apiNow); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer func() { cancel(); close(release); handler.Wait() }()
				if automatic {
					handler.StartScheduler(ctx)
					deadline := time.Now().Add(2 * time.Second)
					for {
						attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
						if err != nil {
							t.Fatal(err)
						}
						if len(attempts) > 0 {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("automatic dispatch did not finish")
						}
						time.Sleep(5 * time.Millisecond)
					}
				} else {
					response := postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/execute", map[string]any{"provider_account_id": "codex-main"})
					if (response.Run != nil) != firesale {
						t.Fatalf("response=%+v", response)
					}
				}
				active, err := db.ActiveRunCount(t.Context(), "codex-main")
				want := 0
				if firesale {
					want = 1
				}
				if err != nil || active != want {
					t.Fatalf("active=%d want=%d err=%v", active, want, err)
				}
			})
		}
	}
}

func TestAutomaticFiresaleRaisesConcurrency(t *testing.T) {
	for _, firesale := range []bool{false, true} {
		t.Run(fmt.Sprint(firesale), func(t *testing.T) {
			started := make(chan string, 2)
			release := make(chan struct{})
			handler, _, db := newFiresaleServer(t, firesale, fakeExecutor{execute: func(_ context.Context, _ domain.Run, task domain.Task, _ domain.ExecutionProfile) error {
				started <- task.ID
				<-release
				return nil
			}})
			for _, id := range []string{"first", "second"} {
				if err := db.CreateTask(t.Context(), domain.Task{ID: id, Name: id, ExecutionProfileID: "profile", Type: domain.OneOff}, apiNow); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer func() { cancel(); close(release); handler.Wait() }()
			handler.StartScheduler(ctx)
			want := 1
			if firesale {
				want = 2
			}
			for range want {
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("run not admitted")
				}
			}
			if !firesale {
				select {
				case <-started:
					t.Fatal("raised cap outside firesale")
				case <-time.After(50 * time.Millisecond):
				}
			}
			active, err := db.ActiveRunCount(t.Context(), "codex-main")
			if err != nil || active != want {
				t.Fatalf("active=%d err=%v", active, err)
			}
		})
	}
}

func TestAutomaticFiresaleNotificationsTransitionOnly(t *testing.T) {
	for _, tc := range []struct {
		previous, current bool
		event             string
	}{
		{false, true, "firesale.started"}, {true, false, "firesale.ended"}, {true, true, ""}, {false, false, ""},
	} {
		t.Run(fmt.Sprintf("%v-%v", tc.previous, tc.current), func(t *testing.T) {
			handler, server, db := newFiresaleServer(t, tc.current, fakeExecutor{})
			record := func(raw string) {
				t.Helper()
				if _, err := db.RecordSchedulerDecision(t.Context(), domain.SchedulerDecision{ProviderAccountID: "codex-main", DecisionJSON: []byte(raw)}, apiNow.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			record(fmt.Sprintf(`{"trigger":"automatic","provider_firesale":%v}`, tc.previous))
			record(`{"trigger":"automatic","result":{"decision":"WAIT","firesale":false}}`)
			// Manual evaluations neither emit nor override the automatic state.
			postJSON[map[string]any](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
			ctx, cancel := context.WithCancel(context.Background())
			handler.StartScheduler(ctx)
			defer func() { cancel(); handler.Wait() }()
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
				if err != nil {
					t.Fatal(err)
				}
				if len(attempts) > 0 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			deliveries, err := db.ListNotificationDeliveries(t.Context(), 10)
			if err != nil {
				t.Fatal(err)
			}
			if tc.event == "" {
				if len(deliveries) != 0 {
					t.Fatalf("duplicate transition: %+v", deliveries)
				}
				return
			}
			if len(deliveries) != 1 || deliveries[0].EventType != tc.event {
				t.Fatalf("deliveries=%+v", deliveries)
			}
			var event domain.NotificationEvent
			if err := json.Unmarshal(deliveries[0].Payload, &event); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"pace_gap", "overflow", "effective_reset_at", "unlocked_tier"} {
				if event.Data[key] == "" {
					t.Fatalf("missing %s: %+v", key, event)
				}
			}
		})
	}
}

func TestFiresaleCompletionTriggersPromptRedispatch(t *testing.T) {
	started := make(chan string, 2)
	finish := make(chan struct{})
	var db *store.DB
	handler, _, database := newFiresaleServer(t, true, fakeExecutor{execute: func(ctx context.Context, run domain.Run, task domain.Task, _ domain.ExecutionProfile) error {
		started <- task.ID
		select {
		case <-finish:
		case <-ctx.Done():
			return ctx.Err()
		}
		return db.CompleteRun(ctx, run.ID, domain.RunCompletion{State: domain.RunCompleted}, apiNow)
	}})
	db = database
	if err := db.CreateTask(t.Context(), domain.Task{ID: "first", Name: "First", ExecutionProfileID: "profile", Type: domain.OneOff}, apiNow); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); close(finish); handler.Wait() }()
	handler.StartScheduler(ctx)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("initial run missing")
	}
	// Wait for the initial cycle before adding the successor, ensuring only Trigger can dispatch it.
	deadline := time.Now().Add(time.Second)
	for {
		attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(attempts) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial cycle did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "next", Name: "Next", ExecutionProfileID: "profile", Type: domain.OneOff}, apiNow); err != nil {
		t.Fatal(err)
	}
	finish <- struct{}{}
	select {
	case id := <-started:
		if id != "next" {
			t.Fatalf("run=%s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion did not redispatch")
	}
}

func TestModelPoolFiresaleUnlocksTierWithoutProviderOverrides(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("")
	delete(cfg.Providers, "codex-main")
	trigger := 0.45
	policy := cfg.Policies["standard"]
	policy.FiresaleTrigger = &trigger
	cfg.Policies["standard"] = policy
	provider := cfg.Providers["claude-main"]
	provider.FiresaleMaxConcurrentRuns = 2
	cfg.Providers["claude-main"] = provider
	snapshot := decision.UsageSnapshot{Provider: "claude", ObservedAt: apiNow, Source: "test", Weekly: decision.UsageWindow{Remaining: 0.30, ResetsAt: apiNow.Add(72 * time.Hour)}, Allowances: []decision.AllowanceWindow{{Key: "model:fable:weekly", SourceLabel: "Fable", Scope: "model", Role: "weekly", Remaining: 0.90, ResetsAt: apiNow.Add(24 * time.Hour), PeriodDurationSeconds: 604800}}}
	handler := api.NewDemoServer(cfg, db, func() time.Time { return apiNow }, map[string]decision.UsageSnapshot{"claude-main": snapshot}, nil, fakeExecutor{execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil }})
	defer handler.Wait()
	server := httptest.NewServer(handler)
	defer server.Close()
	createClaudeCandidate(t, db, "profile", "fable", "fable", "first", 100)
	task, _ := db.GetTask(t.Context(), "first")
	task.DispatchTier = domain.DispatchFiresale
	if err := db.UpdateTask(t.Context(), task, apiNow); err != nil {
		t.Fatal(err)
	}
	response := postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-main"})
	if response.SelectedTask == nil || response.Result.UnlockedTier != domain.DispatchFiresale || response.Result.Firesale {
		t.Fatalf("pool firesale=%+v", response)
	}
	// A pool-only firesale does not relax the provider's cooldown.
	completed := apiNow.Add(-time.Hour)
	if err := db.CreateTask(t.Context(), domain.Task{ID: "cooling", Name: "Cooling", Priority: 200, ExecutionProfileID: "profile", Type: domain.Recurring, MinInterval: 24 * time.Hour, FiresaleMinInterval: time.Minute, LastCompletedAt: &completed}, apiNow); err != nil {
		t.Fatal(err)
	}
	response = postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/execute", map[string]any{"provider_account_id": "claude-main"})
	if response.SelectedTask == nil || response.SelectedTask.ID != "first" || len(response.Result.CandidateRejections) == 0 || !strings.Contains(response.Result.CandidateRejections[0].Reason, "cooldown") {
		t.Fatalf("cooldown=%+v", response)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "next", Name: "Next", ExecutionProfileID: "profile", Type: domain.OneOff}, apiNow); err != nil {
		t.Fatal(err)
	}
	response = postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/execute", map[string]any{"provider_account_id": "claude-main"})
	if response.Run != nil || response.Result.Mode != decision.ModeActive {
		t.Fatalf("pool raised provider cap: %+v", response)
	}
}

type firesaleRevisionBarrier struct {
	calls atomic.Int32
	ready chan struct{}
}

func (b *firesaleRevisionBarrier) Resolve(ctx context.Context, _ domain.ExecutionProfile) (string, error) {
	if b.calls.Add(1) == 2 {
		close(b.ready)
	}
	select {
	case <-b.ready:
		return "revision", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestFiresaleAdmissionConflictRemainsWait(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, codexPayload) }))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	trigger := 0.30
	policy := cfg.Policies["standard"]
	policy.FiresaleTrigger = &trigger
	cfg.Policies["standard"] = policy
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "profile", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory", Repository: "/repo"}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "job", Name: "Job", ExecutionProfileID: "profile", Type: domain.OneOff}, apiNow); err != nil {
		t.Fatal(err)
	}
	barrier := &firesaleRevisionBarrier{ready: make(chan struct{})}
	handler := api.NewServerWithDependencies(cfg, db, func() time.Time { return apiNow }, fakeExecutor{execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil }}, barrier)
	defer handler.Wait()
	server := httptest.NewServer(handler)
	defer server.Close()
	responses := make(chan schedulerResponseForTest, 2)
	for range 2 {
		go func() {
			response, err := http.Post(server.URL+"/v1/scheduler/execute", "application/json", strings.NewReader(`{"provider_account_id":"codex-main"}`))
			if err != nil {
				t.Error(err)
				responses <- schedulerResponseForTest{}
				return
			}
			defer response.Body.Close()
			var result schedulerResponseForTest
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			responses <- result
		}()
	}
	waits := 0
	for range 2 {
		select {
		case response := <-responses:
			if response.Result.Decision == decision.Wait {
				waits++
				if response.Result.Firesale {
					t.Fatalf("WAIT marked firesale: %+v", response)
				}
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent requests did not complete")
		}
	}
	if waits != 1 {
		t.Fatalf("waits=%d", waits)
	}
}

func TestTaskFiresaleIntervalAPI(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}, apiNow); err != nil {
		t.Fatal(err)
	}
	task := postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{"id": "fire", "name": "Fire", "prompt": "Test", "execution_profile_id": "profile", "type": "recurring", "min_interval": "1h", "firesale_min_interval": "2h", "dispatch_tier": "firesale"})
	if task.FiresaleMinInterval != 2*time.Hour || task.DispatchTier != domain.DispatchFiresale {
		t.Fatalf("task=%+v", task)
	}
	for _, value := range []string{"10m", ""} {
		req, _ := http.NewRequest(http.MethodPatch, server.URL+"/v1/tasks/"+task.ID, strings.NewReader(`{"firesale_min_interval":"`+value+`"}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("patch status=%d", resp.StatusCode)
		}
		got, err := db.GetTask(t.Context(), task.ID)
		want := 10 * time.Minute
		if value == "" {
			want = 0
		}
		if err != nil || got.FiresaleMinInterval != want {
			t.Fatalf("task=%+v err=%v", got, err)
		}
	}
	for _, value := range []string{"0s", "-1h", "invalid"} {
		req, _ := http.NewRequest(http.MethodPatch, server.URL+"/v1/tasks/"+task.ID, strings.NewReader(`{"firesale_min_interval":"`+value+`"}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid %q status=%d", value, resp.StatusCode)
		}
	}
}
