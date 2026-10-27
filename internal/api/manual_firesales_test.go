package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/usage"
)

func TestManualFiresaleExpiryWhilePaused(t *testing.T) {
	s, db, _ := redemptionServer(t, "claude")
	n := &redemptionNotifier{}
	s.notifier = n
	if _, _, err := db.StartManualFiresale(t.Context(), "main", s.now(), s.now().Add(time.Minute), "owner", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProviderPaused(t.Context(), "main", true); err != nil {
		t.Fatal(err)
	}
	now := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	if err := s.dispatchAutomatic(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	if len(n.events) != 1 || n.events[0].Data["reason"] != "deadline_passed" {
		t.Fatalf("events=%+v", n.events)
	}
}

type manualNoticeObserver func(domain.NotificationEvent) error

func (f manualNoticeObserver) Notify(_ context.Context, e domain.NotificationEvent) error {
	return f(e)
}

func TestManualFiresaleNoticesPersistedOutsideLocks(t *testing.T) {
	s, db, _ := redemptionServer(t, "claude")
	calls := 0
	s.notifier = manualNoticeObserver(func(e domain.NotificationEvent) error {
		calls++
		if !s.manualFiresaleMu.TryLock() {
			t.Fatal("notification delivered under lock")
		}
		s.manualFiresaleMu.Unlock()
		_, err := db.ActiveManualFiresaleFor(t.Context(), "main")
		if (e.Type == domain.EventFiresaleEnded) != errors.Is(err, store.ErrNotFound) {
			t.Fatalf("not persisted: event=%+v err=%v", e, err)
		}
		return errors.New("hook failed")
	})
	if w := manualAction(s, "POST", "/v1/firesales", `{"account":"main","until":"15m"}`, "secret"); w.Code != 500 {
		t.Fatalf("start=%d %s", w.Code, w.Body)
	}
	if w := manualAction(s, "POST", "/v1/firesales", `{"account":"main","until":"15m"}`, "secret"); w.Code != 200 {
		t.Fatalf("extend=%d %s", w.Code, w.Body)
	}
	if w := manualAction(s, "DELETE", "/v1/firesales/main", "", "secret"); w.Code != 500 {
		t.Fatalf("stop=%d %s", w.Code, w.Body)
	}
	if err := s.reconcileManualFiresales(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func manualAction(s *Server, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestManualFiresalesAPI(t *testing.T) {
	s, db, snapshot := redemptionServer(t, "claude")
	n := &redemptionNotifier{}
	s.notifier = n
	for _, method := range []string{"GET", "POST", "DELETE"} {
		path := "/v1/firesales"
		if method == "DELETE" {
			path += "/main"
		}
		if w := manualAction(s, method, path, `{}`, ""); w.Code != 401 {
			t.Fatalf("unauth %s=%d", method, w.Code)
		}
	}
	if err := db.CreateDeviceSession(t.Context(), store.DeviceSession{ID: "phone", Name: "My phone", Kind: "mobile", CreatedAt: s.now(), LastUsedAt: s.now()}, deviceTokenHash("phone-token"), s.sessionIssuer()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`null`, 400}, {`{}`, 400}, {`{"account":"missing","until":"15m"}`, 404},
		{`{"account":"main","until":"4m"}`, 400}, {`{"account":"main","until":"169h"}`, 400}, {`{"account":"main","until":"nonsense"}`, 400},
		{`{"account":"main","until":"15m"}`, 201},
		{`{"account":"main","until":"2h"}`, 200},
	} {
		w := manualAction(s, "POST", "/v1/firesales", tc.body, "phone-token")
		if w.Code != tc.status {
			t.Fatalf("body=%s status=%d response=%s", tc.body, w.Code, w.Body.String())
		}
	}
	if len(n.events) != 1 || n.events[0].Data["trigger"] != "manual" || n.events[0].Data["started_by"] != "device:My phone" {
		t.Fatalf("events=%+v", n.events)
	}
	w := manualAction(s, "GET", "/v1/firesales", "", "phone-token")
	var state struct {
		Manual   []store.ManualFiresale `json:"manual"`
		Eligible []struct {
			Account, Label, Provider string
			WeeklyResetsAt           *time.Time `json:"weekly_resets_at"`
			Reason                   *string    `json:"reason_unavailable"`
		} `json:"eligible"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || w.Code != 200 || len(state.Manual) != 1 || len(state.Eligible) != 1 {
		t.Fatalf("status=%d state=%+v err=%v", w.Code, state, err)
	}
	if state.Eligible[0].Account != "main" || state.Eligible[0].Label != "main" || state.Eligible[0].Provider != "claude" || !state.Eligible[0].WeeklyResetsAt.Equal(snapshot.Weekly.ResetsAt) || state.Eligible[0].Reason != nil {
		t.Fatalf("eligible=%+v", state.Eligible)
	}
	for _, tc := range []struct {
		account string
		status  int
	}{{"missing", 404}, {"main", 204}, {"main", 404}} {
		if w := manualAction(s, "DELETE", "/v1/firesales/"+tc.account, "", "phone-token"); w.Code != tc.status {
			t.Fatalf("stop %s=%d %s", tc.account, w.Code, w.Body.String())
		}
	}
	if len(n.events) != 2 || n.events[1].Data["reason"] != "stopped" {
		t.Fatalf("events=%+v", n.events)
	}
	if w := manualAction(s, "POST", "/v1/firesales", `{"account":"main","until":"weekly_reset"}`, "secret"); w.Code != 201 {
		t.Fatalf("reset start=%d %s", w.Code, w.Body.String())
	}
	sale, _ := db.ActiveManualFiresaleFor(t.Context(), "main")
	if !sale.Deadline.Equal(snapshot.Weekly.ResetsAt) || sale.StartedBy != "owner" {
		t.Fatalf("sale=%+v", sale)
	}
}

func TestResetAnnouncedSampleManualActionRequiresExplicitProvider(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		s, db, _ := redemptionServer(t, "claude")
		s.config.Notifications.Enabled = true
		s.notifier = nil // Build the test service from the enabled configuration.
		body := `{"event":"reset.announced"}`
		if explicit {
			body = `{"event":"reset.announced","provider":"main"}`
		}
		w := manualAction(s, "POST", "/v1/notifications/test", body, "secret")
		var delivery domain.NotificationDelivery
		if err := json.Unmarshal(w.Body.Bytes(), &delivery); err != nil || w.Code != 200 {
			t.Fatalf("test=%d %s err=%v", w.Code, w.Body, err)
		}
		var event domain.NotificationEvent
		if err := json.Unmarshal(delivery.Payload, &event); err != nil {
			t.Fatal(err)
		}
		if explicit {
			if event.Data["manual_account"] != "main" || event.Data["manual_for"] != "15m" || event.Data["kind"] != "PROMISED" {
				t.Fatalf("event=%+v", event)
			}
		} else if event.Data["manual_account"] != "" || event.Data["kind"] != "COMPLETED" {
			t.Fatalf("event=%+v", event)
		}
		if sales, err := db.ActiveManualFiresales(t.Context()); err != nil || len(sales) != 0 {
			t.Fatalf("sample started sale: %+v err=%v", sales, err)
		}
	}
}

func TestManualFiresaleUnavailableReasonsAndConflicts(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{{"missing usage", "weekly usage not reported"}, {"empty", "weekly allowance used up"}, {"paused", "provider paused"}, {"demo", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			if tc.name == "missing usage" {
				provider := s.config.Providers["main"]
				provider.Provider = "codex"
				s.config.Providers["main"] = provider
			}
			if tc.name == "empty" {
				snapshot.Weekly.Remaining = 0
				snapshot.ObservedAt = snapshot.ObservedAt.Add(time.Second)
				s.now = func() time.Time { return snapshot.ObservedAt }
				if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "paused" {
				if err := db.SetProviderPaused(t.Context(), "main", true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "demo" {
				s.redemptionDisabled = true
			}
			w := manualAction(s, "GET", "/v1/firesales", "", "secret")
			if w.Code != 200 {
				t.Fatalf("get=%d %s", w.Code, w.Body)
			}
			if tc.reason != "" && !strings.Contains(w.Body.String(), `"reason_unavailable":"`+tc.reason+`"`) {
				t.Fatalf("reason missing: %s", w.Body)
			}
			if tc.name == "missing usage" && !strings.Contains(w.Body.String(), `"weekly_resets_at":null`) {
				t.Fatal(w.Body)
			}
			w = manualAction(s, "POST", "/v1/firesales", `{"account":"main","until":"15m"}`, "secret")
			want := 201
			if tc.name == "demo" {
				want = 409
			}
			if w.Code != want {
				t.Fatalf("start=%d %s", w.Code, w.Body)
			}
			if tc.name == "missing usage" {
				w = manualAction(s, "POST", "/v1/firesales", `{"account":"main","until":"weekly_reset"}`, "secret")
				if w.Code != 409 {
					t.Fatalf("until-reset=%d %s", w.Code, w.Body)
				}
			}
		})
	}
}

func TestManualFiresaleSchedulerSafety(t *testing.T) {
	s, db, snapshot := redemptionServer(t, "claude")
	provider := s.config.Providers["main"]
	provider.WindowWeeklyCost = .1
	s.config.Providers["main"] = provider
	s.config.ActivePolicy = "standard"
	trigger := .9
	s.config.Policies = map[string]config.Policy{"standard": {PaceGapTrigger: &trigger}}
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "profile", ProviderAccountID: "main", HarnessType: "command", WorkspaceProvider: "existing-directory"}, s.now()); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "job", Name: "Job", ExecutionProfileID: "profile", Type: domain.OneOff, DispatchTier: domain.DispatchFiresale}, s.now()); err != nil {
		t.Fatal(err)
	}
	if w := manualAction(s, "POST", "/v1/firesales", `{"account":"main","until":"15m"}`, "secret"); w.Code != 201 {
		t.Fatalf("start=%d %s", w.Code, w.Body)
	}
	evaluate := func() schedulerResponse {
		t.Helper()
		w := manualAction(s, "POST", "/v1/scheduler/evaluate", `{"provider_account_id":"main"}`, "secret")
		var result schedulerResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
			t.Fatalf("evaluate=%d %s err=%v", w.Code, w.Body, err)
		}
		return result
	}
	if result := evaluate(); result.SelectedTask == nil || result.Result.UnlockedTier != domain.DispatchFiresale {
		t.Fatalf("not unlocked: %+v", result)
	}
	if err := db.SetProviderPaused(t.Context(), "main", true); err != nil {
		t.Fatal(err)
	}
	if result := evaluate(); result.SelectedTask != nil || result.Result.Mode != decision.ModePaused {
		t.Fatalf("pause bypassed: %+v", result)
	}
	if err := db.SetProviderPaused(t.Context(), "main", false); err != nil {
		t.Fatal(err)
	}
	base, err := s.evaluateStoredProvider(t.Context(), "main", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	profile := domain.ExecutionProfile{Model: "opus", BudgetModelGroup: "opus"}
	provider.ModelGroups = map[string]config.ModelGroup{"opus": {}}
	s.config.Providers["main"] = provider
	snapshot.Allowances = []decision.AllowanceWindow{{Key: "model:opus:weekly", SourceLabel: "Opus", Scope: "model", Role: "weekly", Remaining: 0, ResetsAt: snapshot.Weekly.ResetsAt, PeriodDurationSeconds: 604800}}
	if _, ok, reason := s.evaluateCandidateBudget("main", snapshot, base, profile); ok || !strings.Contains(reason, "exhausted") {
		t.Fatalf("budget bypassed: ok=%v reason=%s", ok, reason)
	}
	// Expired sales cannot force decisions, even before a separate monitor tick.
	now := s.now().Add(16 * time.Minute)
	s.now = func() time.Time { return now }
	snapshot.ObservedAt = now
	snapshot.Allowances = nil
	source := staticUsageSource{snapshots: map[string]decision.UsageSnapshot{"main": snapshot}}
	s.usageSources = usage.NewManager(source, source, s.now)
	if result := evaluate(); result.SelectedTask != nil || result.Result.Firesale {
		t.Fatalf("expired forced: %+v", result)
	}
}

func TestManualFiresaleDecisionSafety(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*decision.UsageSnapshot)
		want   decision.Decision
	}{
		{"reported", func(*decision.UsageSnapshot) {}, decision.Admit},
		{"not reported", func(s *decision.UsageSnapshot) { s.Weekly = decision.UsageWindow{} }, decision.Unknown},
		{"empty", func(s *decision.UsageSnapshot) { s.Weekly.Remaining = 0 }, decision.Wait},
		{"stale", func(s *decision.UsageSnapshot) { s.ObservedAt = s.ObservedAt.Add(-24 * time.Hour) }, decision.Unknown},
		{"reserve", func(s *decision.UsageSnapshot) {
			s.Short = &decision.UsageWindow{Remaining: .1, ResetsAt: s.ObservedAt.Add(time.Hour)}
		}, decision.Wait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			provider := s.config.Providers["main"]
			provider.WindowWeeklyCost = .1
			s.config.Providers["main"] = provider
			s.config.ActivePolicy = "standard"
			trigger := .9
			s.config.Policies = map[string]config.Policy{"standard": {PaceGapTrigger: &trigger, RollingReserve: .2}}
			if _, _, err := db.StartManualFiresale(t.Context(), "main", s.now(), s.now().Add(time.Hour), "owner", nil); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&snapshot)
			result, err := s.evaluateStoredProvider(t.Context(), "main", snapshot)
			if err != nil || result.Decision != tc.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.want == decision.Admit && (!result.Firesale || result.UnlockedTier != domain.DispatchFiresale || result.FiresaleTrigger != "manual" || !result.EffectiveResetAt.Equal(snapshot.Weekly.ResetsAt)) {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestManualFiresaleLifecycle(t *testing.T) {
	for _, reason := range []string{"deadline_passed", "completed", "remaining_only", "jitter", "no_baseline"} {
		t.Run(reason, func(t *testing.T) {
			s, db, snapshot := redemptionServer(t, "claude")
			n := &redemptionNotifier{}
			s.notifier = n
			baseline := &snapshot.Weekly.ResetsAt
			if reason == "no_baseline" {
				baseline = nil
			}
			if _, _, err := db.StartManualFiresale(t.Context(), "main", s.now(), s.now().Add(time.Hour), "owner", baseline); err != nil {
				t.Fatal(err)
			}
			now := s.now().Add(time.Minute)
			if reason == "deadline_passed" {
				now = now.Add(time.Hour)
			}
			s.now = func() time.Time { return now }
			snapshot.ObservedAt = now
			snapshot.Weekly.Remaining = .9
			if reason == "completed" || reason == "no_baseline" {
				snapshot.Weekly.ResetsAt = snapshot.Weekly.ResetsAt.Add(time.Hour)
			}
			if reason == "jitter" {
				snapshot.Weekly.ResetsAt = snapshot.Weekly.ResetsAt.Add(59 * time.Minute)
			}
			if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := s.reconcileManualFiresales(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			want := reason == "deadline_passed" || reason == "completed"
			sales, err := db.ActiveManualFiresales(t.Context())
			if err != nil || (len(sales) == 0) != want {
				t.Fatalf("sales=%+v err=%v", sales, err)
			}
			if want {
				if len(n.events) != 1 || n.events[0].Type != domain.EventFiresaleEnded || n.events[0].Data["reason"] != reason || n.events[0].Data["trigger"] != "manual" {
					t.Fatalf("events=%+v", n.events)
				}
			} else if len(n.events) != 0 {
				t.Fatalf("events=%+v", n.events)
			}
		})
	}
}
