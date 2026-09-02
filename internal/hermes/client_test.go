package hermes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/hermes"
)

func TestLoadDesktopConnectionDiscoversRemoteGatewayWithoutCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connection.json")
	if err := os.WriteFile(path, []byte(`{
		"mode":"remote",
		"remote":{"url":"http://hermes.test:9119","authMode":"oauth"},
		"profiles":{}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	connection, err := hermes.LoadDesktopConnection(path)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Runtime != "hermes" || connection.Transport != "gateway" ||
		connection.CredentialSource != "hermes_desktop" || connection.URL != "http://hermes.test:9119" {
		t.Fatalf("connection = %#v", connection)
	}
}

func TestDiscoverReturnsProfilesProjectsAndOnlyAuthenticatedModels(t *testing.T) {
	server := fakeGateway(t)
	defer server.Close()
	client := hermes.Client{HTTPClient: authenticatedClient(server.URL)}
	discovery, err := client.Discover(t.Context(), domain.RuntimeConnection{
		ID: "test", Runtime: "hermes", Transport: "gateway", URL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Version != "0.17.0" || len(discovery.Profiles) != 1 ||
		len(discovery.ProfileOptions) != 1 || len(discovery.ProfileOptions[0].Projects) != 1 {
		t.Fatalf("discovery = %#v", discovery)
	}
	options := discovery.ProfileOptions[0]
	if projectByName(options, "redline").PrimaryPath != "/srv/redline" ||
		options.Provider != "openai-codex" || options.Model != "gpt-5.5" ||
		len(options.Providers[0].Models) != 2500 {
		t.Fatalf("options = %#v", options)
	}
}

func TestReadLoopDoesNotLeakWhenUnsolicitedEventsFillTheBuffer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"version": "0.17.0"})
	})
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"profiles": []map[string]any{{
			"name": "default", "path": "/home/test/.hermes", "is_default": true,
		}}})
	})
	mux.HandleFunc("POST /api/auth/ws-ticket", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ticket": "one-shot-ticket"})
	})
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		for {
			var request struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			if err := wsjson.Read(r.Context(), socket, &request); err != nil {
				return
			}
			if request.Method != "model.options" {
				writeRPC(t, r.Context(), socket, request.ID, map[string]any{"projects": []map[string]any{}})
				continue
			}
			// Flood past the gatewayClient's 64-entry event buffer with
			// unsolicited events from other sessions before ever answering,
			// simulating a busy Hermes Gateway. readLoop must not deadlock.
			for range 80 {
				writeRPCEvent(t, r.Context(), socket, map[string]any{
					"type": "noise", "session_id": "other-session",
				})
			}
			writeRPC(t, r.Context(), socket, request.ID, map[string]any{"providers": []map[string]any{}})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := hermes.Client{HTTPClient: authenticatedClient(server.URL)}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := client.Discover(ctx, domain.RuntimeConnection{
		ID: "test", Runtime: "hermes", Transport: "gateway", URL: server.URL,
	}); err == nil {
		t.Fatal("expected Discover to report the stalled model.options RPC")
	}

	// The blocked event send should be released once Discover calls
	// gateway.close(), not linger forever. Poll the goroutine dump for the
	// specific readLoop stack frame rather than comparing raw goroutine
	// counts, which are noisy due to unrelated HTTP keep-alive goroutines.
	const stackMarker = "gatewayClient).readLoop"
	deadline := time.Now().Add(3 * time.Second)
	for {
		var buf bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&buf, 1); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), stackMarker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("readLoop goroutine leaked after Discover returned:\n%s", buf.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestListAndTriggerJobsUseAuthenticatedGatewayAPI(t *testing.T) {
	var triggered string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs":
			writeJSON(w, map[string]any{"jobs": []map[string]any{{
				"id": "content-post", "name": "Draft content post", "state": "scheduled",
				"enabled": true, "provider": "anthropic-cli",
			}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/jobs/content-post/run":
			triggered = r.URL.Path
			writeJSON(w, map[string]any{"job": map[string]any{
				"id": "content-post", "name": "Draft content post", "state": "scheduled",
				"enabled": true,
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	client := hermes.Client{HTTPClient: func(_ context.Context, _ domain.RuntimeConnection) (*http.Client, string, error) {
		return &http.Client{Transport: bearerTransport{
			token: "test-key", base: http.DefaultTransport,
		}}, gateway.URL, nil
	}}
	connection := domain.RuntimeConnection{ID: "remote", Runtime: "hermes", Transport: "gateway"}

	jobs, err := client.ListJobs(t.Context(), connection)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "content-post" || jobs[0].Provider != "anthropic-cli" {
		t.Fatalf("jobs = %#v", jobs)
	}
	job, err := client.TriggerJob(t.Context(), connection, jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != jobs[0].ID || triggered != "/api/jobs/content-post/run" {
		t.Fatalf("job=%#v triggered=%q", job, triggered)
	}
}

func TestListAndTriggerJobsFallBackToDesktopCronAPI(t *testing.T) {
	var requested []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/api/jobs" || r.URL.Path == "/api/jobs/content-post/run":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/cron/jobs":
			writeJSON(w, []map[string]any{{
				"id": "content-post", "name": "Draft content post",
				"enabled": true, "provider": "anthropic-cli",
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/cron/jobs/content-post/trigger":
			writeJSON(w, map[string]any{
				"id": "content-post", "name": "Draft content post", "enabled": true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	client := hermes.Client{HTTPClient: func(_ context.Context, _ domain.RuntimeConnection) (*http.Client, string, error) {
		return gateway.Client(), gateway.URL, nil
	}}
	connection := domain.RuntimeConnection{ID: "remote", Runtime: "hermes", Transport: "gateway"}

	jobs, err := client.ListJobs(t.Context(), connection)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "content-post" {
		t.Fatalf("jobs = %#v", jobs)
	}
	job, err := client.TriggerJob(t.Context(), connection, jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != jobs[0].ID {
		t.Fatalf("job = %#v", job)
	}
	want := []string{
		"GET /api/jobs",
		"GET /api/cron/jobs",
		"POST /api/jobs/content-post/run",
		"POST /api/cron/jobs/content-post/trigger",
	}
	if !reflect.DeepEqual(requested, want) {
		t.Fatalf("requests = %#v, want %#v", requested, want)
	}
}

func TestTriggerJobFallsBackToDesktopCronAPIWhenGatewayRouteRejectsMethod(t *testing.T) {
	var requested []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/jobs/content-post/run":
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case r.Method == http.MethodPost && r.URL.Path == "/api/cron/jobs/content-post/trigger":
			writeJSON(w, map[string]any{
				"id": "content-post", "name": "Draft content post", "enabled": true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	client := hermes.Client{HTTPClient: func(_ context.Context, _ domain.RuntimeConnection) (*http.Client, string, error) {
		return gateway.Client(), gateway.URL, nil
	}}

	job, err := client.TriggerJob(t.Context(), domain.RuntimeConnection{
		ID: "remote", Runtime: "hermes", Transport: "gateway",
	}, "content-post")
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "content-post" {
		t.Fatalf("job = %#v", job)
	}
	want := []string{
		"POST /api/jobs/content-post/run",
		"POST /api/cron/jobs/content-post/trigger",
	}
	if !reflect.DeepEqual(requested, want) {
		t.Fatalf("requests = %#v, want %#v", requested, want)
	}
}

func TestListJobsFallsBackToDesktopCronAPIWhenGatewayRouteRejectsMethod(t *testing.T) {
	var requested []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs":
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case r.Method == http.MethodGet && r.URL.Path == "/api/cron/jobs":
			writeJSON(w, []map[string]any{{
				"id": "content-post", "name": "Draft content post",
				"enabled": true, "provider": "anthropic-cli",
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	client := hermes.Client{HTTPClient: func(_ context.Context, _ domain.RuntimeConnection) (*http.Client, string, error) {
		return gateway.Client(), gateway.URL, nil
	}}

	jobs, err := client.ListJobs(t.Context(), domain.RuntimeConnection{
		ID: "remote", Runtime: "hermes", Transport: "gateway",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "content-post" {
		t.Fatalf("jobs = %#v", jobs)
	}
	want := []string{
		"GET /api/jobs",
		"GET /api/cron/jobs",
	}
	if !reflect.DeepEqual(requested, want) {
		t.Fatalf("requests = %#v, want %#v", requested, want)
	}
}

func TestRunJobFallsBackToDesktopCronRunsWhenGatewayRouteRejectsMethod(t *testing.T) {
	var cronRunReads int
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs/content-post/runs":
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case r.Method == http.MethodGet && r.URL.Path == "/api/cron/jobs/content-post/runs":
			cronRunReads++
			runs := []map[string]any{}
			if cronRunReads >= 2 {
				runs = append(runs, map[string]any{
					"id": "cron_content-post_new", "started_at": 200.0, "ended_at": 220.0,
					"end_reason": "cron_complete", "profile": "default",
				})
			}
			writeJSON(w, map[string]any{"runs": runs})
		case r.Method == http.MethodPost && r.URL.Path == "/api/jobs/content-post/run":
			writeJSON(w, map[string]any{"job": map[string]any{
				"id": "content-post", "last_status": "success",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs":
			writeJSON(w, map[string]any{"jobs": []map[string]any{{
				"id": "content-post", "last_status": "success",
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions/cron_content-post_new/messages":
			writeJSON(w, map[string]any{"messages": []map[string]any{{
				"role": "assistant", "content": "Finished the content plan.",
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	client := hermes.Client{
		HTTPClient: func(_ context.Context, _ domain.RuntimeConnection) (*http.Client, string, error) {
			return gateway.Client(), gateway.URL, nil
		},
		PollInterval: time.Millisecond,
	}

	result, err := client.RunJob(t.Context(), hermes.JobRunRequest{
		Connection: domain.RuntimeConnection{ID: "remote", Runtime: "hermes", Transport: "gateway"},
		JobID:      "content-post",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.ID != "cron_content-post_new" || result.Output != "Finished the content plan." || cronRunReads < 2 {
		t.Fatalf("result=%#v cronRunReads=%d", result, cronRunReads)
	}
}

func TestRunJobWaitsForNewHermesSessionAndCollectsResult(t *testing.T) {
	var runReads int
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs/content-post/runs":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/cron/jobs/content-post/runs":
			runReads++
			runs := []map[string]any{{"id": "cron_content-post_old", "ended_at": 100.0}}
			if runReads >= 2 {
				run := map[string]any{
					"id": "cron_content-post_new", "started_at": 200.0,
					"model": "claude-fable-5-medium", "billing_provider": "custom:cliproxyapi-plus",
					"input_tokens": 120, "output_tokens": 8, "cache_read_tokens": 400,
				}
				if runReads >= 3 {
					run["ended_at"] = 220.0
					run["end_reason"] = "cron_complete"
					run["profile"] = "default"
				}
				runs = append([]map[string]any{run}, runs...)
			}
			writeJSON(w, map[string]any{"runs": runs})
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/cron/jobs":
			// Hermes removes completed one-shot jobs from the active job list.
			writeJSON(w, []map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/jobs/content-post/run":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/cron/jobs/content-post/trigger":
			writeJSON(w, map[string]any{
				"id": "content-post", "name": "Draft content post", "enabled": true,
				"provider": "custom:cliproxyapi-plus", "model": "claude-fable-5-medium",
				"last_run_at": "2026-07-28T10:02:20Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions/cron_content-post_new/messages":
			if r.URL.Query().Get("profile") != "default" {
				t.Fatalf("profile query = %q", r.URL.Query().Get("profile"))
			}
			writeJSON(w, map[string]any{"session_id": "cron_content-post_new", "messages": []map[string]any{
				{"role": "user", "content": "write"},
				{"role": "assistant", "content": "Finished the content plan."},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	client := hermes.Client{
		HTTPClient: func(_ context.Context, _ domain.RuntimeConnection) (*http.Client, string, error) {
			return gateway.Client(), gateway.URL, nil
		},
		PollInterval: time.Millisecond,
	}
	var external domain.ExternalRun
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	result, err := client.RunJob(ctx, hermes.JobRunRequest{
		Connection: domain.RuntimeConnection{ID: "remote", Runtime: "hermes", Transport: "gateway"},
		JobID:      "content-post",
		OnExternalStarted: func(value domain.ExternalRun) error {
			external = value
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.ID != "cron_content-post_new" || result.Run.EndReason != "cron_complete" ||
		result.Output != "Finished the content plan." || runReads < 3 {
		t.Fatalf("result=%#v runReads=%d", result, runReads)
	}
	if external.RuntimeConnectionID != "remote" || external.RunID != "content-post" ||
		external.SessionID != "cron_content-post_new" {
		t.Fatalf("external = %#v", external)
	}
}

func TestRunJobReturnsFailureForNonSuccessfulHermesSession(t *testing.T) {
	var runReads int
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs/broken/runs":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/cron/jobs/broken/runs":
			runReads++
			runs := []map[string]any{}
			if runReads >= 2 {
				runs = append(runs, map[string]any{
					"id": "cron_broken_new", "started_at": 200.0, "ended_at": 201.0,
					"end_reason": "cron_complete",
				})
			}
			writeJSON(w, map[string]any{"runs": runs})
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/cron/jobs":
			writeJSON(w, []map[string]any{{
				"id": "broken", "last_run_at": "2026-07-28T10:02:20Z",
				"last_status": "error", "last_error": "provider rejected request", "enabled": true,
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/jobs/broken/run":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/cron/jobs/broken/trigger":
			writeJSON(w, map[string]any{"id": "broken", "enabled": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	client := hermes.Client{
		HTTPClient: func(_ context.Context, _ domain.RuntimeConnection) (*http.Client, string, error) {
			return gateway.Client(), gateway.URL, nil
		},
		PollInterval: time.Millisecond,
	}
	_, err := client.RunJob(t.Context(), hermes.JobRunRequest{
		Connection: domain.RuntimeConnection{ID: "remote", Runtime: "hermes", Transport: "gateway"},
		JobID:      "broken",
	})
	if err == nil || !strings.Contains(err.Error(), "provider rejected request") {
		t.Fatalf("error = %v", err)
	}
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(cloned)
}

func TestDiscoveryViewIsCompactByDefaultAndBoundsFilteredModels(t *testing.T) {
	discovery := hermes.Discovery{
		Version:  "0.18.2",
		Profiles: []hermes.Profile{{Name: "default"}, {Name: "other"}},
		ProfileOptions: []hermes.ProfileOptions{{
			Profile: hermes.Profile{Name: "default"},
			Providers: []hermes.ModelProvider{
				{
					Slug: "anthropic", Name: "Anthropic", Authenticated: true,
					Models: []string{"haiku", "sonnet", "opus"},
					Capabilities: map[string]any{
						"haiku":  map[string]any{"reasoning": false},
						"sonnet": map[string]any{"reasoning": true},
						"opus":   map[string]any{"reasoning": true},
					},
				},
				{Slug: "openai-codex", Name: "OpenAI Codex", Authenticated: true, Models: []string{"gpt-5.5"}},
			},
		}, {
			Profile:   hermes.Profile{Name: "other"},
			Providers: []hermes.ModelProvider{{Slug: "anthropic", Models: []string{"other-model"}}},
		}},
	}

	compact := discovery.View(hermes.DiscoveryOptions{})
	provider := compact.ProfileOptions[0].Providers[0]
	if provider.ModelCount != 3 || len(provider.Models) != 0 || provider.Capabilities != nil ||
		!provider.ModelsTruncated || !compact.Truncated {
		t.Fatalf("compact provider = %#v", provider)
	}

	filtered := discovery.View(hermes.DiscoveryOptions{
		Profile: "default", Provider: "anthropic", IncludeModels: true, ModelOffset: 1, ModelLimit: 1,
	})
	if len(filtered.Profiles) != 1 || len(filtered.ProfileOptions) != 1 ||
		len(filtered.ProfileOptions[0].Providers) != 1 {
		t.Fatalf("filtered discovery = %#v", filtered)
	}
	provider = filtered.ProfileOptions[0].Providers[0]
	if len(provider.Models) != 1 || provider.Models[0] != "sonnet" ||
		provider.ModelCount != 3 || provider.ModelOffset != 1 || !provider.ModelsTruncated ||
		len(provider.Capabilities) != 1 || provider.Capabilities["sonnet"] == nil {
		t.Fatalf("filtered provider = %#v", provider)
	}

	complete := discovery.View(hermes.DiscoveryOptions{
		Profile: "default", Provider: "anthropic", IncludeModels: true, ModelLimit: 3,
	})
	if complete.Truncated || complete.ProfileOptions[0].Providers[0].ModelsTruncated {
		t.Fatalf("complete discovery unexpectedly truncated = %#v", complete)
	}
}

func TestRunPersistsExternalIdentityBeforeWaitingAndCollectsUsage(t *testing.T) {
	server := fakeGateway(t)
	defer server.Close()
	client := hermes.Client{HTTPClient: authenticatedClient(server.URL)}
	var external domain.ExternalRun
	result, err := client.Run(context.Background(), hermes.RunRequest{
		RunID: "run-123", Prompt: "Reply with exactly OK.",
		Connection: domain.RuntimeConnection{ID: "hermes-pi", Runtime: "hermes", Transport: "gateway", URL: server.URL},
		Context:    domain.AgentContext{Profile: "default", WorkingDirectory: "/srv/redline"},
		Model:      "gpt-5.5", Provider: "openai-codex",
		OnExternalStarted: func(value domain.ExternalRun) error {
			external = value
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "OK" || result.SessionID != "stored-session" ||
		external.RuntimeConnectionID != "hermes-pi" || external.SessionID != "stored-session" {
		t.Fatalf("result=%#v external=%#v", result, external)
	}
	if result.Usage["input"] != float64(8) || result.Usage["output"] != float64(1) {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestEnvironmentSessionTokenAuthenticatesHTTPAndWebSocket(t *testing.T) {
	const variable = "REDLINE_TEST_HERMES_CREDENTIAL"
	t.Setenv(variable, `{"session_token":"secret-token"}`)
	server := tokenGateway(t, "secret-token")
	defer server.Close()

	discovery, err := (hermes.Client{}).Discover(t.Context(), domain.RuntimeConnection{
		ID: "token", Runtime: "hermes", Transport: "gateway", URL: server.URL,
		CredentialSource: "environment", CredentialRef: variable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Version != "0.17.0" || len(discovery.Profiles) != 1 {
		t.Fatalf("discovery = %#v", discovery)
	}
}

func TestSessionTokenIsNotLeakedToARedirectTarget(t *testing.T) {
	const variable = "REDLINE_TEST_HERMES_REDIRECT"
	const secret = "do-not-leak-this-token"
	t.Setenv(variable, `{"session_token":"`+secret+`"}`)

	var attackerSawToken bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hermes-Session-Token") != "" {
			attackerSawToken = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/collect", http.StatusFound)
	}))
	defer gateway.Close()

	_, err := (hermes.Client{}).Discover(t.Context(), domain.RuntimeConnection{
		ID: "redirect", Runtime: "hermes", Transport: "gateway", URL: gateway.URL,
		CredentialSource: "environment", CredentialRef: variable,
	})
	if err == nil {
		t.Fatal("expected Discover to fail against a redirecting Gateway response")
	}
	if attackerSawToken {
		t.Fatal("session token was sent to the redirect target")
	}
}

func TestFilePasswordCredentialLogsInWithoutPersistingSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-credential.json")
	if err := os.WriteFile(path, []byte(`{
		"provider":"basic","username":"redline","password":"correct horse"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := passwordGateway(t, "redline", "correct horse")
	defer server.Close()

	discovery, err := (hermes.Client{}).Discover(t.Context(), domain.RuntimeConnection{
		ID: "password", Runtime: "hermes", Transport: "gateway", URL: server.URL,
		CredentialSource: "file", CredentialRef: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Version != "0.17.0" {
		t.Fatalf("discovery = %#v", discovery)
	}
}

func TestCredentialFileRejectsGroupReadableSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-credential.json")
	if err := os.WriteFile(path, []byte(`{"session_token":"secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (hermes.Client{}).Discover(t.Context(), domain.RuntimeConnection{
		ID: "file", Runtime: "hermes", Transport: "gateway", URL: "https://gateway.invalid",
		CredentialSource: "file", CredentialRef: path,
	})
	if err == nil || !strings.Contains(err.Error(), "must not allow group or other access") {
		t.Fatalf("error = %v", err)
	}
}

func TestWebSocketFailureDoesNotExposeSessionToken(t *testing.T) {
	const variable = "REDLINE_TEST_HERMES_REDACT"
	const secret = "do-not-log-this-token"
	t.Setenv(variable, `{"session_token":"`+secret+`"}`)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"version": "0.17.0"})
	})
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"profiles": []map[string]any{{"name": "default"}}})
	})
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	_, err := (hermes.Client{}).Discover(t.Context(), domain.RuntimeConnection{
		ID: "token", Runtime: "hermes", Transport: "gateway", URL: server.URL,
		CredentialSource: "environment", CredentialRef: variable,
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func authenticatedClient(baseURL string) hermes.HTTPClientFactory {
	return func(context.Context, domain.RuntimeConnection) (*http.Client, string, error) {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar}, baseURL, nil
	}
}

func fakeGateway(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"version": "0.17.0", "release_date": "2026.6.19",
			"auth_required": true, "auth_providers": []string{"basic"},
		})
	})
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"profiles": []map[string]any{{
			"name": "default", "path": "/home/test/.hermes", "is_default": true,
			"model": "gpt-5.5", "provider": "openai-codex",
		}}})
	})
	mux.HandleFunc("POST /api/auth/ws-ticket", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ticket": "one-shot-ticket"})
	})
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != "one-shot-ticket" {
			http.Error(w, "missing ticket", http.StatusUnauthorized)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		for {
			var request struct {
				ID     string         `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := wsjson.Read(r.Context(), socket, &request); err != nil {
				return
			}
			var result any
			switch request.Method {
			case "projects.list":
				result = map[string]any{"projects": []map[string]any{{
					"id": "project-1", "name": "redline", "primary_path": "/srv/redline",
				}}}
			case "model.options":
				models := make([]string, 2500)
				for index := range models {
					models[index] = "gpt-5.5-model-" + strings.Repeat("x", 8) + fmt.Sprint(index)
				}
				result = map[string]any{
					"model": "gpt-5.5", "provider": "openai-codex",
					"providers": []map[string]any{{
						"slug": "openai-codex", "name": "OpenAI Codex", "is_current": true,
						"authenticated": true, "models": models,
					}},
				}
			case "session.create":
				result = map[string]any{"session_id": "live-session", "stored_session_id": "stored-session"}
			case "prompt.submit":
				result = map[string]any{"status": "streaming"}
			case "session.usage":
				result = map[string]any{"calls": 1, "input": 8, "output": 1, "total": 9}
			case "session.status":
				result = map[string]any{"model": "gpt-5.5", "provider": "openai-codex"}
			default:
				result = map[string]any{}
			}
			writeRPC(t, r.Context(), socket, request.ID, result)
			if request.Method == "prompt.submit" {
				writeRPCEvent(t, r.Context(), socket, map[string]any{
					"type": "message.complete", "session_id": "live-session",
					"payload": map[string]any{"text": "OK"},
				})
			}
		}
	})
	return httptest.NewServer(mux)
}

func tokenGateway(t *testing.T, token string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hermes-Session-Token") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"version": "0.17.0"})
	})
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hermes-Session-Token") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"profiles": []map[string]any{{
			"name": "default", "path": "/srv/hermes", "is_default": true,
		}}})
	})
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		serveDiscoveryWebSocket(t, w, r)
	})
	return httptest.NewServer(mux)
}

