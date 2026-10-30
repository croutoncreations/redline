package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/resetsignal"
)

func installResetSignalFixture(t *testing.T, s *Server, signal *resetsignal.Signal) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(signal)
	}))
	t.Cleanup(feed.Close)
	s.resetSignalFetcher = resetsignal.Fetcher{HTTP: feed.Client(), AllowHTTP: true, Now: s.now}
	s.resetSignalSources = []resetsignal.Source{{Name: "fixture", Endpoint: feed.URL, Normalize: func(body []byte, _ http.Header, _ time.Time) (resetsignal.Signal, error) {
		var result resetsignal.Signal
		err := json.Unmarshal(body, &result)
		return result, err
	}}}
	return &calls
}

func enableResetSignalNotifications(s *Server) {
	s.config.ResetSignals.Enabled = true
	s.config.Notifications = config.Notifications{Enabled: true, Events: []string{"reset.announced"}}
}

func TestResetSignalPollPersistenceEscalationAndRetraction(t *testing.T) {
	s, db, _ := redemptionServer(t, "codex")
	enableResetSignalNotifications(s)
	s.config.ActivePolicy = "standard"
	s.config.Policies = map[string]config.Policy{"standard": {}}
	n := &redemptionNotifier{}
	s.notifier = n
	pub := s.now().Add(-time.Hour)
	end := s.now().Add(time.Hour)
	signal := resetsignal.Signal{Kind: resetsignal.KindPromised, Freshness: resetsignal.Fresh, AnnouncementID: "123", ClaimedOriginalURL: "https://x.com/thsottiaux/status/123", OriginalAccount: "thsottiaux", PublishedAt: &pub, Window: resetsignal.Window{End: &end, Certainty: resetsignal.WindowExplicitPrimary}}
	installResetSignalFixture(t, s, &signal)
	poll := func() {
		t.Helper()
		if err := s.pollResetSignals(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	poll()
	if len(n.events) != 1 {
		t.Fatalf("repeat notifications=%+v", n.events)
	}
	restarted := newServer(s.config, db, s.now, nil, nil, n)
	installResetSignalFixture(t, restarted, &signal)
	if err := restarted.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if len(n.events) != 1 {
		t.Fatal("restart duplicated announcement")
	}
	signal.Kind = resetsignal.KindScheduled
	poll()
	signal.Kind = resetsignal.KindCompleted
	poll()
	poll()
	if len(n.events) != 3 {
		t.Fatalf("escalations=%+v", n.events)
	}
	signal.Corrected = true
	poll()
	poll()
	if len(n.events) != 4 || n.events[3].Data["retracted"] != "true" {
		t.Fatalf("retractions=%+v", n.events)
	}
	state := s.ResetSignalState()
	if state.Announcement != nil || state.LastPollAt == nil || state.Report == nil {
		t.Fatalf("state=%+v", state)
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1/v1/reset-signals", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("state API: %d %s", w.Code, w.Body.String())
	}
	var cached resetsignal.State
	if err := json.Unmarshal(w.Body.Bytes(), &cached); err != nil || cached.LastPollAt == nil {
		t.Fatalf("cached=%+v err=%v", cached, err)
	}
	dash, err := s.dashboardData(t.Context())
	if err != nil || dash.ResetSignal == nil {
		t.Fatalf("dashboard=%+v err=%v", dash.ResetSignal, err)
	}
}

func TestResetSignalOptInsAndPredictionNeverNotify(t *testing.T) {
	for _, mode := range []string{"prediction", "notify off", "notifications off", "event not opted in", "enabled"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _ := redemptionServer(t, "codex")
			enableResetSignalNotifications(s)
			n := &redemptionNotifier{}
			s.notifier = n
			pub := s.now().Add(-time.Hour)
			signal := resetsignal.Signal{Kind: resetsignal.KindPromised, Freshness: resetsignal.Fresh, PublishedAt: &pub, AnnouncementID: "123"}
			switch mode {
			case "prediction":
				signal.Kind = resetsignal.KindPrediction
				p := 100.
				signal.Prediction24Percent = &p
			case "notify off":
				no := false
				s.config.ResetSignals.Notify = &no
			case "notifications off":
				s.config.Notifications.Enabled = false
			case "event not opted in":
				s.config.Notifications.Events = nil
			}
			installResetSignalFixture(t, s, &signal)
			if err := s.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "enabled" {
				want = 1
			}
			if len(n.events) != want {
				t.Fatalf("events=%+v", n.events)
			}
		})
	}
}

