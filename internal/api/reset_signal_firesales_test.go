package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/resetsignal"
	"github.com/croutoncreations/redline/internal/usage"
)

func firesaleFixture(t *testing.T, mode string) (*Server, *resetsignal.Signal, *redemptionNotifier) {
	t.Helper()
	s, _, _ := redemptionServer(t, "codex")
	s.config.ResetSignals.Enabled = true
	s.config.ResetSignals.Firesale = mode
	provider := s.config.Providers["main"]
	provider.WindowWeeklyCost = .1
	s.config.Providers["main"] = provider
	trigger := .9
	s.config.ActivePolicy = "standard"
	s.config.Policies = map[string]config.Policy{"standard": {PaceGapTrigger: &trigger}}
	n := &redemptionNotifier{}
	s.notifier = n
	pub, end := s.now().Add(-time.Hour), s.now().Add(time.Hour)
	signal := &resetsignal.Signal{Kind: resetsignal.KindPromised, Freshness: resetsignal.Fresh, AnnouncementID: "123", OriginalAccount: "thsottiaux", PublishedAt: &pub, Window: resetsignal.Window{End: &end, Certainty: resetsignal.WindowExplicitPrimary}}
	installResetSignalFixture(t, s, signal)
	return s, signal, n
}

func firesaleAction(t *testing.T, s *Server, key, action string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "http://127.0.0.1/v1/reset-signals/firesale", strings.NewReader(`{"announcement_key":"`+key+`","action":"`+action+`"}`))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func mustSnapshot(t *testing.T, s *Server) decision.UsageSnapshot {
	t.Helper()
	snapshot, _, err := s.store.LatestSnapshot(t.Context(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestResetSignalFiresaleAutoQualificationAndRestart(t *testing.T) {
	for _, tc := range []struct {
		name, mode, provider string
		kind                 resetsignal.Kind
		deadline, active     bool
	}{
		{"promised", "auto", "codex", resetsignal.KindPromised, true, true},
		{"scheduled", "auto", "codex", resetsignal.KindScheduled, true, true},
		{"no deadline", "auto", "codex", resetsignal.KindPromised, false, false},
		{"completed", "auto", "codex", resetsignal.KindCompleted, true, false},
		{"odds", "auto", "codex", resetsignal.KindPrediction, true, false},
		{"off", "off", "codex", resetsignal.KindPromised, true, false},
		{"not codex", "auto", "claude", resetsignal.KindPromised, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, signal, n := firesaleFixture(t, tc.mode)
			signal.Kind = tc.kind
			provider := s.config.Providers["main"]
			provider.Provider = tc.provider
			s.config.Providers["main"] = provider
			if !tc.deadline {
				signal.Window.End = nil
			}
			for range 2 {
				if err := s.pollResetSignals(t.Context(), ""); err != nil {
					t.Fatal(err)
				}
			}
			if got := s.ResetSignalState().Firesale.Active; got != tc.active {
				t.Fatalf("active=%v want=%v", got, tc.active)
			}
			want := 0
			if tc.active {
				want = 1
			}
			if len(n.events) != want {
				t.Fatalf("events=%+v", n.events)
			}
			restarted := newServer(s.config, s.store, s.now, nil, nil, n)
			installResetSignalFixture(t, restarted, signal)
			if restarted.ResetSignalState().Firesale.Active != tc.active {
				t.Fatal("restart lost firesale")
			}
			if err := restarted.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			if len(n.events) != want {
				t.Fatalf("restart events=%+v", n.events)
			}
		})
	}
}

func TestResetSignalFiresaleAutoOff(t *testing.T) {
	for _, reason := range []string{"deadline_passed", "retracted", "completed", "disabled", "stopped"} {
		t.Run(reason, func(t *testing.T) {
			s, signal, n := firesaleFixture(t, "auto")
			if err := s.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "deadline_passed":
				now := signal.Window.End.Add(time.Second)
				s.now = func() time.Time { return now }
			case "retracted":
				signal.Corrected = true
			case "completed":
				signal.Kind = resetsignal.KindCompleted
			case "disabled":
				s.config.ResetSignals.Enabled = false
			case "stopped":
				w := firesaleAction(t, s, "announcement:123", "stop")
				if w.Code != 200 {
					t.Fatalf("stop=%d %s", w.Code, w.Body.String())
				}
			}
			for range 2 {
				if err := s.pollResetSignals(t.Context(), ""); err != nil {
					t.Fatal(err)
				}
			}
			if s.ResetSignalState().Firesale.Active || len(n.events) != 2 || n.events[1].Type != domain.EventFiresaleEnded || n.events[1].Data["reason"] != reason {
				t.Fatalf("state=%+v events=%+v", s.ResetSignalState().Firesale, n.events)
			}
			restarted := newServer(s.config, s.store, s.now, nil, nil, n)
			installResetSignalFixture(t, restarted, signal)
			if err := restarted.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			if len(n.events) != 2 {
				t.Fatalf("restart duplicated events=%+v", n.events)
			}
			if reason == "completed" && n.events[1].Data["completion_source"] != "announcement" {
				t.Fatalf("completion=%+v", n.events[1])
			}
			if reason == "stopped" {
				w := firesaleAction(t, restarted, "announcement:123", "start")
				if w.Code != 200 || !restarted.ResetSignalState().Firesale.Active {
					t.Fatalf("explicit restart=%d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestResetSignalFiresaleRetainedFeedExpiresSafely(t *testing.T) {
	s, signal, n := firesaleFixture(t, "auto")
	now := s.now()
	s.now = func() time.Time { return now }
	expiry := now.Add(10 * time.Minute)
	signal.ExpiresAt = &expiry
	fail := false
	calls := 0
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if fail {
			w.Header().Set("Retry-After", "7200")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(signal)
	}))
	defer feed.Close()
	s.resetSignalSources[0].Endpoint = feed.URL
	s.resetSignalFetcher = resetsignal.Fetcher{HTTP: feed.Client(), AllowHTTP: true, Now: s.now}
	poll := func() {
		t.Helper()
		if err := s.pollResetSignals(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	fail = true
	poll()
	poll()
	if calls != 2 || s.ResetSignalState().Announcement == nil || !s.ResetSignalState().Firesale.Active || len(n.events) != 1 {
		t.Fatalf("calls=%d state=%+v events=%+v", calls, s.ResetSignalState(), n.events)
	}
	now = expiry.Add(time.Second)
	poll()
	if s.ResetSignalState().Announcement != nil || !s.ResetSignalState().Firesale.Active {
		t.Fatalf("snapshot expiry=%+v", s.ResetSignalState())
	}
	if w := firesaleAction(t, s, "announcement:123", "start"); w.Code != 409 {
		t.Fatalf("stale opt-in=%d", w.Code)
	}
	now = signal.Window.End.Add(time.Second)
	poll()
	poll()
	if s.ResetSignalState().Firesale.Active || len(n.events) != 2 || n.events[1].Data["reason"] != "deadline_passed" {
		t.Fatalf("deadline state=%+v events=%+v", s.ResetSignalState(), n.events)
	}
}

func TestResetSignalFiresaleDeadlineTracksSameAnnouncement(t *testing.T) {
	s, signal, n := firesaleFixture(t, "auto")
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	for _, delta := range []time.Duration{2 * time.Hour, 30 * time.Minute} {
		end := s.now().Add(delta)
		signal.Window.End = &end
		if err := s.pollResetSignals(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
		state := s.ResetSignalState().Firesale
		if state.Deadline == nil || !state.Deadline.Equal(end) || len(n.events) != 1 {
			t.Fatalf("state=%+v events=%+v", state, n.events)
		}
	}
}

func TestResetSignalFiresaleSchedulerDoesNotDuplicateLifecycleNotifications(t *testing.T) {
	s, signal, n := firesaleFixture(t, "auto")
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	runScheduler := func() {
		t.Helper()
		before, err := s.store.ListSchedulerDecisions(t.Context(), "main", 100)
		if err != nil {
			t.Fatal(err)
		}
		cfg := s.config
		cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
		restarted := newServer(cfg, s.store, s.now, nil, nil, n)
		restarted.usageSources = s.usageSources
		ctx, cancel := context.WithCancel(t.Context())
		defer func() { cancel(); restarted.Wait() }()
		restarted.StartScheduler(ctx)
		until := time.Now().Add(3 * time.Second)
		for {
			rows, err := s.store.ListSchedulerDecisions(t.Context(), "main", 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) > len(before) {
				break
			}
			if time.Now().After(until) {
				t.Fatal("scheduler did not evaluate")
			}
			time.Sleep(time.Millisecond)
		}
	}
	runScheduler()
	if len(n.events) != 1 {
		t.Fatalf("scheduler duplicated start=%+v", n.events)
	}
	w := firesaleAction(t, s, "announcement:123", "stop")
	if w.Code != 200 {
		t.Fatalf("stop=%d %s", w.Code, w.Body.String())
	}
	runScheduler()
	if len(n.events) != 2 {
		t.Fatalf("scheduler duplicated end=%+v signal=%+v", n.events, signal)
	}
}

func TestResetSignalFiresaleAPIAuthAndConflicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{
		{"bearer", 200}, {"cookie", 403}, {"demo", 409}, {"off", 409}, {"missing key", 409}, {"mismatched key", 409}, {"no announcement", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := firesaleFixture(t, "notify")
			if tc.name != "no announcement" {
				if err := s.pollResetSignals(t.Context(), ""); err != nil {
					t.Fatal(err)
				}
			}
			key := "announcement:123"
			switch tc.name {
			case "demo":
				s.redemptionDisabled = true
			case "off":
				s.config.ResetSignals.Firesale = "off"
			case "missing key":
				key = ""
			case "mismatched key":
				key = "announcement:other"
			}
			req := httptest.NewRequest("POST", "http://127.0.0.1/v1/reset-signals/firesale", strings.NewReader(`{"announcement_key":"`+key+`","action":"start"}`))
			if tc.name == "cookie" {
				req.AddCookie(&http.Cookie{Name: apiSessionCookie, Value: "secret"})
				req.Header.Set("Origin", "http://127.0.0.1")
			} else {
				req.Header.Set("Authorization", "Bearer secret")
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestResetSignalFiresaleSchedulerTierAndSafety(t *testing.T) {
	s, _, _ := firesaleFixture(t, "notify")
	snapshot := mustSnapshot(t, s)
	snapshot.Weekly.Remaining = .001
	setSnapshot := func() {
		source := staticUsageSource{snapshots: map[string]decision.UsageSnapshot{"main": snapshot}}
		s.usageSources = usage.NewManager(source, source, s.now)
	}
	setSnapshot()
	if err := s.store.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "profile", ProviderAccountID: "main", HarnessType: "command", WorkspaceProvider: "existing-directory"}, s.now()); err != nil {
		t.Fatal(err)
	}
	completed := s.now().Add(-time.Hour)
	if err := s.store.CreateTask(t.Context(), domain.Task{ID: "job", Name: "Job", ExecutionProfileID: "profile", Type: domain.Recurring, DispatchTier: domain.DispatchFiresale, MinInterval: 24 * time.Hour, FiresaleMinInterval: time.Minute, LastCompletedAt: &completed}, s.now()); err != nil {
		t.Fatal(err)
	}
	evaluate := func() schedulerResponse {
		t.Helper()
		req := httptest.NewRequest("POST", "http://127.0.0.1/v1/scheduler/evaluate", strings.NewReader(`{"provider_account_id":"main"}`))
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("evaluate=%d %s", w.Code, w.Body.String())
		}
		var response schedulerResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if got := evaluate(); got.SelectedTask != nil || got.Result.Firesale {
		t.Fatalf("notify forced before opt-in=%+v", got)
	}
	if w := firesaleAction(t, s, "announcement:123", "start"); w.Code != 200 {
		t.Fatalf("start=%d %s", w.Code, w.Body.String())
	}
	if got := evaluate(); got.SelectedTask == nil || !got.Result.Firesale || got.Result.UnlockedTier != domain.DispatchFiresale || got.Result.EffectiveResetSource != decision.ResetPredictedGlobal || got.Result.PaceGap > 0 {
		t.Fatalf("forced=%+v", got)
	}
	if err := s.store.SetProviderPaused(t.Context(), "main", true); err != nil {
		t.Fatal(err)
	}
	if got := evaluate(); got.SelectedTask != nil || got.Result.Mode != decision.ModePaused {
		t.Fatalf("pause bypassed=%+v", got)
	}
	if err := s.store.SetProviderPaused(t.Context(), "main", false); err != nil {
		t.Fatal(err)
	}
	snapshot.Short = &decision.UsageWindow{Remaining: 0, ResetsAt: s.now().Add(5 * time.Hour)}
	setSnapshot()
	if got := evaluate(); got.SelectedTask != nil || got.Result.Decision != decision.Wait || got.Result.Firesale {
		t.Fatalf("short limit bypassed=%+v", got)
	}
}

func TestResetSignalFiresaleDifferentAnnouncementWaitsAndStopUsesActiveKey(t *testing.T) {
	s, signal, n := firesaleFixture(t, "auto")
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	signal.AnnouncementID = "456"
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if w := firesaleAction(t, s, "announcement:456", "start"); w.Code != 409 || !strings.Contains(w.Body.String(), "announcement:123") || !strings.Contains(w.Body.String(), "stop") {
		t.Fatalf("different start=%d %s", w.Code, w.Body.String())
	}
	if w := firesaleAction(t, s, "announcement:456", "stop"); w.Code != 409 {
		t.Fatalf("mismatched stop=%d %s", w.Code, w.Body.String())
	}
	if w := firesaleAction(t, s, "announcement:123", "stop"); w.Code != 200 {
		t.Fatalf("old key stop=%d %s", w.Code, w.Body.String())
	}
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if state := s.ResetSignalState().Firesale; !state.Active || state.AnnouncementKey != "announcement:456" || len(n.events) != 3 {
		t.Fatalf("handoff=%+v events=%+v", state, n.events)
	}
}

func TestResetSignalFiresaleStopWithoutViableAnnouncement(t *testing.T) {
	s, signal, n := firesaleFixture(t, "auto")
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	signal.Kind = resetsignal.KindUnknown
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if s.ResetSignalState().Announcement != nil {
		t.Fatal("unexpected announcement")
	}
	if w := firesaleAction(t, s, "", "stop"); w.Code != 200 || s.ResetSignalState().Firesale.Active {
		t.Fatalf("outage stop=%d %s", w.Code, w.Body.String())
	}
	if len(n.events) != 2 || n.events[1].Data["reason"] != "stopped" {
		t.Fatal(n.events)
	}
	s.config.ResetSignals.Enabled = false
	if w := firesaleAction(t, s, "", "stop"); w.Code != 200 {
		t.Fatalf("empty stop=%d %s", w.Code, w.Body.String())
	}
}

func TestResetSignalFiresaleIgnoresOlderCompletion(t *testing.T) {
	s, signal, n := firesaleFixture(t, "auto")
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	signal.AnnouncementID = "previous"
	signal.Kind = resetsignal.KindCompleted
	older := signal.PublishedAt.Add(-time.Hour)
	signal.PublishedAt = &older
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if !s.ResetSignalState().Firesale.Active || len(n.events) != 1 {
		t.Fatalf("old completion ended sale=%+v", n.events)
	}
	newer := s.now()
	signal.PublishedAt = &newer
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if s.ResetSignalState().Firesale.Active || len(n.events) != 2 || n.events[1].Data["reason"] != "completed" {
		t.Fatalf("new completion=%+v", n.events)
	}
}

func TestResetSignalFiresaleDisableThenReenable(t *testing.T) {
	for _, mode := range []string{"notify", "auto"} {
		t.Run(mode, func(t *testing.T) {
			s, _, n := firesaleFixture(t, mode)
			if err := s.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			if mode == "notify" {
				if w := firesaleAction(t, s, "announcement:123", "start"); w.Code != 200 {
					t.Fatalf("start=%d %s", w.Code, w.Body.String())
				}
			}
			s.config.ResetSignals.Firesale = "off"
			if err := s.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			if s.ResetSignalState().Firesale.Active || len(n.events) != 2 || n.events[1].Data["reason"] != "disabled" {
				t.Fatalf("disabled=%+v events=%+v", s.ResetSignalState().Firesale, n.events)
			}
			s.config.ResetSignals.Firesale = mode
			if err := s.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			if got := s.ResetSignalState().Firesale.Active; got != (mode == "auto") {
				t.Fatalf("reenabled %s active=%v", mode, got)
			}
		})
	}
}

func TestResetSignalFiresaleAutoAnnouncementAndDashboardState(t *testing.T) {
	s, _, n := firesaleFixture(t, "auto")
	enableResetSignalNotifications(s)
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if len(n.events) != 2 || n.events[1].Type != domain.EventResetAnnounced || n.events[1].Data["firesale_action"] != "active" || n.events[1].Data["firesale_mode"] != "auto" {
		t.Fatalf("events=%+v", n.events)
	}
	dashboard, err := s.dashboardData(t.Context())
	if err != nil || dashboard.ResetSignal == nil || !dashboard.ResetSignal.Firesale.Active || dashboard.ResetSignal.Firesale.AnnouncementKey != "announcement:123" {
		t.Fatalf("dashboard=%+v err=%v", dashboard.ResetSignal, err)
	}
}

func TestResetSignalRetainsFailedFeedAndReplacesOnSuccess(t *testing.T) {
	s, signal, _ := firesaleFixture(t, "notify")
	fail := false
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if fail {
			_, _ = w.Write([]byte(`invalid JSON`))
			return
		}
		_ = json.NewEncoder(w).Encode(signal)
	}))
	defer feed.Close()
	s.resetSignalSources[0].Endpoint = feed.URL
	s.resetSignalFetcher = resetsignal.Fetcher{HTTP: feed.Client(), AllowHTTP: true, Now: s.now}
	poll := func() {
		t.Helper()
		if err := s.pollResetSignals(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	fail = true
	poll()
	if state := s.ResetSignalState(); state.Announcement == nil || len(state.Errors) == 0 {
		t.Fatalf("failed fetch lost last success=%+v", state)
	}
	fail = false
	signal.Kind = resetsignal.KindUnknown
	poll()
	if state := s.ResetSignalState(); state.Announcement != nil || len(state.Errors) != 0 {
		t.Fatalf("success did not replace=%+v", state)
	}
}

func TestResetSignalFiresaleUnlinkedCompletionOnlyEndsWhenNewerAndFresh(t *testing.T) {
	for _, tc := range []struct {
		name        string
		freshness   resetsignal.Freshness
		newer, ends bool
	}{
		{"newer", resetsignal.Fresh, true, true},
		{"older", resetsignal.Fresh, false, false},
		{"stale", resetsignal.Stale, true, false},
		{"missing TTL", resetsignal.MissingTTL, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, signal, n := firesaleFixture(t, "auto")
			enableResetSignalNotifications(s)
			if err := s.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			signal.Kind, signal.AnnouncementID = resetsignal.KindCompleted, ""
			signal.Freshness = tc.freshness
			pub := signal.PublishedAt.Add(-time.Minute)
			if tc.newer {
				pub = s.now()
			}
			signal.PublishedAt = &pub
			if err := s.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			if s.ResetSignalState().Firesale.Active == tc.ends {
				t.Fatalf("state=%+v events=%+v", s.ResetSignalState().Firesale, n.events)
			}
			wantEvents := 2
			if tc.ends {
				wantEvents++
			}
			if len(n.events) != wantEvents {
				t.Fatalf("events=%+v", n.events)
			}
			if tc.ends && (n.events[2].Data["reason"] != "completed" || n.events[2].Data["completion_source"] != "tracker_reset") {
				t.Fatalf("completion=%+v", n.events[2])
			}
		})
	}
}

// A tracker that marks the sale's own post completed keeps its publication
// time. The report's selected completion must still end the sale, even if a
// better-mirrored promise occupies the alert-ranked announcement.
func TestResetSignalFiresaleSameKeyCompletionEndsWithoutNewerTime(t *testing.T) {
	s, signal, n := firesaleFixture(t, "auto")
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	state := s.ResetSignalState()
	if !state.Firesale.Active || state.Report == nil {
		t.Fatalf("auto start=%+v", state.Firesale)
	}
	signal.Kind = resetsignal.KindCompleted
	sale, err := s.store.ActiveResetSignalFiresales(t.Context())
	if err != nil || len(sale) != 1 {
		t.Fatalf("sales=%d err=%v", len(sale), err)
	}
	completion := *signal
	got := firesaleCompletionSource(resetsignal.State{Report: state.Report}, sale[0])
	if got != "" {
		t.Fatalf("pre-completion source=%q", got)
	}
	report := resetsignal.BuildReport(s.now(), []resetsignal.SourceResult{{Signal: &completion}})
	if got := firesaleCompletionSource(resetsignal.State{Report: &report}, sale[0]); got != "announcement" {
		t.Fatalf("same-key completion with unchanged time: source=%q, want announcement (events=%d)", got, len(n.events))
	}
}

// An announcement the report already shows superseded by a newer completion
// must not start (auto or manual): the end check would end it at once.
func TestResetSignalFiresaleDoesNotStartAnAlreadyCompletedAnnouncement(t *testing.T) {
	s, signal, n := firesaleFixture(t, "auto")
	enableResetSignalNotifications(s)
	// Older promise for post 123, mirrored by two feeds; a newer linked
	// completion for a different post 456 from one feed.
	completedAt := s.now().Add(-time.Minute)
	completion := &resetsignal.Signal{Feed: "codex-reset", Kind: resetsignal.KindCompleted, Freshness: resetsignal.Fresh, AnnouncementID: "456", OriginalAccount: "thsottiaux", PublishedAt: &completedAt}
	promise := *signal
	promise.Feed = "resetbeacon"
	mirror := promise
	mirror.Feed = "willreset"
	sources := []resetsignal.SourceResult{{Signal: &promise}, {Signal: &mirror}, {Signal: completion}}
	report := resetsignal.BuildReport(s.now(), sources)
	if c := resetsignal.SelectedCompletion(report); c == nil || c.AnnouncementID != "456" {
		t.Fatalf("fixture: selected completion=%+v", c)
	}
	if a := resetsignal.ViableAnnouncement(report); a == nil || a.Kind != resetsignal.KindPromised || a.Key != "announcement:123" {
		t.Fatalf("fixture: alert-ranked announcement=%+v", a)
	}
	s.resetSignalMu.Lock()
	s.resetSignalState = resetsignal.State{Enabled: true, Report: &report}
	s.resetSignalMu.Unlock()
	if err := s.reconcileResetSignalFiresales(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sales, _ := s.store.ActiveResetSignalFiresales(t.Context()); len(sales) != 0 {
		t.Fatalf("auto started a sale the report shows completed (events=%+v)", n.events)
	}
	s.config.ResetSignals.Firesale = "notify"
	if w := firesaleAction(t, s, "announcement:123", "start"); w.Code != http.StatusConflict {
		t.Fatalf("manual start of a completed announcement=%d %s", w.Code, w.Body.String())
	}
}

type failingFiresaleNotifier struct{ calls int }

func (n *failingFiresaleNotifier) Notify(context.Context, domain.NotificationEvent) error {
	n.calls++
	return errors.New("hook failed")
}

// A failing hook must not leave some accounts' sales running: every lifecycle
// change is persisted first, and delivery errors are reported afterwards.
func TestResetSignalFiresaleLifecycleSurvivesNotificationFailures(t *testing.T) {
	s, _, _ := firesaleFixture(t, "notify")
	s.config.Providers["second"] = s.config.Providers["main"]
	n := &failingFiresaleNotifier{}
	s.notifier = n
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	firesaleAction(t, s, "announcement:123", "start")
	if got := s.ResetSignalState().Firesale.Providers; len(got) != 2 {
		t.Fatalf("start with failing hook reached %v, want both accounts", got)
	}
	firesaleAction(t, s, "", "stop")
	sales, err := s.store.ActiveResetSignalFiresales(t.Context())
	if err != nil || len(sales) != 0 {
		t.Fatalf("stop with failing hook left %d sales active (err=%v)", len(sales), err)
	}
	if n.calls != 4 {
		t.Fatalf("notification attempts=%d, want start and stop for both accounts", n.calls)
	}
}

// A delivery failure while reconciling ahead of a stop must not skip the stop:
// in auto mode that reconcile can itself start a sale the user means to stop.
func TestResetSignalFiresaleStopRunsDespiteReconcileDeliveryFailure(t *testing.T) {
	s, _, _ := firesaleFixture(t, "auto")
	n := &failingFiresaleNotifier{}
	s.notifier = n
	_ = s.pollResetSignals(t.Context(), "")
	sales, err := s.store.ActiveResetSignalFiresales(t.Context())
	if err != nil || len(sales) != 1 {
		t.Fatalf("auto start=%d err=%v", len(sales), err)
	}
	if _, err := s.store.EndResetSignalFiresale(t.Context(), sales[0].ID, "disabled", s.now()); err != nil {
		t.Fatal(err)
	}
	// The stop's own reconcile restarts the auto sale, and that alert fails.
	w := firesaleAction(t, s, "", "stop")
	if w.Code == http.StatusOK {
		t.Fatalf("delivery failure hidden: %d %s", w.Code, w.Body.String())
	}
	if sales, err := s.store.ActiveResetSignalFiresales(t.Context()); err != nil || len(sales) != 0 {
		t.Fatalf("stop skipped after reconcile delivery failure: %d active, err=%v", len(sales), err)
	}
}

func TestResetSignalFiresaleNotifyStartStop(t *testing.T) {
	s, signal, n := firesaleFixture(t, "notify")
	enableResetSignalNotifications(s)
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if len(n.events) != 1 || n.events[0].Data["firesale_action"] != "start" || n.events[0].Data["firesale_mode"] != "notify" {
		t.Fatalf("announcement=%+v", n.events)
	}
	assertActive := func(want bool) {
		t.Helper()
		req := httptest.NewRequest("GET", "http://127.0.0.1/v1/reset-signals", nil)
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		var state struct {
			Firesale struct {
				Active    bool     `json:"active"`
				Mode      string   `json:"mode"`
				Providers []string `json:"providers"`
			} `json:"firesale"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state.Firesale.Active != want || state.Firesale.Mode != "notify" || (want && len(state.Firesale.Providers) != 1) {
			t.Fatalf("state=%s", w.Body.String())
		}
	}
	assertActive(false)
	for range 2 {
		w := firesaleAction(t, s, "announcement:123", "start")
		if w.Code != 200 {
			t.Fatalf("start=%d %s", w.Code, w.Body.String())
		}
	}
	assertActive(true)
	events, err := s.providerResetEvents(t.Context(), "main", mustSnapshot(t, s))
	if err != nil || len(events) != 2 || events[1].Confidence != "announced" || !events[1].At.Equal(*signal.Window.End) {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	input, err := s.providerDecisionInput(t.Context(), "main", mustSnapshot(t, s), providerPolicySelection{Definition: s.config.Policies["standard"]})
	if err != nil || !input.ForceFiresale {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	for range 2 {
		w := firesaleAction(t, s, "announcement:123", "stop")
		if w.Code != 200 {
			t.Fatalf("stop=%d %s", w.Code, w.Body.String())
		}
	}
	assertActive(false)
	if len(n.events) != 3 || n.events[1].Type != domain.EventFiresaleStarted || n.events[2].Type != domain.EventFiresaleEnded || n.events[2].Data["reason"] != "stopped" || n.events[1].Data["trigger"] != "reset_announcement" {
		t.Fatalf("events=%+v", n.events)
	}
}