func passwordGateway(t *testing.T, username, password string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	authenticated := func(r *http.Request) bool {
		cookie, err := r.Cookie("hermes_session_at")
		return err == nil && cookie.Value == "access-token"
	}
	mux.HandleFunc("POST /auth/password-login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Provider string `json:"provider"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Provider != "basic" ||
			body.Username != username || body.Password != password {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "hermes_session_at", Value: "access-token", Path: "/"})
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"version": "0.17.0"})
	})
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"profiles": []map[string]any{{
			"name": "default", "path": "/srv/hermes", "is_default": true,
		}}})
	})
	mux.HandleFunc("POST /api/auth/ws-ticket", func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"ticket": "password-ticket"})
	})
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != "password-ticket" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		serveDiscoveryWebSocket(t, w, r)
	})
	return httptest.NewServer(mux)
}

func serveDiscoveryWebSocket(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer socket.CloseNow()
	for {
		var request struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		}
		if err := wsjson.Read(r.Context(), socket, &request); err != nil {
			return
		}
		result := map[string]any{}
		if request.Method == "projects.list" {
			result = map[string]any{"projects": []any{}}
		}
		if request.Method == "model.options" {
			result = map[string]any{"providers": []any{}}
		}
		writeRPC(t, r.Context(), socket, request.ID, result)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeRPC(t *testing.T, ctx context.Context, socket *websocket.Conn, id string, result any) {
	t.Helper()
	if err := wsjson.Write(ctx, socket, map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil &&
		!strings.Contains(err.Error(), "closed") {
		t.Error(err)
	}
}

func writeRPCEvent(t *testing.T, ctx context.Context, socket *websocket.Conn, params any) {
	t.Helper()
	if err := wsjson.Write(ctx, socket, map[string]any{"jsonrpc": "2.0", "method": "event", "params": params}); err != nil {
		t.Error(err)
	}
}

func projectByName(p hermes.ProfileOptions, name string) hermes.Project {
	for _, project := range p.Projects {
		if project.Name == name {
			return project
		}
	}
	return hermes.Project{}
}