func TestResetSignalLoopIndependentAndOffByDefault(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			s, db, _ := redemptionServer(t, "codex")
			cfg := s.config
			cfg.ResetSignals.Enabled = enabled
			s = newServer(cfg, db, s.now, nil, nil, nil)
			body, err := os.ReadFile("../resetsignal/testdata/willreset-promised.json")
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer fixture.Close()
			s.resetSignalSources = []resetsignal.Source{resetsignal.DefaultSources()[1]}
			s.resetSignalSources[0].Endpoint = fixture.URL
			s.resetSignalFetcher = resetsignal.Fetcher{AllowHTTP: true, HTTP: fixture.Client(), Now: func() time.Time { return time.Date(2026, 10, 13, 4, 16, 0, 0, time.UTC) }}
			s.resetSignalSleep = func(context.Context, time.Duration) error { return nil }
			ctx, cancel := context.WithCancel(context.Background())
			s.StartScheduler(ctx)
			if enabled {
				deadline := time.Now().Add(3 * time.Second)
				for s.ResetSignalState().LastPollAt == nil && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
			}
			cancel()
			s.Wait()
			want := int32(0)
			if enabled {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want=%d", calls.Load(), want)
			}
		})
	}
}

func TestResetSignalPollPreservesRetryAfterAcrossCycles(t *testing.T) {
	s, _, _ := redemptionServer(t, "codex")
	s.config.ResetSignals.Enabled = true
	var calls atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(429)
	}))
	defer fixture.Close()
	s.resetSignalSources = []resetsignal.Source{{Name: "codex-reset", Endpoint: fixture.URL}}
	s.resetSignalFetcher = resetsignal.Fetcher{HTTP: fixture.Client(), AllowHTTP: true, Now: s.now}
	for range 2 {
		if err := s.pollResetSignals(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 || len(s.ResetSignalState().Errors) == 0 {
		t.Fatalf("calls=%d state=%+v", calls.Load(), s.ResetSignalState())
	}
}

func TestResetSignalCandidateDoesNotChangeDecisionsOrFiresale(t *testing.T) {
	s, _, _ := redemptionServer(t, "codex")
	s.config.ActivePolicy = "standard"
	trigger := .3
	s.config.Policies = map[string]config.Policy{"standard": {FiresaleTrigger: &trigger}}
	baseline, _, err := s.dispatchCore(t.Context(), schedulerRequest{ProviderAccountID: "main"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	s.config.ResetSignals.Enabled = true
	pub, end := s.now().Add(-time.Hour), s.now().Add(time.Minute)
	signal := resetsignal.Signal{Kind: resetsignal.KindPromised, Freshness: resetsignal.Fresh, PublishedAt: &pub, AnnouncementID: "123", Window: resetsignal.Window{End: &end}}
	installResetSignalFixture(t, s, &signal)
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if s.ResetSignalCandidate() == nil {
		t.Fatal("missing candidate seam")
	}
	after, _, err := s.dispatchCore(t.Context(), schedulerRequest{ProviderAccountID: "main"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(baseline.Result, after.Result) || !reflect.DeepEqual(baseline.ProviderFiresale, after.ProviderFiresale) {
		t.Fatalf("decision changed: before=%+v after=%+v", baseline, after)
	}
}
