package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jfox/redline/internal/api"
	"github.com/jfox/redline/internal/artifacts"
	"github.com/jfox/redline/internal/calibration"
	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/discovery"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/launchmetrics"
	"github.com/jfox/redline/internal/scheduler"
	"github.com/jfox/redline/internal/store"
	"github.com/jfox/redline/internal/workspace"
)

var apiNow = time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)

func TestLaunchMetricsReportsAutomaticWaitRate(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	for _, attempt := range []domain.DispatchAttempt{
		{ProviderAccountID: "codex-main", Trigger: "automatic", Outcome: domain.DispatchWait,
			Decision: "WAIT", Mode: "pace_threshold", StartedAt: apiNow.Add(-time.Hour), CompletedAt: apiNow.Add(-time.Hour + time.Second)},
		{ProviderAccountID: "codex-main", Trigger: "automatic", Outcome: domain.DispatchNoTask,
			Decision: "RUN", Mode: "pace_threshold", StartedAt: apiNow.Add(-30 * time.Minute), CompletedAt: apiNow.Add(-30*time.Minute + time.Second)},
		{ProviderAccountID: "codex-main", Trigger: "manual", Outcome: domain.DispatchWait,
			Decision: "WAIT", StartedAt: apiNow.Add(-time.Minute), CompletedAt: apiNow},
	} {
		if _, err := db.RecordDispatchAttempt(context.Background(), attempt); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := http.Get(server.URL + "/v1/metrics/launch?provider=codex-main&days=7")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var report launchmetrics.Report
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || report.Decisions.AutomaticChecks != 2 || report.Decisions.WaitRate != .5 {
		t.Fatalf("status=%d report=%#v", resp.StatusCode, report)
	}
	if len(report.Providers) != 1 || report.Providers[0].Allowance.Status != launchmetrics.StatusUnavailable {
		t.Fatalf("providers = %#v", report.Providers)
	}
}

func TestDashboardPageAndAssetsAreServed(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: "gas-gauge"},
		{path: "/", contentType: "text/html", contains: "+ New job"},
		{path: "/", contentType: "text/html", contains: "Run when"},
		{path: "/", contentType: "text/html", contains: "Start from"},
		{path: "/", contentType: "text/html", contains: "EXECUTION PROFILES"},
		{path: "/", contentType: "text/html", contains: "Custom command"},
		{path: "/", contentType: "text/html", contains: "Recently used repositories"},
		{path: "/", contentType: "text/html", contains: "Allowance routing override"},
		{path: "/", contentType: "text/html", contains: `id="failure-alert"`},
		{path: "/", contentType: "text/html", contains: `href="https://www.croutoncreations.com/?utm_source=redline&amp;utm_medium=product&amp;utm_campaign=redline"`},
		{path: "/", contentType: "text/html", contains: `href="https://buttondown.com/croutoncreations?utm_source=redline&amp;utm_medium=product&amp;utm_campaign=redline"`},
		{path: "/assets/dashboard.css", contentType: "text/css", contains: ":root"},
		{path: "/assets/dashboard.js", contentType: "text/javascript", contains: "Recent errors"},
		{path: "/assets/dashboard.js", contentType: "text/javascript", contains: "Resume & retry"},
		{path: "/assets/dashboard.js", contentType: "text/javascript", contains: "method:id ? 'PATCH' : 'POST'"},
		{path: "/assets/dashboard.js", contentType: "text/javascript", contains: "/v1/profile-options"},
		{path: "/assets/dashboard.js", contentType: "text/javascript", contains: "include_models:true"},
		{path: "/assets/dashboard.js", contentType: "text/javascript", contains: "/concurrency"},
		{path: "/", contentType: "text/html", contains: "profile-context-concurrency"},
		{path: "/assets/claude.svg", contentType: "image/svg+xml", contains: "<title>Claude</title>"},
		{path: "/assets/codex.svg", contentType: "image/svg+xml", contains: "<title>Codex</title>"},
	} {
		resp, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), test.contentType) || !strings.Contains(body.String(), test.contains) {
			t.Fatalf("GET %s: status=%d content-type=%q body=%q", test.path, resp.StatusCode, resp.Header.Get("Content-Type"), body.String())
		}
	}
	resp, err := http.Get(server.URL + "/assets/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset status = %d", resp.StatusCode)
	}
}

func TestMobileDashboardPageAndAssetsAreServed(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/m", contentType: "text/html", contains: "m-header"},
		{path: "/m", contentType: "text/html", contains: "m-tabs"},
		{path: "/m", contentType: "text/html", contains: "manifest.webmanifest"},
		{path: "/m", contentType: "text/html", contains: "mobile.css"},
		{path: "/m", contentType: "text/html", contains: "mobile.js"},
		{path: "/pair", contentType: "text/html", contains: "Pair this browser"},
		{path: "/assets/mobile/mobile.css", contentType: "text/css", contains: "--bg"},
		{path: "/assets/mobile/mobile.css", contentType: "text/css", contains: "m-header"},
		{path: "/assets/mobile/mobile.js", contentType: "text/javascript", contains: "connectSSE"},
		{path: "/assets/mobile/mobile.js", contentType: "text/javascript", contains: "/v1/dashboard"},
		{path: "/assets/mobile/mobile.js", contentType: "text/javascript", contains: "serviceWorker"},
		{path: "/assets/mobile/pair.js", contentType: "text/javascript", contains: "/v1/pairing/redeem"},
		{path: "/assets/mobile/manifest.webmanifest", contentType: "application/manifest+json", contains: "maskable"},
		{path: "/assets/mobile/manifest.webmanifest", contentType: "application/manifest+json", contains: "icon-192.png"},
		{path: "/sw.js", contentType: "text/javascript", contains: "redline-mobile-v1"},
		{path: "/sw.js", contentType: "text/javascript", contains: "/v1/"},
		{path: "/assets/mobile/icon-192.png", contentType: "image/png", contains: "\x89PNG"},
		{path: "/assets/mobile/icon-512.png", contentType: "image/png", contains: "\x89PNG"},
	} {
		resp, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), test.contentType) || !strings.Contains(body.String(), test.contains) {
			t.Fatalf("GET %s: status=%d content-type=%q body contains %q not found in: %.200s",
				test.path, resp.StatusCode, resp.Header.Get("Content-Type"), test.contains, body.String())
		}
	}
	resp, err := http.Get(server.URL + "/assets/mobile/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing mobile asset status = %d", resp.StatusCode)
	}
}

func TestServerRejectsDNSRebindingAndCrossOriginRequests(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	for _, test := range []struct {
		name   string
		host   string
		origin string
	}{
		{name: "non loopback host", host: "redline.attacker.test"},
		{name: "cross origin", origin: "https://attacker.test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/providers/codex-main/pause", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if test.host != "" {
				req.Host = test.host
			}
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
			}
		})
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Frame-Options") != "DENY" ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("status=%d headers=%v", resp.StatusCode, resp.Header)
	}
}

func TestServerAllowsConfiguredTrustedHostWithSameOriginAndAuthentication(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://unused")
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net"}
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()

	for _, test := range []struct {
		name       string
		host       string
		origin     string
		token      string
		wantStatus int
	}{
		{name: "trusted authenticated host", host: "macbook.example.ts.net", origin: "https://macbook.example.ts.net", token: cfg.APIToken, wantStatus: http.StatusOK},
		{name: "trusted host is case insensitive", host: "MACBOOK.EXAMPLE.TS.NET", origin: "https://macbook.example.ts.net", token: cfg.APIToken, wantStatus: http.StatusOK},
		{name: "trusted host still requires authentication", host: "macbook.example.ts.net", origin: "https://macbook.example.ts.net", wantStatus: http.StatusUnauthorized},
		{name: "trusted host rejects another origin", host: "macbook.example.ts.net", origin: "https://attacker.test", token: cfg.APIToken, wantStatus: http.StatusForbidden},
		{name: "trusted HTTPS host rejects HTTP origin", host: "macbook.example.ts.net", origin: "http://macbook.example.ts.net", token: cfg.APIToken, wantStatus: http.StatusForbidden},
		{name: "untrusted host stays forbidden", host: "other.example.ts.net", token: cfg.APIToken, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, requestErr := http.NewRequest(http.MethodGet, server.URL+"/v1/health", nil)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			req.Host = test.host
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if strings.EqualFold(test.host, "macbook.example.ts.net") {
				req.Header.Set("X-Forwarded-Proto", "https")
			}
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			resp, requestErr := http.DefaultClient.Do(req)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			defer resp.Body.Close()
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, test.wantStatus)
			}
		})
	}
}

func TestServerRejectsProxyThatRewritesRemoteHostToLoopback(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://unused")
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net"}
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://localhost/v1/health", nil)
	request.Header.Set("Authorization", "Bearer "+cfg.APIToken)
	request.Header.Set("X-Forwarded-Proto", "https")
	request.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("rewritten proxy host status=%d want=%d", recorder.Code, http.StatusForbidden)
	}
}

func TestTrustedHostRejectsMobileBootstrapWithoutExternalHTTPS(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://unused")
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net"}
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://macbook.example.ts.net/m?access_token="+url.QueryEscape(cfg.APIToken), nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.RemoteAddr = "100.101.102.103:54321"
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestTrustedHostBootstrapsSecureMobileDashboardSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://unused")
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net"}
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	currentTime := apiNow
	handler := api.NewServer(cfg, db, func() time.Time { return currentTime })
	pairing := httptest.NewRecorder()
	pairingRequest := httptest.NewRequest(http.MethodPost, "http://macbook.example.ts.net/v1/pairing", nil)
	pairingRequest.Header.Set("Authorization", "Bearer "+cfg.APIToken)
	pairingRequest.Header.Set("X-Forwarded-Proto", "https")
	pairingRequest.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(pairing, pairingRequest)
	if pairing.Code != http.StatusCreated {
		t.Fatalf("pairing status=%d body=%s", pairing.Code, pairing.Body.String())
	}
	var pairingResponse struct {
		Token     string    `json:"pairing_token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(pairing.Body).Decode(&pairingResponse); err != nil {
		t.Fatal(err)
	}
	if len(pairingResponse.Token) < 32 || !pairingResponse.ExpiresAt.After(apiNow) || pairingResponse.Token == cfg.APIToken {
		t.Fatalf("pairing response=%#v", pairingResponse)
	}

	pairPage := httptest.NewRecorder()
	pairPageRequest := httptest.NewRequest(http.MethodGet, "http://macbook.example.ts.net/pair", nil)
	pairPageRequest.Header.Set("X-Forwarded-Proto", "https")
	pairPageRequest.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(pairPage, pairPageRequest)
	if pairPage.Code != http.StatusOK || !strings.Contains(pairPage.Body.String(), "Pair this browser") {
		t.Fatalf("pair page status=%d body=%s", pairPage.Code, pairPage.Body.String())
	}

	redeemBody, err := json.Marshal(map[string]string{"pairing_token": pairingResponse.Token})
	if err != nil {
		t.Fatal(err)
	}
	redeem := httptest.NewRecorder()
	redeemRequest := httptest.NewRequest(http.MethodPost, "http://macbook.example.ts.net/v1/pairing/redeem", bytes.NewReader(redeemBody))
	redeemRequest.Header.Set("Content-Type", "application/json")
	redeemRequest.Header.Set("Origin", "https://macbook.example.ts.net")
	redeemRequest.Header.Set("X-Forwarded-Proto", "https")
	redeemRequest.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(redeem, redeemRequest)
	cookies := redeem.Result().Cookies()
	if redeem.Code != http.StatusNoContent || len(cookies) != 1 || cookies[0].Name != "redline_api_session" ||
		!cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("redeem status=%d cookies=%#v body=%s", redeem.Code, cookies, redeem.Body.String())
	}

	page := httptest.NewRecorder()
	pageRequest := httptest.NewRequest(http.MethodGet, "http://macbook.example.ts.net/m", nil)
	pageRequest.Header.Set("X-Forwarded-Proto", "https")
	pageRequest.RemoteAddr = "127.0.0.1:54321"
	pageRequest.AddCookie(cookies[0])
	handler.ServeHTTP(page, pageRequest)
	if page.Code != http.StatusOK || !strings.Contains(page.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("mobile page status=%d content-type=%q", page.Code, page.Header().Get("Content-Type"))
	}

	reused := httptest.NewRecorder()
	reusedRequest := httptest.NewRequest(http.MethodPost, "http://macbook.example.ts.net/v1/pairing/redeem", bytes.NewReader(redeemBody))
	reusedRequest.Header.Set("Content-Type", "application/json")
	reusedRequest.Header.Set("Origin", "https://macbook.example.ts.net")
	reusedRequest.Header.Set("X-Forwarded-Proto", "https")
	reusedRequest.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(reused, reusedRequest)
	if reused.Code != http.StatusUnauthorized {
		t.Fatalf("reused pairing token status=%d want=%d", reused.Code, http.StatusUnauthorized)
	}

	fullToken := httptest.NewRecorder()
	fullTokenRequest := httptest.NewRequest(http.MethodGet, "http://macbook.example.ts.net/m?access_token="+url.QueryEscape(cfg.APIToken), nil)
	fullTokenRequest.Header.Set("X-Forwarded-Proto", "https")
	fullTokenRequest.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(fullToken, fullTokenRequest)
	if fullToken.Code != http.StatusBadRequest {
		t.Fatalf("remote full API token bootstrap status=%d want=%d", fullToken.Code, http.StatusBadRequest)
	}
}

func TestServerRequiresBearerOrDashboardSessionWhenTokenConfigured(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://unused")
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer status = %d", resp.StatusCode)
	}

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err = client.Get(server.URL + "/?access_token=" + url.QueryEscape(cfg.APIToken))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("bootstrap status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "redline_api_session" || !cookies[0].HttpOnly ||
		cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("bootstrap cookies = %#v", cookies)
	}
	req, err = http.NewRequest(http.MethodGet, server.URL+"/v1/dashboard", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookies[0])
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie status = %d", resp.StatusCode)
	}
}

func TestTaskTemplatesAreEditableStarters(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	resp, err := http.Get(server.URL + "/v1/task-templates")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var templates []struct {
		ID          string          `json:"id"`
		Prompt      string          `json:"prompt"`
		MinInterval time.Duration   `json:"min_interval"`
		Type        domain.TaskType `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&templates); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(templates) != 6 {
		t.Fatalf("status=%d templates=%#v", resp.StatusCode, templates)
	}
	if templates[0].ID != "bug-hunt" || templates[0].Prompt == "" ||
		templates[0].MinInterval != 3*24*time.Hour || templates[0].Type != domain.Recurring {
		t.Fatalf("first template = %#v", templates[0])
	}
}

func TestDashboardEventsStreamAnImmediateSnapshot(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/dashboard/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buffer := make([]byte, 8192)
	n, err := resp.Body.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	body := string(buffer[:n])
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "event: dashboard\n") || !strings.Contains(body, `"active_policy":"standard"`) {
		t.Fatalf("unexpected event: %q", body)
	}
}

func TestProfileOptionsExposeDiscoveredHarnessesAndCacheUntilRefresh(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	discoverer := &fakeHarnessDiscoverer{catalog: discovery.Catalog{GeneratedAt: apiNow, Harnesses: []discovery.Harness{{
		ID: "pi", Label: "Pi", Installed: true, Version: "0.80.10",
		Models: map[string][]discovery.Model{"codex": {{ID: "openai-codex/gpt-5.6-sol", Source: "pi_catalog"}}},
	}}}}
	server := httptest.NewServer(api.NewServerWithHarnessDiscoverer(testConfig("http://unused"), db, func() time.Time { return apiNow }, discoverer))
	defer server.Close()
	for _, path := range []string{"/v1/profile-options", "/v1/profile-options", "/v1/profile-options?refresh=true"} {
		resp, getErr := http.Get(server.URL + path)
		if getErr != nil {
			t.Fatal(getErr)
		}
		var catalog discovery.Catalog
		if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || catalog.Harnesses[0].ID != "pi" {
			t.Fatalf("status=%d catalog=%#v", resp.StatusCode, catalog)
		}
	}
	if discoverer.calls.Load() != 2 {
		t.Fatalf("discovery calls = %d", discoverer.calls.Load())
	}
}

func TestDashboardReadModelIsUsefulAndDoesNotExposePrompts(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	if err := db.SetProviderPaused(t.Context(), "codex-main", true); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSnapshot(t.Context(), decision.UsageSnapshot{
		Provider: "codex", ObservedAt: apiNow, Weekly: decision.UsageWindow{Remaining: .86, ResetsAt: apiNow.Add(4 * 24 * time.Hour)}, Source: "test",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "codex-devx", ProviderAccountID: "codex-main", HarnessType: "codex-cli", Model: "gpt-5",
		WorkspaceProvider: "devx",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: "quiet-check", Name: "Quiet check", Prompt: "TOP SECRET PROMPT", Priority: 90,
		ExecutionProfileID: "codex-devx", Type: domain.Recurring, MinInterval: 24 * time.Hour,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	decisionJSON := json.RawMessage(`{"result":{"decision":"WAIT","policy":"standard","mode":"pace_threshold","reason":"no pace threshold matched","pace_gap":0.08},"trigger":"automatic"}`)
	if _, err := db.RecordSchedulerDecision(t.Context(), domain.SchedulerDecision{
		ProviderAccountID: "codex-main", DecisionJSON: decisionJSON,
	}, apiNow); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body.String())
	}
	if strings.Contains(body.String(), "TOP SECRET PROMPT") {
		t.Fatalf("dashboard exposed task prompt: %s", body.String())
	}
	var got struct {
		ActivePolicy string `json:"active_policy"`
		Providers    []struct {
			ID             string                  `json:"id"`
			Snapshot       *decision.UsageSnapshot `json:"snapshot"`
			Error          string                  `json:"error"`
			Paused         bool                    `json:"paused"`
			LatestDecision *struct {
				PaceGap            float64    `json:"pace_gap"`
				ProjectedTriggerAt *time.Time `json:"projected_trigger_at"`
			} `json:"latest_decision"`
		} `json:"providers"`
		Tasks []struct {
			ID       string        `json:"id"`
			Provider string        `json:"provider_account_id"`
			Model    string        `json:"model"`
			Interval time.Duration `json:"min_interval"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ActivePolicy != "standard" || len(got.Providers) != 2 || len(got.Tasks) != 1 {
		t.Fatalf("dashboard = %#v", got)
	}
	if got.Providers[0].ID != "claude-main" || got.Providers[0].Snapshot != nil || got.Providers[0].Error == "" {
		t.Fatalf("missing-provider state = %#v", got.Providers[0])
	}
	if got.Providers[1].ID != "codex-main" || got.Providers[1].Snapshot == nil || got.Providers[1].Snapshot.Weekly.Remaining != .86 || !got.Providers[1].Paused {
		t.Fatalf("codex provider = %#v", got.Providers[1])
	}
	if got.Providers[1].LatestDecision == nil || got.Providers[1].LatestDecision.PaceGap != .08 {
		t.Fatalf("codex latest decision = %#v", got.Providers[1].LatestDecision)
	}
	if got.Providers[1].LatestDecision.ProjectedTriggerAt == nil ||
		!got.Providers[1].LatestDecision.ProjectedTriggerAt.Equal(apiNow.Add(24*time.Hour)) {
		t.Fatalf("codex projected trigger = %#v", got.Providers[1].LatestDecision.ProjectedTriggerAt)
	}
	if got.Tasks[0].ID != "quiet-check" || got.Tasks[0].Provider != "codex-main" || got.Tasks[0].Model != "gpt-5" || got.Tasks[0].Interval != 24*time.Hour {
		t.Fatalf("task projection = %#v", got.Tasks[0])
	}
}

func TestDashboardMarksStoredUsageOlderThanConfiguredMaximumAsStale(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	if err := db.SaveSnapshot(t.Context(), decision.UsageSnapshot{
		Provider:   "claude",
		ObservedAt: apiNow.Add(-time.Hour),
		Short: &decision.UsageWindow{
			Remaining: .75, ResetsAt: apiNow.Add(4 * time.Hour),
		},
		Weekly: decision.UsageWindow{
			Remaining: .40, ResetsAt: apiNow.Add(4 * 24 * time.Hour),
		},
		Source: "native",
	}, nil); err != nil {
		t.Fatal(err)
	}

	var dashboard struct {
		Providers []struct {
			ID            string                  `json:"id"`
			Snapshot      *decision.UsageSnapshot `json:"snapshot"`
			SnapshotStale bool                    `json:"snapshot_stale"`
			Error         string                  `json:"error"`
		} `json:"providers"`
	}
	getJSON(t, server.URL+"/v1/dashboard", &dashboard)
	claude := dashboard.Providers[0]
	if claude.ID != "claude-main" || claude.Snapshot == nil || !claude.SnapshotStale {
		t.Fatalf("claude provider = %#v", claude)
	}
	if !strings.Contains(claude.Error, "Usage data is stale") || !strings.Contains(claude.Error, "scheduling is paused") {
		t.Fatalf("stale error = %q", claude.Error)
	}
}

func TestDashboardReportsStoreFailures(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestRunDetailEndpointReturnsRunAndNotFound(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "run-profile", "provider_account_id": "codex-main", "harness_type": "command", "workspace_provider": "existing-directory",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "run-task", "name": "Run task", "execution_profile_id": "run-profile", "type": "one_off",
	})
	if _, err := db.AdmitTask(t.Context(), "run-detail", "run-task", "codex-main", "revision", apiNow); err != nil {
		t.Fatal(err)
	}
	var run domain.Run
	getJSON(t, server.URL+"/v1/runs/run-detail", &run)
	if run.ID != "run-detail" || run.TaskID != "run-task" {
		t.Fatalf("run = %#v", run)
	}
	requestStatus(t, http.MethodGet, server.URL+"/v1/runs/missing", "", http.StatusNotFound)
}

func TestRunActivityCanBeMarkedRead(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "activity-profile", "provider_account_id": "codex-main", "harness_type": "command", "workspace_provider": "existing-directory",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "activity-task", "name": "Activity task", "execution_profile_id": "activity-profile", "type": "one_off",
	})
	run, err := db.AdmitTask(t.Context(), "activity-run", "activity-task", "codex-main", "revision", apiNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRun(t.Context(), run.ID, domain.RunCompletion{
		State: domain.RunCompleted, ExitCode: 0, Summary: "Opened a PR.",
		Artifacts: []domain.RunArtifact{{Type: "pull_request", Label: "Pull request", URL: "https://github.com/acme/app/pull/42"}},
	}, apiNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		UnreadRuns int          `json:"unread_runs"`
		Runs       []domain.Run `json:"runs"`
	}
	getJSON(t, server.URL+"/v1/dashboard", &dashboard)
	if dashboard.UnreadRuns != 1 || len(dashboard.Runs) == 0 || dashboard.Runs[0].Summary != "Opened a PR." {
		t.Fatalf("dashboard = %#v", dashboard)
	}
	requestStatus(t, http.MethodPost, server.URL+"/v1/runs/"+run.ID+"/read", "", http.StatusOK)
	getJSON(t, server.URL+"/v1/dashboard", &dashboard)
	if dashboard.UnreadRuns != 0 || dashboard.Runs[0].ActivityReadAt == nil {
		t.Fatalf("read dashboard = %#v", dashboard)
	}
	requestStatus(t, http.MethodPost, server.URL+"/v1/runs/missing/read", "", http.StatusNotFound)
}

func TestCapacityEndpointCorrelatesStoredLogsAndSnapshots(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, claudePayload) }))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	shortReset, weeklyReset := apiNow.Add(5*time.Hour), apiNow.Add(5*24*time.Hour)
	for index, remaining := range []float64{1, .98, .95} {
		snapshot := decision.UsageSnapshot{Provider: "claude", ObservedAt: apiNow.Add(time.Duration(index) * time.Minute),
			Short:  &decision.UsageWindow{Remaining: remaining, ResetsAt: shortReset},
			Weekly: decision.UsageWindow{Remaining: .80 - float64(index)*.01, ResetsAt: weeklyReset}, Source: "test"}
		if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.SaveTokenObservations(t.Context(), []capacity.TokenObservation{
		{Provider: "claude", Source: "gatepost", SourceID: "s:1", ObservedAt: apiNow.Add(30 * time.Second), InputTokens: 1000},
		{Provider: "claude", Source: "gatepost", SourceID: "s:2", ObservedAt: apiNow.Add(90 * time.Second), InputTokens: 4000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewServer(testConfig(usage.URL), db, func() time.Time { return apiNow.Add(time.Hour) }))
	defer server.Close()
	resp, err := http.Get(server.URL + "/v1/providers/claude-main/capacity")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got capacity.EstimateResult
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Short == nil || math.Abs(got.Short.EstimatedTokens.Total-100_000) > .01 || got.Weekly == nil || math.Abs(got.Weekly.EstimatedTokens.Total-250_000) > .01 {
		t.Fatalf("capacity = %#v", got)
	}
	if got.Short.AttributionCoverage != 1 || len(got.Short.Sources) != 1 || got.Short.Sources[0].Key != "gatepost" ||
		got.RatioDerivedDifference == nil {
		t.Fatalf("capacity evidence = %#v", got)
	}
}

func TestTokenSyncIncludesExplicitPiSubscriptionProvider(t *testing.T) {
	directory := t.TempDir()
	viewerPath := filepath.Join(directory, "viewer.db")
	piPath := filepath.Join(directory, "pi.jsonl")
	if err := os.WriteFile(piPath, []byte(`{"type":"message","id":"m1","timestamp":"2026-07-16T18:00:01Z","message":{"role":"assistant","provider":"anthropic-cli","model":"claude-opus","usage":{"input":10,"output":2,"cacheRead":30,"cacheWrite":4}}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	viewer, err := sql.Open("sqlite", viewerPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = viewer.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT NOT NULL, source_path TEXT, started_at INTEGER, ended_at INTEGER);
CREATE TABLE messages (session_id TEXT, ordinal INTEGER, role TEXT, ts INTEGER, model TEXT, context_tokens INTEGER, output_tokens INTEGER);
INSERT INTO sessions VALUES ('pi:s1', 'pi', ?, 1784224800000, 1784224810000);`, piPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = viewer.Close()
	db, err := store.Open(filepath.Join(directory, "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://127.0.0.1:1")
	cfg.UsageMonitor.GatepostDatabase = viewerPath
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	postJSON[map[string]any](t, server.URL+"/v1/providers/claude-main/token-sync", map[string]any{})
	observations, err := db.ListTokenObservations(t.Context(), "claude", time.Time{}, time.Time{})
	if err != nil || len(observations) != 1 || observations[0].Source != "gatepost-pi" || observations[0].CacheReadTokens != 30 {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
}

func TestTokenSyncBackfillsCompletedOwnedRun(t *testing.T) {
	directory := t.TempDir()
	viewerPath := filepath.Join(directory, "viewer.db")
	viewer, err := sql.Open("sqlite", viewerPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = viewer.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT NOT NULL, source_path TEXT, started_at INTEGER, ended_at INTEGER);
CREATE TABLE messages (session_id TEXT, ordinal INTEGER, role TEXT, ts INTEGER, model TEXT, context_tokens INTEGER, output_tokens INTEGER);`)
	if err != nil {
		t.Fatal(err)
	}
	_ = viewer.Close()
	db, err := store.Open(filepath.Join(directory, "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "claude-profile", ProviderAccountID: "claude-main", HarnessType: "claude-code",
		Model: "claude-haiku", WorkspaceProvider: "existing-directory",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: "owned-task", Name: "Owned task", ExecutionProfileID: "claude-profile", Type: domain.OneOff,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	run, err := db.AdmitTask(t.Context(), "owned-run", "owned-task", "claude-main", "", apiNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkRunRunning(t.Context(), run.ID, domain.Workspace{Directory: directory}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "stdout.jsonl")
	if err := os.WriteFile(output, []byte(`{"type":"result","modelUsage":{"claude-haiku":{"inputTokens":12,"outputTokens":3,"cacheReadInputTokens":4,"cacheCreationInputTokens":1}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	completedAt := apiNow.Add(time.Minute)
	if err := db.CompleteRun(t.Context(), run.ID, domain.RunCompletion{
		State: domain.RunCompleted, ExitCode: 0, OutputFile: output,
	}, completedAt); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("http://127.0.0.1:1")
	cfg.UsageMonitor.GatepostDatabase = viewerPath
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return completedAt }))
	defer server.Close()
	result := postJSON[struct {
		OwnedRunsInserted int `json:"owned_runs_inserted"`
	}](t, server.URL+"/v1/providers/claude-main/token-sync", map[string]any{})
	if result.OwnedRunsInserted != 1 {
		t.Fatalf("owned_runs_inserted = %d", result.OwnedRunsInserted)
	}
	postJSON[map[string]any](t, server.URL+"/v1/providers/claude-main/token-sync", map[string]any{})
	observations, err := db.ListTokenObservations(t.Context(), "claude", time.Time{}, time.Time{})
	if err != nil || len(observations) != 1 || observations[0].Source != "redline-run" || observations[0].CacheReadTokens != 4 {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
}

func TestServiceTaskAndSimulatedSchedulerFlow(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	profile := postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "codex-devx", "provider_account_id": "codex-main",
		"harness_type": "codex-cli", "workspace_provider": "devx",
	})
	if profile.ID != "codex-devx" {
		t.Fatalf("profile = %#v", profile)
	}
	task := postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "review", "name": "Review auth", "priority": 80,
		"execution_profile_id": "codex-devx", "type": "one_off",
	})
	if task.State != domain.Queued {
		t.Fatalf("task = %#v", task)
	}

	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{
		"provider_account_id": "codex-main",
	})
	if result.Result.Decision != decision.Admit || result.Result.Mode != decision.ModePace {
		t.Fatalf("decision = %#v", result.Result)
	}
	if result.SelectedTask == nil || result.SelectedTask.ID != "review" {
		t.Fatalf("selected task = %#v", result.SelectedTask)
	}
	decisions, err := db.ListSchedulerDecisions(t.Context(), "codex-main", 10)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	stored, err := db.GetTask(t.Context(), "review")
	if err != nil || stored.State != domain.Queued {
		t.Fatalf("simulation mutated task: %#v err=%v", stored, err)
	}
	resp, err := http.Get(server.URL + "/v1/scheduler/decisions?provider=codex-main")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var history []domain.SchedulerDecision
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || !bytes.Contains(history[0].DecisionJSON, []byte(`"decision":"RUN"`)) {
		t.Fatalf("history = %#v", history)
	}
}

func TestTaskCRUDOverServiceAPI(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "codex-devx", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "editable", "name": "Before", "priority": 10, "execution_profile_id": "codex-devx", "type": "one_off",
	})

	request, err := http.NewRequest(http.MethodPatch, server.URL+"/v1/tasks/editable", strings.NewReader(`{
		"name":"After", "prompt":"Do useful work", "priority":80, "type":"recurring",
		"min_interval":"2d", "require_repo_change":true, "dispatch_tier":"well_behind"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var updated domain.Task
	if err := json.NewDecoder(response.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || updated.Name != "After" || updated.Prompt != "Do useful work" || updated.MinInterval != 48*time.Hour || !updated.RequireRepoChange || updated.DispatchTier != domain.DispatchWellBehind {
		t.Fatalf("status=%d task=%#v", response.StatusCode, updated)
	}

	response, err = http.Get(server.URL + "/v1/tasks/editable")
	if err != nil {
		t.Fatal(err)
	}
	var fetched domain.Task
	if err := json.NewDecoder(response.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || fetched.Priority != 80 || fetched.Type != domain.Recurring {
		t.Fatalf("status=%d task=%#v", response.StatusCode, fetched)
	}

	request, _ = http.NewRequest(http.MethodDelete, server.URL+"/v1/tasks/editable", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", response.StatusCode)
	}
	response, err = http.Get(server.URL + "/v1/tasks/editable")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted GET status = %d", response.StatusCode)
	}
}

func TestExecutionProfileCRUDOverServiceAPI(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "editable-profile", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	request, err := http.NewRequest(http.MethodPatch, server.URL+"/v1/profiles/editable-profile", strings.NewReader(`{
		"model":"gpt-5.4-mini", "workspace_provider":"git-worktree", "repository":"/repo"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var updated domain.ExecutionProfile
	if err := json.NewDecoder(response.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || updated.Model != "gpt-5.4-mini" || updated.WorkspaceProvider != "git-worktree" || updated.Repository != "/repo" {
		t.Fatalf("status=%d profile=%#v", response.StatusCode, updated)
	}
	response, err = http.Get(server.URL + "/v1/profiles/editable-profile")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodDelete, server.URL+"/v1/profiles/editable-profile", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", response.StatusCode)
	}
}

func TestListAndTaskControlEndpoints(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "listed-profile", "provider_account_id": "codex-main", "harness_type": "pi", "model": "openai-codex/gpt-5.6-sol", "workspace_provider": "devx",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "controlled-task", "name": "Controlled", "priority": 42, "execution_profile_id": "listed-profile", "type": "one_off",
	})

	var profiles []domain.ExecutionProfile
	getJSON(t, server.URL+"/v1/profiles", &profiles)
	if len(profiles) != 1 || profiles[0].ID != "listed-profile" || profiles[0].HarnessType != "pi" {
		t.Fatalf("profiles = %#v", profiles)
	}
	var tasks []domain.Task
	getJSON(t, server.URL+"/v1/tasks", &tasks)
	if len(tasks) != 1 || tasks[0].ID != "controlled-task" {
		t.Fatalf("tasks = %#v", tasks)
	}

	disabled := postJSON[domain.Task](t, server.URL+"/v1/tasks/controlled-task/disable", map[string]any{})
	if disabled.Enabled || disabled.State != domain.Disabled {
		t.Fatalf("disabled = %#v", disabled)
	}
	enabled := postJSON[domain.Task](t, server.URL+"/v1/tasks/controlled-task/enable", map[string]any{})
	if !enabled.Enabled || enabled.State != domain.Queued {
		t.Fatalf("enabled = %#v", enabled)
	}
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks/missing/disable", `{}`, http.StatusNotFound)
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks/controlled-task/archive", `{}`, http.StatusInternalServerError)
}

func TestProfileAndTaskValidationErrorsOverServiceAPI(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	requestStatus(t, http.MethodPost, server.URL+"/v1/profiles", `{`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/profiles", `{"id":"bad","provider_account_id":"missing","harness_type":"pi","workspace_provider":"devx"}`, http.StatusBadRequest)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "valid", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	requestStatus(t, http.MethodPatch, server.URL+"/v1/profiles/valid", `{"provider_account_id":"missing"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks", `{`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks", `{"id":"bad-duration","name":"Bad","execution_profile_id":"valid","type":"recurring","min_interval":"never"}`, http.StatusBadRequest)
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "valid-task", "name": "Valid", "execution_profile_id": "valid", "type": "one_off",
	})
	requestStatus(t, http.MethodPatch, server.URL+"/v1/tasks/valid-task", `{"min_interval":"never"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPatch, server.URL+"/v1/tasks/valid-task", `{"execution_profile_id":"missing"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodDelete, server.URL+"/v1/profiles/valid", ``, http.StatusConflict)
}

func TestTaskAPIRoundTripsDispatchTier(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "profile", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	task := postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "filler", "name": "Filler", "execution_profile_id": "profile", "type": "one_off", "dispatch_tier": "expiring",
	})
	if task.DispatchTier != domain.DispatchExpiring {
		t.Fatalf("task = %#v", task)
	}
}

func TestSchedulerUnlocksJobsByDispatchTierBeforeApplyingPriority(t *testing.T) {
	payload := `{"providerId":"codex","fetchedAt":"2026-07-16T18:00:00Z","lines":[{"type":"progress","label":"Weekly","used":50,"limit":100,"resetsAt":"2026-07-18T18:00:00Z"}]}`
	server, _ := newAPIServer(t, payload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "profile", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "deep-filler", "name": "Deep filler", "priority": 100, "execution_profile_id": "profile", "type": "one_off", "dispatch_tier": "expiring",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "useful-work", "name": "Useful work", "priority": 20, "execution_profile_id": "profile", "type": "one_off", "dispatch_tier": "behind",
	})
	response := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
	if response.Result.UnlockedTier != domain.DispatchWellBehind || response.SelectedTask == nil || response.SelectedTask.ID != "useful-work" {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Result.CandidateRejections) == 0 || response.Result.CandidateRejections[0].TaskID != "deep-filler" {
		t.Fatalf("rejections = %#v", response.Result.CandidateRejections)
	}
}

func TestSchedulerExplainsRecurringTaskCooldown(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	lastCompleted := apiNow.Add(-time.Hour)
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: "cooling-down", Name: "Cooling down", Priority: 100, ExecutionProfileID: "profile",
		Type: domain.Recurring, MinInterval: 24 * time.Hour, LastCompletedAt: &lastCompleted,
	}, apiNow); err != nil {
		t.Fatal(err)
	}

	response := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
	if response.Result.Decision != decision.Admit || response.SelectedTask != nil {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Result.CandidateRejections) != 1 ||
		response.Result.CandidateRejections[0].TaskID != "cooling-down" ||
		!strings.Contains(response.Result.CandidateRejections[0].Reason, "cooldown until") {
		t.Fatalf("rejections = %#v", response.Result.CandidateRejections)
	}
}

func TestSchedulerExplainsRepositoryRevisionFailure(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profile := domain.ExecutionProfile{
		ID: "missing-repo", ProviderAccountID: "codex-main", HarnessType: "codex-cli",
		WorkspaceProvider: "git-worktree", Repository: "/missing/repository",
	}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: "repo-task", Name: "Repository task", Priority: 100, ExecutionProfileID: profile.ID,
		Type: domain.Recurring, RequireRepoChange: true,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	handler := api.NewServerWithDependencies(testConfig(usage.URL), db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil },
	}, fakeRevisionResolver{failures: map[string]error{"missing-repo": fmt.Errorf("repository is unavailable")}})
	server := httptest.NewServer(handler)
	defer server.Close()

	response := postJSON[struct {
		Result decision.Result `json:"result"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
	if len(response.Result.CandidateRejections) != 1 ||
		!strings.Contains(response.Result.CandidateRejections[0].Reason, "repository revision could not be read") ||
		!strings.Contains(response.Result.CandidateRejections[0].Reason, "repository is unavailable") {
		t.Fatalf("rejections = %#v", response.Result.CandidateRejections)
	}
}

func TestSchedulerSkipsExhaustedFableAndSelectsOpus(t *testing.T) {
	server, db := newAPIServer(t, claudeAllowancePayload(0, 1.0, 23*time.Hour))
	createClaudeCandidate(t, db, "fable-profile", "fable", "fable", "fable-task", 100)
	createClaudeCandidate(t, db, "opus-profile", "opus", "", "opus-task", 50)

	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-main"})
	if result.SelectedTask == nil || result.SelectedTask.ID != "opus-task" {
		t.Fatalf("selected task = %#v", result.SelectedTask)
	}
	if len(result.Result.CandidateRejections) == 0 ||
		result.Result.CandidateRejections[0].TaskID != "fable-task" ||
		!strings.Contains(result.Result.CandidateRejections[0].Reason, "exhausted") {
		t.Fatalf("rejections = %#v", result.Result.CandidateRejections)
	}
	if strings.Join(result.Result.RequiredPools, ",") != "session,weekly" {
		t.Fatalf("required pools = %#v", result.Result.RequiredPools)
	}
}

func TestFablePaceSignalSelectsOnlyFableTask(t *testing.T) {
	server, db := newAPIServer(t, claudeAllowancePayload(.60, .40, 48*time.Hour))
	createClaudeCandidate(t, db, "opus-profile", "opus", "", "opus-task", 100)
	createClaudeCandidate(t, db, "fable-profile", "claude-fable-5", "", "fable-task", 50)

	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-main"})
	if result.SelectedTask == nil || result.SelectedTask.ID != "fable-task" {
		t.Fatalf("selected task = %#v result=%#v", result.SelectedTask, result.Result)
	}
	if result.Result.Decision != decision.Admit ||
		strings.Join(result.Result.TriggeringPools, ",") != "model:fable:weekly" {
		t.Fatalf("result = %#v", result.Result)
	}
	if strings.Join(result.Result.RequiredPools, ",") != "session,weekly,model:fable:weekly" {
		t.Fatalf("required pools = %#v", result.Result.RequiredPools)
	}
}

func TestFableSignalDoesNotReleaseOpusTask(t *testing.T) {
	server, db := newAPIServer(t, claudeAllowancePayload(.60, .40, 48*time.Hour))
	createClaudeCandidate(t, db, "opus-profile", "opus", "", "opus-task", 100)

	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-main"})
	if result.SelectedTask != nil || result.Result.Decision != decision.Wait {
		t.Fatalf("selected=%#v result=%#v", result.SelectedTask, result.Result)
	}
}

func TestExecutionSkipsExhaustedFableAndAdmitsOpus(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, claudeAllowancePayload(0, 1.0, 23*time.Hour))
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createClaudeCandidate(t, db, "fable-profile", "fable", "fable", "fable-task", 100)
	createClaudeCandidate(t, db, "opus-profile", "opus", "", "opus-task", 50)
	executed := make(chan domain.Task, 1)
	handler := api.NewServerWithDependencies(testConfig(usage.URL), db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(_ context.Context, _ domain.Run, task domain.Task, _ domain.ExecutionProfile) error {
			executed <- task
			return nil
		},
	}, fakeRevisionResolver{})
	server := httptest.NewServer(handler)
	defer server.Close()

	result := postJSON[struct {
		Result decision.Result `json:"result"`
		Run    *domain.Run     `json:"run,omitempty"`
	}](t, server.URL+"/v1/scheduler/execute", map[string]any{"provider_account_id": "claude-main"})
	if result.Run == nil || result.Run.TaskID != "opus-task" {
		t.Fatalf("run=%#v result=%#v", result.Run, result.Result)
	}
	select {
	case task := <-executed:
		if task.ID != "opus-task" {
			t.Fatalf("executed task = %#v", task)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
}

func TestSimulatedSchedulerResolvesTaskRepositoryLikeExecution(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, profile := range []domain.ExecutionProfile{
		{ID: "changed", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory", Repository: "/repo/changed"},
		{ID: "fallback", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory", Repository: "/repo/fallback"},
	} {
		if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []domain.Task{
		{ID: "repo-change", Name: "Repo change", Priority: 100, ExecutionProfileID: "changed", Type: domain.Recurring, RequireRepoChange: true, LastSuccessfulSourceRevision: "old"},
		{ID: "fallback", Name: "Fallback", Priority: 10, ExecutionProfileID: "fallback", Type: domain.OneOff},
	} {
		if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	handler := api.NewServerWithDependencies(testConfig(usage.URL), db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil },
	}, fakeRevisionResolver{revisions: map[string]string{"changed": "new", "fallback": "new"}})
	server := httptest.NewServer(handler)
	defer server.Close()

	result := postJSON[struct {
		SelectedTask *domain.Task `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
	if result.SelectedTask == nil || result.SelectedTask.ID != "repo-change" {
		t.Fatalf("selected task = %#v, want repo-change", result.SelectedTask)
	}
}

func TestServiceProviderStatusAndDecision(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	refresh := postJSON[decision.UsageSnapshot](
		t, server.URL+"/v1/providers/claude-main/refresh", map[string]any{},
	)
	if refresh.Short == nil || refresh.Weekly.Remaining < 0.679999 || refresh.Weekly.Remaining > 0.680001 {
		t.Fatalf("refresh = %#v", refresh)
	}

	resp, err := http.Get(server.URL + "/v1/providers/claude-main/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", resp.StatusCode)
	}
	var status decision.UsageSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Provider != "claude" {
		t.Fatalf("status = %#v", status)
	}
}

func TestServiceHealth(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	resp, err := http.Get(server.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestDetailedHealthStartsHealthy(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	resp, err := http.Get(server.URL + "/v1/health/details?window=12h")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var health domain.OperationalHealth
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "healthy" || health.Window != "12h0m0s" {
		t.Fatalf("health = %#v", health)
	}
}

func TestDetailedHealthRejectsInvalidWindow(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	for _, configured := range []string{"invalid", "0s", "-1h"} {
		resp, err := http.Get(server.URL + "/v1/health/details?window=" + url.QueryEscape(configured))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("window %q status = %d", configured, resp.StatusCode)
		}
	}
}

func TestNotificationDeliveryHistoryEndpoint(t *testing.T) {
	server, db := newAPIServer(t, claudePayload)
	id, err := db.CreateNotificationDelivery(t.Context(), domain.EventRunFailed, json.RawMessage(`{"type":"run.failed"}`), apiNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteNotificationDelivery(t.Context(), id, "failed", "offline", apiNow); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(server.URL + "/v1/notifications")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var deliveries []domain.NotificationDelivery
	if err := json.NewDecoder(resp.Body).Decode(&deliveries); err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].LastError != "offline" {
		t.Fatalf("deliveries = %#v", deliveries)
	}
}

func TestSchedulerStatusIsExposedWhenDisabled(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	resp, err := http.Get(server.URL + "/v1/scheduler/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var status scheduler.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.PollInterval != "5m0s" {
		t.Fatalf("status = %#v", status)
	}
}

func TestAutomaticSchedulerResolvesEachTaskRepositoryAndRecordsTrigger(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
	for _, profile := range []domain.ExecutionProfile{
		{ID: "unchanged", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "git-worktree", Repository: "/repo/a"},
		{ID: "changed", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "git-worktree", Repository: "/repo/b"},
	} {
		if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []domain.Task{
		{ID: "skip", Name: "Skip", Priority: 100, ExecutionProfileID: "unchanged", Type: domain.Recurring, RequireRepoChange: true, LastSuccessfulSourceRevision: "same"},
		{ID: "run", Name: "Run", Priority: 80, ExecutionProfileID: "changed", Type: domain.OneOff, RequireRepoChange: true, LastSuccessfulSourceRevision: "old"},
	} {
		if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	executed := make(chan domain.Task, 1)
	handler := api.NewServerWithDependencies(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(_ context.Context, _ domain.Run, task domain.Task, _ domain.ExecutionProfile) error {
			executed <- task
			return nil
		},
	}, fakeRevisionResolver{revisions: map[string]string{"unchanged": "same", "changed": "new"}})
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartScheduler(ctx)
	select {
	case task := <-executed:
		if task.ID != "run" {
			t.Fatalf("executed task = %#v", task)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic scheduler did not dispatch")
	}
	cancel()
	handler.Wait()
	decisions, err := db.ListSchedulerDecisions(t.Context(), "codex-main", 10)
	if err != nil || len(decisions) != 1 || !bytes.Contains(decisions[0].DecisionJSON, []byte(`"trigger":"automatic"`)) {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	if !bytes.Contains(decisions[0].DecisionJSON, []byte("repository has not changed since the last successful run")) {
		t.Fatalf("unchanged repository rejection missing from decision: %s", decisions[0].DecisionJSON)
	}
}

func TestAutomaticSchedulerFillsConfiguredProviderConcurrency(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	delete(cfg.Providers, "claude-main")
	provider := cfg.Providers["codex-main"]
	provider.MaxConcurrentRuns = 2
	cfg.Providers["codex-main"] = provider
	cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "command",
		WorkspaceProvider: "existing-directory",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	for _, task := range []domain.Task{
		{ID: "first", Name: "First", Priority: 100, ExecutionProfileID: "profile", Type: domain.OneOff},
		{ID: "second", Name: "Second", Priority: 90, ExecutionProfileID: "profile", Type: domain.OneOff},
	} {
		if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan string, 2)
	release := make(chan struct{})
	handler := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(_ context.Context, _ domain.Run, task domain.Task, _ domain.ExecutionProfile) error {
			started <- task.ID
			<-release
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartScheduler(ctx)
	got := make([]string, 0, 2)
	for len(got) < 2 {
		select {
		case taskID := <-started:
			got = append(got, taskID)
		case <-time.After(time.Second):
			t.Fatalf("started = %v; automatic scheduler did not fill concurrency", got)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("started = %v", got)
	}
	active, err := db.ActiveRunCount(t.Context(), "codex-main")
	if err != nil || active != 2 {
		t.Fatalf("active=%d err=%v", active, err)
	}
	close(release)
	cancel()
	handler.Wait()
}

func TestAutomaticSchedulerSkipsActiveProviderWithoutFetchingUsage(t *testing.T) {
	var requests atomic.Int32
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	delete(cfg.Providers, "claude-main")
	cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
	profile := domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli",
		WorkspaceProvider: "existing-directory",
	}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "Task", ExecutionProfileID: "profile", Type: domain.OneOff}
	if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(t.Context(), "existing-run", task.ID, "codex-main", "", apiNow); err != nil {
		t.Fatal(err)
	}
	handler := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartScheduler(ctx)
	deadline := time.Now().Add(time.Second)
	found := false
	for time.Now().Before(deadline) {
		decisions, err := db.ListSchedulerDecisions(t.Context(), "codex-main", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(decisions) == 1 {
			found = true
			if !bytes.Contains(decisions[0].DecisionJSON, []byte(`"mode":"active_run"`)) {
				t.Fatalf("decision = %s", decisions[0].DecisionJSON)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	handler.Wait()
	if !found {
		t.Fatal("automatic active-run decision was not recorded")
	}
	if requests.Load() != 0 {
		t.Fatalf("OpenUsage requests = %d", requests.Load())
	}
}

func TestAutomaticSchedulerPersistsUsageFailureAttempt(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	delete(cfg.Providers, "claude-main")
	cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
	cfg.Notifications = config.Notifications{Enabled: true, Command: "true", Events: []string{domain.EventSchedulerError}}
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartScheduler(ctx)
	deadline := time.Now().Add(time.Second)
	var attempts []domain.DispatchAttempt
	for time.Now().Before(deadline) {
		attempts, err = db.ListDispatchAttempts(t.Context(), "codex-main", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(attempts) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	handler.Wait()
	if len(attempts) != 1 || attempts[0].Outcome != domain.DispatchError ||
		attempts[0].Trigger != "automatic" || !strings.Contains(attempts[0].Error, "HTTP 503") {
		t.Fatalf("attempts = %#v", attempts)
	}
	deliveries, err := db.ListNotificationDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "delivered" ||
		deliveries[0].EventType != domain.EventSchedulerError {
		t.Fatalf("deliveries=%#v err=%v", deliveries, err)
	}
}

func TestAutomaticSchedulerDoesNotPersistShutdownCancellationAsAnError(t *testing.T) {
	requestStarted := make(chan struct{})
	usage := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	delete(cfg.Providers, "claude-main")
	cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartScheduler(ctx)
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("usage request did not start")
	}
	cancel()
	handler.Wait()
	attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 {
		t.Fatalf("shutdown cancellation attempts = %#v", attempts)
	}
}

func TestAutomaticSchedulerCompletesHermesRemoteRunAndRecordsUsage(t *testing.T) {
	const credentialVariable = "REDLINE_TEST_HERMES_SESSION"
	t.Setenv(credentialVariable, `{"session_token":"scheduler-token"}`)
	gateway := hermesSchedulerGateway(t, "scheduler-token")
	defer gateway.Close()
	handler, db := automaticHermesServer(t, gateway.URL, credentialVariable)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler.StartScheduler(ctx)
	run := waitForTaskRun(t, db, "hermes-auto", domain.RunCompleted)
	cancel()
	handler.Wait()
	if run.External.SessionID != "stored-hermes-session" || run.External.RuntimeConnectionID != "hermes-auto-runtime" {
		t.Fatalf("run = %#v", run)
	}
	task, err := db.GetTask(t.Context(), "hermes-auto")
	if err != nil || task.State != domain.Completed {
		t.Fatalf("task=%#v err=%v", task, err)
	}
	observations, err := db.ListTokenObservations(t.Context(), "codex", time.Time{}, time.Time{})
	if err != nil || len(observations) != 1 || observations[0].SourceID != run.ID ||
		observations[0].InputTokens != 12 || observations[0].OutputTokens != 2 {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
	attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
	if err != nil || len(attempts) != 1 || attempts[0].Trigger != "automatic" ||
		attempts[0].Outcome != domain.DispatchAdmitted {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
}

func TestAutomaticHermesCredentialFailureLeavesAuditableFailedRun(t *testing.T) {
	handler, db := automaticHermesServer(t, "https://gateway.invalid", "REDLINE_MISSING_HERMES_CREDENTIAL")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler.StartScheduler(ctx)
	run := waitForTaskRun(t, db, "hermes-auto", domain.RunFailed)
	cancel()
	handler.Wait()
	if !strings.Contains(run.Error, `environment variable "REDLINE_MISSING_HERMES_CREDENTIAL" is empty`) {
		t.Fatalf("run error = %q", run.Error)
	}
	task, err := db.GetTask(t.Context(), "hermes-auto")
	if err != nil || task.State != domain.Failed {
		t.Fatalf("task=%#v err=%v", task, err)
	}
	events, err := db.ListRunEvents(t.Context(), run.ID, 100)
	if err != nil || len(events) == 0 || events[len(events)-1].Type != "run.failed" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func automaticHermesServer(
	t *testing.T,
	gatewayURL, credentialVariable string,
) (*api.Server, *store.DB) {
	t.Helper()
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	t.Cleanup(usage.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := testConfig(usage.URL)
	delete(cfg.Providers, "claude-main")
	cfg.RunArtifactsDir = t.TempDir()
	cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
	connection := domain.RuntimeConnection{
		ID: "hermes-auto-runtime", Runtime: "hermes", Transport: "gateway", URL: gatewayURL,
		CredentialSource: "environment", CredentialRef: credentialVariable, MaxConcurrentRuns: 1,
	}
	if err := db.CreateRuntimeConnection(t.Context(), connection, apiNow); err != nil {
		t.Fatal(err)
	}
	agentContext := domain.AgentContext{
		ID: "hermes-auto-context", RuntimeConnectionID: connection.ID,
		Profile: "default", WorkingDirectory: "/srv/project", SessionMode: "isolated",
	}
	if err := db.CreateAgentContext(t.Context(), agentContext, apiNow); err != nil {
		t.Fatal(err)
	}
	profile := domain.ExecutionProfile{
		ID: "hermes-auto-profile", ProviderAccountID: "codex-main",
		AgentContextID: agentContext.ID, HarnessType: "hermes",
		Model: "openai-codex/gpt-test", WorkspaceProvider: "runtime-owned", Repository: "/srv/project",
	}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{
		ID: "hermes-auto", Name: "Hermes automatic smoke", Prompt: "Reply OK.",
		Priority: 100, ExecutionProfileID: profile.ID, Type: domain.OneOff,
	}
	if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
		t.Fatal(err)
	}
	return api.NewServer(cfg, db, func() time.Time { return apiNow }), db
}

func waitForTaskRun(t *testing.T, db *store.DB, taskID string, state domain.RunState) domain.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := db.ListRuns(t.Context(), 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.TaskID == taskID && run.State == state {
				return run
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach run state %s", taskID, state)
	return domain.Run{}
}

func hermesSchedulerGateway(t *testing.T, token string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
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
			if wsjson.Read(r.Context(), socket, &request) != nil {
				return
			}
			result := map[string]any{}
			switch request.Method {
			case "session.create":
				result = map[string]any{
					"session_id": "live-hermes-session", "stored_session_id": "stored-hermes-session",
					"info": map[string]any{"model": "gpt-test", "provider": "openai-codex"},
				}
			case "session.usage":
				result = map[string]any{"calls": 1, "input": 12, "output": 2, "total": 14}
			case "session.status":
				result = map[string]any{"model": "gpt-test", "provider": "openai-codex"}
			}
			_ = wsjson.Write(r.Context(), socket, map[string]any{
				"jsonrpc": "2.0", "id": request.ID, "result": result,
			})
			if request.Method == "prompt.submit" {
				_ = wsjson.Write(r.Context(), socket, map[string]any{
					"jsonrpc": "2.0", "method": "event", "params": map[string]any{
						"type": "message.complete", "session_id": "live-hermes-session",
						"payload": map[string]any{"text": "OK"},
					},
				})
			}
		}
	})
	return httptest.NewServer(mux)
}

func TestManualExecutePersistsNoTaskAttemptAndListsIt(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[map[string]any](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "codex-main",
	})
	resp, err := http.Get(server.URL + "/v1/scheduler/attempts?provider=codex-main")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var attempts []domain.DispatchAttempt
	if err := json.NewDecoder(resp.Body).Decode(&attempts); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Outcome != domain.DispatchNoTask || attempts[0].Trigger != "manual" ||
		attempts[0].Reason != "no queued tasks are eligible" {
		t.Fatalf("attempts = %#v", attempts)
	}
}

func TestPausedManualExecuteIsPersistedBeforeConflictResponse(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	postJSON[map[string]any](t, server.URL+"/v1/providers/codex-main/pause", map[string]any{})
	data, _ := json.Marshal(map[string]string{"provider_account_id": "codex-main"})
	resp, err := http.Post(server.URL+"/v1/scheduler/execute", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != domain.DispatchWait || attempts[0].Mode != "paused" {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
}

func TestRunLogsRejectArtifactOutsideConfiguredRoot(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := domain.ExecutionProfile{ID: "p", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory"}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "t", Name: "Task", ExecutionProfileID: "p", Type: domain.OneOff}
	if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(t.Context(), "r", task.ID, "codex-main", "", apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRun(t.Context(), "r", domain.RunCompletion{
		State: domain.RunCompleted, ExitCode: 0, OutputFile: outside,
	}, apiNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(usage.URL)
	cfg.RunArtifactsDir = root
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	resp, err := http.Get(server.URL + "/v1/runs/r/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestMissingOptionalLifecycleLogIsAnEmptySuccessfulTail(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "logs-profile", "provider_account_id": "codex-main", "harness_type": "command", "workspace_provider": "existing-directory",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "logs-task", "name": "Logs task", "execution_profile_id": "logs-profile", "type": "one_off",
	})
	if _, err := db.AdmitTask(t.Context(), "logs-run", "logs-task", "codex-main", "revision", apiNow); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(server.URL + "/v1/runs/logs-run/logs?stream=finalize_stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tail artifacts.Tail
	if err := json.NewDecoder(resp.Body).Decode(&tail); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || tail.Content != "" || tail.SizeBytes != 0 || tail.Truncated {
		t.Fatalf("status=%d tail=%#v", resp.StatusCode, tail)
	}
}

func TestEmptyRunListIsJSONArray(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	resp, err := http.Get(server.URL + "/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	if strings.TrimSpace(body.String()) != "[]" {
		t.Fatalf("body = %s", body.String())
	}
}

func TestRunEventsEndpointReturnsTimeline(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	now := apiNow
	profile := domain.ExecutionProfile{ID: "events-profile", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory"}
	task := domain.Task{ID: "events-task", Name: "Events", ExecutionProfileID: profile.ID, Type: domain.OneOff}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), task, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(t.Context(), "events-run", task.ID, "codex-main", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRunEvent(t.Context(), domain.RunEvent{
		RunID: "events-run", Type: domain.RunEventStarted, OccurredAt: now,
		Payload: json.RawMessage(`{"task_id":"events-task"}`),
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(server.URL + "/v1/runs/events-run/events?limit=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var events []domain.RunEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(events) != 1 || events[0].Type != domain.RunEventStarted {
		t.Fatalf("status=%d events=%#v", resp.StatusCode, events)
	}
}

func TestPausedProviderDoesNotSelectTask(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[map[string]any](t, server.URL+"/v1/providers/codex-main/pause", map[string]any{})
	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{
		"provider_account_id": "codex-main",
	})
	if result.Result.Decision != decision.Wait || result.Result.Reason != "provider is paused" {
		t.Fatalf("result = %#v", result.Result)
	}
}

func TestProviderPolicyOverrideChangesDecisionAndPersistsInDashboard(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	initial := postJSON[decisionResponseForTest](t, server.URL+"/v1/providers/codex-main/decision", map[string]any{})
	if initial.Result.Policy != "standard" || initial.Result.Decision != decision.Admit {
		t.Fatalf("initial result = %#v", initial.Result)
	}

	request, err := http.NewRequest(http.MethodPatch, server.URL+"/v1/providers/codex-main/policy",
		strings.NewReader(`{"policy":"late"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var selection struct {
		Policy string `json:"policy"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(response.Body).Decode(&selection); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || selection.Policy != "late" || selection.Source != "override" {
		t.Fatalf("status=%d selection=%#v", response.StatusCode, selection)
	}

	overridden := postJSON[decisionResponseForTest](t, server.URL+"/v1/providers/codex-main/decision", map[string]any{})
	if overridden.Result.Policy != "late" || overridden.Result.Decision != decision.Wait {
		t.Fatalf("overridden result = %#v", overridden.Result)
	}
	postJSON[map[string]any](t, server.URL+"/v1/scheduler/evaluate", map[string]any{
		"provider_account_id": "codex-main",
	})
	decisions, err := db.ListSchedulerDecisions(t.Context(), "codex-main", 1)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	var persisted struct {
		Result decision.Result `json:"result"`
	}
	if err := json.Unmarshal(decisions[0].DecisionJSON, &persisted); err != nil ||
		persisted.Result.Policy != "late" {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	var dashboard struct {
		Policies  map[string]config.Policy `json:"policies"`
		Providers []struct {
			ID           string `json:"id"`
			Policy       string `json:"policy"`
			PolicySource string `json:"policy_source"`
		} `json:"providers"`
	}
	getJSON(t, server.URL+"/v1/dashboard", &dashboard)
	if _, ok := dashboard.Policies["late"]; !ok {
		t.Fatalf("policy catalog = %#v", dashboard.Policies)
	}
	var codexProvider struct {
		ID           string
		Policy       string
		PolicySource string
	}
	for _, provider := range dashboard.Providers {
		if provider.ID == "codex-main" {
			codexProvider.ID = provider.ID
			codexProvider.Policy = provider.Policy
			codexProvider.PolicySource = provider.PolicySource
		}
	}
	if codexProvider.Policy != "late" || codexProvider.PolicySource != "override" {
		t.Fatalf("codex provider = %#v", codexProvider)
	}

	requestStatus(t, http.MethodPatch, server.URL+"/v1/providers/codex-main/policy",
		`{"policy":"missing"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPatch, server.URL+"/v1/providers/missing/policy",
		`{"policy":"late"}`, http.StatusNotFound)
}

func TestProviderConcurrencyOverridePersistsInDashboardAndCanBeCleared(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	var updated struct {
		MaxConcurrentRuns int    `json:"max_concurrent_runs"`
		Source            string `json:"source"`
	}
	patchJSON(t, server.URL+"/v1/providers/codex-main/concurrency",
		map[string]any{"max_concurrent_runs": 3}, &updated)
	if updated.MaxConcurrentRuns != 3 || updated.Source != "override" {
		t.Fatalf("updated = %#v", updated)
	}
	var dashboard struct {
		Providers []struct {
			ID                       string `json:"id"`
			MaxConcurrentRuns        int    `json:"max_concurrent_runs"`
			DefaultMaxConcurrentRuns int    `json:"default_max_concurrent_runs"`
			ConcurrencySource        string `json:"concurrency_source"`
		} `json:"providers"`
	}
	getJSON(t, server.URL+"/v1/dashboard", &dashboard)
	var codexProvider *struct {
		ID                       string `json:"id"`
		MaxConcurrentRuns        int    `json:"max_concurrent_runs"`
		DefaultMaxConcurrentRuns int    `json:"default_max_concurrent_runs"`
		ConcurrencySource        string `json:"concurrency_source"`
	}
	for index := range dashboard.Providers {
		if dashboard.Providers[index].ID == "codex-main" {
			codexProvider = &dashboard.Providers[index]
		}
	}
	if codexProvider == nil || codexProvider.MaxConcurrentRuns != 3 ||
		codexProvider.DefaultMaxConcurrentRuns != 1 ||
		codexProvider.ConcurrencySource != "override" {
		t.Fatalf("dashboard = %#v", dashboard)
	}
	patchJSON(t, server.URL+"/v1/providers/codex-main/concurrency",
		map[string]any{"max_concurrent_runs": 0}, &updated)
	if updated.MaxConcurrentRuns != 1 || updated.Source != "config" {
		t.Fatalf("cleared = %#v", updated)
	}
	requestStatus(t, http.MethodPatch, server.URL+"/v1/providers/codex-main/concurrency",
		`{"max_concurrent_runs":-1}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPatch, server.URL+"/v1/providers/missing/concurrency",
		`{"max_concurrent_runs":2}`, http.StatusNotFound)
}

func TestCalibrationEndpointAndDecisionUseObservedWindowCost(t *testing.T) {
	server, db := newAPIServer(t, claudePayload)
	weeklyReset := time.Date(2026, 7, 17, 17, 0, 0, 0, time.UTC)
	for _, snapshot := range []decision.UsageSnapshot{
		calibrationSnapshot(apiNow.Add(-8*time.Hour), 1.00, 0.80, apiNow.Add(-6*time.Hour), weeklyReset),
		calibrationSnapshot(apiNow.Add(-7*time.Hour), 0.40, 0.75, apiNow.Add(-6*time.Hour), weeklyReset.Add(300*time.Millisecond)),
		calibrationSnapshot(apiNow.Add(-3*time.Hour), 0.90, 0.75, apiNow.Add(-time.Hour), weeklyReset),
		calibrationSnapshot(apiNow.Add(-2*time.Hour), 0.40, 0.71, apiNow.Add(-time.Hour), weeklyReset.Add(-300*time.Millisecond)),
	} {
		if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Get(server.URL + "/v1/providers/claude-main/calibration")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var estimate calibration.Estimate
	if err := json.NewDecoder(resp.Body).Decode(&estimate); err != nil {
		t.Fatal(err)
	}
	if estimate.Source != calibration.SourceObserved || estimate.Confidence != calibration.ConfidenceMedium {
		t.Fatalf("estimate = %#v", estimate)
	}

	result := postJSON[struct {
		Result decision.Result `json:"result"`
	}](t, server.URL+"/v1/providers/claude-main/decision", map[string]any{})
	if result.Result.WindowWeeklyCostSource != string(calibration.SourceObserved) ||
		result.Result.CalibrationConfidence != string(calibration.ConfidenceMedium) ||
		result.Result.WindowWeeklyCost == 0.08 {
		t.Fatalf("result = %#v", result.Result)
	}
}

func calibrationSnapshot(observed time.Time, shortRemaining, weeklyRemaining float64, shortReset, weeklyReset time.Time) decision.UsageSnapshot {
	return decision.UsageSnapshot{
		Provider: "claude", ObservedAt: observed,
		Short:  &decision.UsageWindow{Remaining: shortRemaining, ResetsAt: shortReset},
		Weekly: decision.UsageWindow{Remaining: weeklyRemaining, ResetsAt: weeklyReset},
		Source: "test", Confidence: "high",
	}
}

func TestExecuteAdmitsTaskAndStartsExecutor(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	executed := make(chan domain.Run, 1)
	handler := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(_ context.Context, run domain.Run, _ domain.Task, _ domain.ExecutionProfile) error {
			executed <- run
			return nil
		},
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "codex-devx", "provider_account_id": "codex-main",
		"harness_type": "codex-cli", "workspace_provider": "devx",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "review", "name": "Review auth", "prompt": "review", "priority": 80,
		"execution_profile_id": "codex-devx", "type": "one_off",
	})
	response := postJSON[struct {
		Run *domain.Run `json:"run"`
	}](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "codex-main",
	})
	if response.Run == nil || response.Run.State != domain.RunPreparing {
		t.Fatalf("run = %#v", response.Run)
	}
	select {
	case got := <-executed:
		if got.ID != response.Run.ID {
			t.Fatalf("executed run %q, want %q", got.ID, response.Run.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not started")
	}
}

func TestCandidatePreviewWithoutSnapshotReturnsUnavailableState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profile := domain.ExecutionProfile{ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "queued", Name: "Queued", Priority: 10, ExecutionProfileID: profile.ID, Type: domain.OneOff}, apiNow); err != nil {
		t.Fatal(err)
	}
	handler := api.NewDemoServer(testConfig("http://usage.invalid"), db, func() time.Time { return apiNow }, nil,
		&fakeHarnessDiscoverer{}, fakeExecutor{execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil }})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/providers/codex-main/candidates", nil)
	request.Host = "localhost"
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		DispatchAvailable bool   `json:"dispatch_available"`
		ProviderReason    string `json:"provider_reason"`
		SelectedTaskID    string `json:"selected_task_id"`
		Candidates        []struct {
			Eligible bool   `json:"eligible"`
			Reason   string `json:"reason"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.DispatchAvailable || response.ProviderReason == "" || response.SelectedTaskID != "" ||
		len(response.Candidates) != 1 || response.Candidates[0].Eligible || response.Candidates[0].Reason == "" {
		t.Fatalf("response=%#v", response)
	}

	stale := decision.UsageSnapshot{
		Provider: "codex", ObservedAt: apiNow.Add(-time.Hour),
		Weekly: decision.UsageWindow{Remaining: .6, ResetsAt: apiNow.Add(48 * time.Hour)},
		Source: "test", Confidence: "high",
	}
	if err := db.SaveSnapshot(t.Context(), stale, []byte(`{"stale":true}`)); err != nil {
		t.Fatal(err)
	}
	staleRecorder := httptest.NewRecorder()
	staleRequest := httptest.NewRequest(http.MethodGet, "/v1/providers/codex-main/candidates", nil)
	staleRequest.Host = "localhost"
	handler.ServeHTTP(staleRecorder, staleRequest)
	var staleResponse struct {
		DispatchAvailable bool   `json:"dispatch_available"`
		ProviderReason    string `json:"provider_reason"`
		SnapshotStale     bool   `json:"snapshot_stale"`
		SelectedTaskID    string `json:"selected_task_id"`
	}
	if err := json.NewDecoder(staleRecorder.Body).Decode(&staleResponse); err != nil {
		t.Fatal(err)
	}
	if staleRecorder.Code != http.StatusOK || staleResponse.DispatchAvailable || !staleResponse.SnapshotStale ||
		staleResponse.ProviderReason == "" || staleResponse.SelectedTaskID != "" {
		t.Fatalf("stale response status=%d response=%#v", staleRecorder.Code, staleResponse)
	}
}

func TestCandidatePreviewResolvesSharedProfileRevisionOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://usage.invalid")
	snapshot := decision.UsageSnapshot{
		Provider: "codex", ObservedAt: apiNow,
		Weekly: decision.UsageWindow{Remaining: .6, ResetsAt: apiNow.Add(48 * time.Hour)},
		Source: "test", Confidence: "high",
	}
	if err := db.SaveSnapshot(t.Context(), snapshot, []byte(`{"stored":true}`)); err != nil {
		t.Fatal(err)
	}
	profile := domain.ExecutionProfile{ID: "shared", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx", Repository: "/repo"}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := db.CreateTask(t.Context(), domain.Task{ID: id, Name: id, Priority: 10, ExecutionProfileID: profile.ID, Type: domain.OneOff}, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	handler := api.NewServerWithDependencies(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil },
	}, fakeRevisionResolver{revisions: map[string]string{"shared": "revision"}, calls: &calls})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/providers/codex-main/candidates", nil)
	request.Host = "localhost"
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("revision resolve calls=%d want=1", calls.Load())
	}
}

func TestCandidatePreviewIsReadOnlyAndTargetedDispatchCanSelectLowerPriority(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://usage.invalid")
	snapshot := decision.UsageSnapshot{
		Provider: "codex", ObservedAt: apiNow,
		Weekly: decision.UsageWindow{Remaining: .6, ResetsAt: apiNow.Add(48 * time.Hour)},
		Source: "test", Confidence: "high",
	}
	if err := db.SaveSnapshot(t.Context(), snapshot, []byte(`{"stored":true}`)); err != nil {
		t.Fatal(err)
	}
	profile := domain.ExecutionProfile{ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	for _, task := range []domain.Task{
		{ID: "blocked", Name: "Blocked", Priority: 110, ExecutionProfileID: profile.ID, Type: domain.OneOff, RequireRepoChange: true},
		{ID: "high", Name: "High", Priority: 100, ExecutionProfileID: profile.ID, Type: domain.OneOff},
		{ID: "low", Name: "Low", Priority: 10, ExecutionProfileID: profile.ID, Type: domain.OneOff},
	} {
		if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	executed := make(chan domain.Task, 1)
	handler := api.NewDemoServer(cfg, db, func() time.Time { return apiNow },
		map[string]decision.UsageSnapshot{"codex-main": snapshot}, &fakeHarnessDiscoverer{}, fakeExecutor{
			execute: func(_ context.Context, _ domain.Run, task domain.Task, _ domain.ExecutionProfile) error {
				executed <- task
				return nil
			},
		})

	requestWithBody := httptest.NewRecorder()
	bodyRequest := httptest.NewRequest(http.MethodPost, "/v1/tasks/low/dispatch", strings.NewReader(`{"current_revision":"forged"}`))
	bodyRequest.Host = "localhost"
	handler.ServeHTTP(requestWithBody, bodyRequest)
	if requestWithBody.Code != http.StatusBadRequest {
		t.Fatalf("dispatch body status=%d body=%s", requestWithBody.Code, requestWithBody.Body.String())
	}

	preview := httptest.NewRecorder()
	previewRequest := httptest.NewRequest(http.MethodGet, "/v1/providers/codex-main/candidates", nil)
	previewRequest.Host = "localhost"
	handler.ServeHTTP(preview, previewRequest)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	var candidates struct {
		Candidates []struct {
			TaskID   string `json:"task_id"`
			Eligible bool   `json:"eligible"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(preview.Body).Decode(&candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates.Candidates) != 3 || candidates.Candidates[0].TaskID != "blocked" || candidates.Candidates[0].Eligible ||
		candidates.Candidates[1].TaskID != "high" || candidates.Candidates[2].TaskID != "low" {
		t.Fatalf("candidates=%#v", candidates.Candidates)
	}
	if attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10); err != nil || len(attempts) != 0 {
		t.Fatalf("preview attempts=%#v err=%v", attempts, err)
	}
	if decisions, err := db.ListSchedulerDecisions(t.Context(), "codex-main", 10); err != nil || len(decisions) != 0 {
		t.Fatalf("preview decisions=%#v err=%v", decisions, err)
	}

	blockedDispatch := httptest.NewRecorder()
	blockedRequest := httptest.NewRequest(http.MethodPost, "/v1/tasks/blocked/dispatch", nil)
	blockedRequest.Host = "localhost"
	handler.ServeHTTP(blockedDispatch, blockedRequest)
	if blockedDispatch.Code != http.StatusOK {
		t.Fatalf("blocked dispatch status=%d body=%s", blockedDispatch.Code, blockedDispatch.Body.String())
	}
	var blockedResponse schedulerResponseForTest
	if err := json.NewDecoder(blockedDispatch.Body).Decode(&blockedResponse); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blockedResponse.Result.Reason, "repository revision is unavailable") {
		t.Fatalf("blocked reason=%q result=%#v", blockedResponse.Result.Reason, blockedResponse.Result)
	}

	if err := db.SetProviderPaused(t.Context(), "codex-main", true); err != nil {
		t.Fatal(err)
	}
	pausedPreview := httptest.NewRecorder()
	pausedPreviewRequest := httptest.NewRequest(http.MethodGet, "/v1/providers/codex-main/candidates", nil)
	pausedPreviewRequest.Host = "localhost"
	handler.ServeHTTP(pausedPreview, pausedPreviewRequest)
	var pausedCandidates struct {
		DispatchAvailable bool   `json:"dispatch_available"`
		ProviderReason    string `json:"provider_reason"`
		SelectedTaskID    string `json:"selected_task_id"`
	}
	if err := json.NewDecoder(pausedPreview.Body).Decode(&pausedCandidates); err != nil {
		t.Fatal(err)
	}
	if pausedPreview.Code != http.StatusOK || pausedCandidates.DispatchAvailable ||
		pausedCandidates.ProviderReason != "provider is paused" || pausedCandidates.SelectedTaskID != "" {
		t.Fatalf("paused preview status=%d response=%#v", pausedPreview.Code, pausedCandidates)
	}
	pausedDispatch := httptest.NewRecorder()
	pausedRequest := httptest.NewRequest(http.MethodPost, "/v1/tasks/low/dispatch", nil)
	pausedRequest.Host = "localhost"
	handler.ServeHTTP(pausedDispatch, pausedRequest)
	if pausedDispatch.Code != http.StatusConflict {
		t.Fatalf("paused dispatch status=%d body=%s", pausedDispatch.Code, pausedDispatch.Body.String())
	}
	if err := db.SetProviderPaused(t.Context(), "codex-main", false); err != nil {
		t.Fatal(err)
	}

	dispatch := httptest.NewRecorder()
	dispatchRequest := httptest.NewRequest(http.MethodPost, "/v1/tasks/low/dispatch", nil)
	dispatchRequest.Host = "localhost"
	handler.ServeHTTP(dispatch, dispatchRequest)
	if dispatch.Code != http.StatusAccepted {
		t.Fatalf("dispatch status=%d body=%s", dispatch.Code, dispatch.Body.String())
	}
	attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
	if err != nil || len(attempts) != 3 || attempts[0].Trigger != "manual-task" ||
		attempts[0].RequestedTaskID != "low" || attempts[0].SelectedTaskID != "low" ||
		attempts[1].RequestedTaskID != "low" || attempts[1].Mode != string(decision.ModePaused) ||
		attempts[2].RequestedTaskID != "blocked" || attempts[2].SelectedTaskID != "" {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
	select {
	case task := <-executed:
		if task.ID != "low" {
			t.Fatalf("executed task=%q", task.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("targeted executor was not started")
	}
	concurrencyPreview := httptest.NewRecorder()
	concurrencyRequest := httptest.NewRequest(http.MethodGet, "/v1/providers/codex-main/candidates", nil)
	concurrencyRequest.Host = "localhost"
	handler.ServeHTTP(concurrencyPreview, concurrencyRequest)
	var concurrencyResponse struct {
		DispatchAvailable bool   `json:"dispatch_available"`
		ProviderReason    string `json:"provider_reason"`
		SelectedTaskID    string `json:"selected_task_id"`
	}
	if err := json.NewDecoder(concurrencyPreview.Body).Decode(&concurrencyResponse); err != nil {
		t.Fatal(err)
	}
	if concurrencyPreview.Code != http.StatusOK || concurrencyResponse.DispatchAvailable ||
		!strings.Contains(concurrencyResponse.ProviderReason, "provider concurrency limit") || concurrencyResponse.SelectedTaskID != "" {
		t.Fatalf("concurrency preview status=%d response=%#v", concurrencyPreview.Code, concurrencyResponse)
	}
}

func TestConcurrentExecuteContentionIsWaitNotError(t *testing.T) {
	var usageRequests atomic.Int32
	usageBarrier := make(chan struct{})
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if usageRequests.Add(1) == 2 {
			close(usageBarrier)
		}
		select {
		case <-usageBarrier:
		case <-time.After(time.Second):
			t.Error("concurrent usage requests did not reach barrier")
		}
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	releaseExecutor := make(chan struct{})
	handler := api.NewServerWithExecutor(testConfig(usage.URL), db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error {
			<-releaseExecutor
			return nil
		},
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "profile", "provider_account_id": "codex-main",
		"harness_type": "codex-cli", "workspace_provider": "devx",
	})
	for _, id := range []string{"first", "second"} {
		postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
			"id": id, "name": id, "priority": 50,
			"execution_profile_id": "profile", "type": "one_off",
		})
	}

	start := make(chan struct{})
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			<-start
			body := bytes.NewBufferString(`{"provider_account_id":"codex-main"}`)
			resp, requestErr := http.Post(server.URL+"/v1/scheduler/execute", "application/json", body)
			if requestErr != nil {
				statuses <- 0
				return
			}
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	close(start)
	gotStatuses := []int{<-statuses, <-statuses}
	close(releaseExecutor)
	slices.Sort(gotStatuses)
	if !slices.Equal(gotStatuses, []int{http.StatusOK, http.StatusAccepted}) {
		t.Fatalf("statuses = %v", gotStatuses)
	}
	attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
	outcomes := []domain.DispatchOutcome{attempts[0].Outcome, attempts[1].Outcome}
	slices.Sort(outcomes)
	if !slices.Equal(outcomes, []domain.DispatchOutcome{domain.DispatchAdmitted, domain.DispatchWait}) {
		t.Fatalf("outcomes = %v; attempts=%#v", outcomes, attempts)
	}
	for _, attempt := range attempts {
		if attempt.Outcome == domain.DispatchError {
			t.Fatalf("normal admission contention recorded as error: %#v", attempt)
		}
	}
}

func TestConfiguredConcurrencySkipsSaturatedPoolAndAdmitsSharedTask(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, claudeAllowancePayload(1, 1, 24*time.Hour))
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	claude := cfg.Providers["claude-main"]
	claude.PoolConcurrency = map[string]int{"model:fable:weekly": 1}
	cfg.Providers["claude-main"] = claude
	releaseExecutor := make(chan struct{})
	handler := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error {
			<-releaseExecutor
			return nil
		},
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	defer close(releaseExecutor)
	var concurrency map[string]any
	patchJSON(t, server.URL+"/v1/providers/claude-main/concurrency",
		map[string]any{"max_concurrent_runs": 2}, &concurrency)
	for _, profile := range []map[string]any{
		{"id": "fable-profile", "provider_account_id": "claude-main", "harness_type": "claude-code",
			"model": "fable", "workspace_provider": "devx"},
		{"id": "opus-profile", "provider_account_id": "claude-main", "harness_type": "claude-code",
			"model": "opus", "workspace_provider": "devx"},
	} {
		postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", profile)
	}
	for _, task := range []map[string]any{
		{"id": "fable-one", "name": "Fable one", "priority": 100, "execution_profile_id": "fable-profile", "type": "one_off"},
		{"id": "fable-two", "name": "Fable two", "priority": 90, "execution_profile_id": "fable-profile", "type": "one_off"},
		{"id": "opus", "name": "Opus", "priority": 50, "execution_profile_id": "opus-profile", "type": "one_off"},
	} {
		postJSON[domain.Task](t, server.URL+"/v1/tasks", task)
	}
	first := postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "claude-main",
	})
	if first.SelectedTask == nil || first.SelectedTask.ID != "fable-one" {
		t.Fatalf("first response = %#v", first)
	}
	second := postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "claude-main",
	})
	if second.SelectedTask == nil || second.SelectedTask.ID != "opus" ||
		len(second.Result.CandidateRejections) == 0 ||
		!strings.Contains(second.Result.CandidateRejections[0].Reason, `model:fable:weekly`) {
		t.Fatalf("second response = %#v", second)
	}
	third := postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "claude-main",
	})
	if third.Run != nil || third.Result.Mode != decision.ModeActive ||
		!strings.Contains(third.Result.Reason, "provider concurrency limit 2") {
		t.Fatalf("third response = %#v", third)
	}
	active, err := db.ActiveRunCount(t.Context(), "claude-main")
	if err != nil || active != 2 {
		t.Fatalf("active=%d err=%v", active, err)
	}
	fableClaims, err := db.ActivePoolClaimCount(t.Context(), "claude-main", "model:fable:weekly")
	if err != nil || fableClaims != 1 {
		t.Fatalf("fable_claims=%d err=%v", fableClaims, err)
	}
	resp, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var dashboard struct {
		Providers []struct {
			ID                string         `json:"id"`
			MaxConcurrentRuns int            `json:"max_concurrent_runs"`
			ActiveRuns        int            `json:"active_runs"`
			PoolConcurrency   map[string]int `json:"pool_concurrency"`
			ActivePoolClaims  map[string]int `json:"active_pool_claims"`
			ConcurrencySource string         `json:"concurrency_source"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dashboard); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, provider := range dashboard.Providers {
		if provider.ID != "claude-main" {
			continue
		}
		found = provider.MaxConcurrentRuns == 2 && provider.ActiveRuns == 2 &&
			provider.PoolConcurrency["model:fable:weekly"] == 1 &&
			provider.ActivePoolClaims["model:fable:weekly"] == 1 &&
			provider.ConcurrencySource == "override"
	}
	if !found {
		t.Fatalf("dashboard providers = %#v", dashboard.Providers)
	}
}

func TestHermesContextConcurrencyConstrainsHigherProviderLimit(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	releaseExecutor := make(chan struct{})
	handler := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error {
			<-releaseExecutor
			return nil
		},
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	defer close(releaseExecutor)
	var concurrency map[string]any
	patchJSON(t, server.URL+"/v1/providers/codex-main/concurrency",
		map[string]any{"max_concurrent_runs": 2}, &concurrency)
	connection := postJSON[domain.RuntimeConnection](t, server.URL+"/v1/runtime-connections", map[string]any{
		"id": "hermes-remote", "runtime": "hermes", "transport": "gateway",
		"url": "https://hermes.example", "max_concurrent_runs": 2,
	})
	contextItem := postJSON[domain.AgentContext](t, server.URL+"/v1/agent-contexts", map[string]any{
		"id": "hermes-context", "runtime_connection_id": connection.ID,
		"session_mode": "isolated", "max_concurrent_runs": 1,
	})
	profile := postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "hermes-profile", "provider_account_id": "codex-main",
		"agent_context_id": contextItem.ID, "harness_type": "hermes",
		"workspace_provider": "runtime-owned", "repository": "/srv/redline",
	})
	for _, taskID := range []string{"hermes-one", "hermes-two"} {
		postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
			"id": taskID, "name": taskID, "priority": 50,
			"execution_profile_id": profile.ID, "type": "one_off",
		})
	}
	first := postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "codex-main",
	})
	if first.Run == nil {
		t.Fatalf("first response = %#v", first)
	}
	second := postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "codex-main",
	})
	if second.Run != nil || second.Result.Mode != decision.ModeActive ||
		!strings.Contains(second.Result.Reason, "agent context concurrency limit 1") {
		t.Fatalf("second response = %#v", second)
	}
}

func TestExecuteEndToEndWithRealCommandHarness(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	cfg.RunArtifactsDir = filepath.Join(t.TempDir(), "runs")
	cfg.Notifications = config.Notifications{Enabled: true, Command: "true", Events: []string{domain.EventRunCompleted}}
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	defer handler.Wait()
	server := httptest.NewServer(handler)
	defer server.Close()
	repository := t.TempDir()
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "command-local", "provider_account_id": "codex-main",
		"harness_type": "command", "harness_command": "printf '{\"done\":true}\\n'",
		"workspace_provider": "existing-directory", "repository": repository,
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "command-task", "name": "Command task", "prompt": "safe prompt", "priority": 80,
		"execution_profile_id": "command-local", "type": "one_off",
	})
	response := postJSON[struct {
		Run *domain.Run `json:"run"`
	}](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "codex-main",
	})
	if response.Run == nil {
		t.Fatal("expected admitted run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := db.GetRun(t.Context(), response.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.State == domain.RunCompleted {
			if run.OutputFile == "" {
				t.Fatal("completed run has no output file")
			}
			data, err := os.ReadFile(run.OutputFile)
			if err != nil || !bytes.Contains(data, []byte(`"done":true`)) {
				t.Fatalf("output=%s err=%v", data, err)
			}
			resp, err := http.Get(server.URL + "/v1/runs/" + run.ID + "/logs?stream=stdout&tail_bytes=8")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var tail artifacts.Tail
			if err := json.NewDecoder(resp.Body).Decode(&tail); err != nil {
				t.Fatal(err)
			}
			if !tail.Truncated || tail.Content != "\":true}\n" {
				t.Fatalf("tail = %#v", tail)
			}
			for _, query := range []string{"stream=combined", "stream=stdout&tail_bytes=0", "stream=stdout&tail_bytes=invalid"} {
				invalid, err := http.Get(server.URL + "/v1/runs/" + run.ID + "/logs?" + query)
				if err != nil {
					t.Fatal(err)
				}
				invalid.Body.Close()
				if invalid.StatusCode != http.StatusBadRequest {
					t.Fatalf("query %q status = %d", query, invalid.StatusCode)
				}
			}
			var deliveries []domain.NotificationDelivery
			for time.Now().Before(deadline) {
				deliveries, err = db.ListNotificationDeliveries(t.Context(), 10)
				if err != nil {
					t.Fatal(err)
				}
				if len(deliveries) == 1 && deliveries[0].Status != "pending" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if len(deliveries) != 1 || deliveries[0].EventType != domain.EventRunCompleted || deliveries[0].Status != "delivered" {
				t.Fatalf("deliveries=%#v", deliveries)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not complete")
}

func TestLifecycleLogStreamUsesManagedArtifact(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	cfg := testConfig(usage.URL)
	cfg.RunArtifactsDir = root
	profile := domain.ExecutionProfile{ID: "p", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory"}
	task := domain.Task{ID: "t", Name: "Task", ExecutionProfileID: profile.ID, Type: domain.OneOff}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(t.Context(), "run-lifecycle", task.ID, "codex-main", "", apiNow); err != nil {
		t.Fatal(err)
	}
	path := workspace.ArtifactPath(root, "run-lifecycle", "prepare", "stderr")
	if err := os.WriteFile(path, []byte("setup warning\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	resp, err := http.Get(server.URL + "/v1/runs/run-lifecycle/logs?stream=prepare_stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tail artifacts.Tail
	if err := json.NewDecoder(resp.Body).Decode(&tail); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || tail.Content != "setup warning\n" {
		t.Fatalf("status=%d tail=%#v", resp.StatusCode, tail)
	}
}

func newAPIServer(t *testing.T, payload string) (*httptest.Server, *store.DB) {
	t.Helper()
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, payload)
	}))
	t.Cleanup(usage.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := testConfig(usage.URL)
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, db
}

func TestRuntimeConnectionAndAgentContextConfigurationAPI(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	connection := postJSON[domain.RuntimeConnection](t, server.URL+"/v1/runtime-connections", map[string]any{
		"id": "hermes-pi", "runtime": "hermes", "transport": "gateway",
		"url": "http://gateway.test:9119", "credential_source": "hermes_desktop",
	})
	if connection.ID != "hermes-pi" || connection.MaxConcurrentRuns != 1 {
		t.Fatalf("connection = %#v", connection)
	}
	agentContext := postJSON[domain.AgentContext](t, server.URL+"/v1/agent-contexts", map[string]any{
		"id": "hermes-default", "runtime_connection_id": connection.ID,
		"profile": "default", "project": "redline", "working_directory": "/srv/redline",
		"session_mode": "isolated",
	})
	if agentContext.RuntimeConnectionID != connection.ID {
		t.Fatalf("agent context = %#v", agentContext)
	}
	profile := postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "hermes-redline", "provider_account_id": "codex-main",
		"agent_context_id": agentContext.ID, "harness_type": "hermes",
		"model": "openai-codex/gpt-5.5", "workspace_provider": "runtime-owned",
		"repository": "/srv/redline",
	})
	if profile.AgentContextID != agentContext.ID {
		t.Fatalf("profile = %#v", profile)
	}
	var connections []domain.RuntimeConnection
	getJSON(t, server.URL+"/v1/runtime-connections", &connections)
	var contexts []domain.AgentContext
	getJSON(t, server.URL+"/v1/agent-contexts", &contexts)
	if len(connections) != 1 || len(contexts) != 1 {
		t.Fatalf("connections=%#v contexts=%#v", connections, contexts)
	}
	requestStatus(t, http.MethodPatch, server.URL+"/v1/runtime-connections/"+connection.ID, `{
		"url":"https://gateway.example","credential_source":"environment",
		"credential_ref":"HERMES_GATEWAY_CREDENTIAL","max_concurrent_runs":2
	}`, http.StatusOK)
	var updatedConnection domain.RuntimeConnection
	getJSON(t, server.URL+"/v1/runtime-connections/"+connection.ID, &updatedConnection)
	if updatedConnection.URL != "https://gateway.example" ||
		updatedConnection.CredentialSource != "environment" || updatedConnection.MaxConcurrentRuns != 2 {
		t.Fatalf("updated connection = %#v", updatedConnection)
	}
	requestStatus(t, http.MethodPatch, server.URL+"/v1/agent-contexts/"+agentContext.ID, `{
		"working_directory":"/srv/new-redline","max_concurrent_runs":2
	}`, http.StatusOK)
	var updatedContext domain.AgentContext
	getJSON(t, server.URL+"/v1/agent-contexts/"+agentContext.ID, &updatedContext)
	if updatedContext.WorkingDirectory != "/srv/new-redline" || updatedContext.MaxConcurrentRuns != 2 {
		t.Fatalf("updated context = %#v", updatedContext)
	}

	requestStatus(t, http.MethodDelete, server.URL+"/v1/agent-contexts/"+agentContext.ID, "", http.StatusConflict)
	requestStatus(t, http.MethodDelete, server.URL+"/v1/profiles/"+profile.ID, "", http.StatusNoContent)
	requestStatus(t, http.MethodDelete, server.URL+"/v1/agent-contexts/"+agentContext.ID, "", http.StatusNoContent)
	requestStatus(t, http.MethodDelete, server.URL+"/v1/runtime-connections/"+connection.ID, "", http.StatusNoContent)
}

func TestRuntimeJobsCanBeDiscoveredAndTriggered(t *testing.T) {
	triggered := atomic.Bool{}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/jobs":
			_, _ = io.WriteString(w, `{"jobs":[{"id":"content-post","name":"Draft content post","state":"scheduled","enabled":true}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/jobs/content-post/run":
			triggered.Store(true)
			_, _ = io.WriteString(w, `{"job":{"id":"content-post","name":"Draft content post","state":"scheduled","enabled":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	server, _ := newAPIServer(t, codexPayload)
	connection := postJSON[domain.RuntimeConnection](t, server.URL+"/v1/runtime-connections", map[string]any{
		"id": "hermes-remote", "runtime": "hermes", "transport": "gateway", "url": gateway.URL,
	})
	var jobs []map[string]any
	getJSON(t, server.URL+"/v1/runtime-connections/"+connection.ID+"/jobs", &jobs)
	if len(jobs) != 1 || jobs[0]["id"] != "content-post" {
		t.Fatalf("jobs = %#v", jobs)
	}
	response, err := http.Post(
		server.URL+"/v1/runtime-connections/"+connection.ID+"/jobs/content-post/run",
		"application/json", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted || !triggered.Load() {
		t.Fatalf("status=%d triggered=%v", response.StatusCode, triggered.Load())
	}
}

func TestRuntimeConfigurationAPIUsesEmptyArraysAndRejectsIncompleteHermesProfile(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	var connections []domain.RuntimeConnection
	getJSON(t, server.URL+"/v1/runtime-connections", &connections)
	var contexts []domain.AgentContext
	getJSON(t, server.URL+"/v1/agent-contexts", &contexts)
	var imports []domain.RuntimeConnection
	getJSON(t, server.URL+"/v1/runtime-connections/imports", &imports)
	if connections == nil || contexts == nil || imports == nil {
		t.Fatalf("empty collections must be JSON arrays: connections=%#v contexts=%#v imports=%#v", connections, contexts, imports)
	}
	requestStatus(t, http.MethodPost, server.URL+"/v1/profiles", `{
		"id":"incomplete-hermes","provider_account_id":"codex-main",
		"harness_type":"hermes","workspace_provider":"runtime-owned","repository":"/srv/redline"
	}`, http.StatusBadRequest)
}

func testConfig(usageURL string) config.Config {
	return config.Config{
		Database: "unused", ActivePolicy: "standard", MaxSnapshotAge: "15m",
		Providers: map[string]config.Provider{
			"codex-main":  {Provider: "codex", OpenUsageURL: usageURL, WindowWeeklyCost: 0.10},
			"claude-main": {Provider: "claude", OpenUsageURL: usageURL, WindowWeeklyCost: 0.08},
		},
		Policies: map[string]config.Policy{
			"late": {
				TriggerMargin: 0.02, RollingReserve: 0.25,
				PaceThresholds: []config.PaceThreshold{{TimeRemaining: "24h", MinWeeklyRemaining: 0.90}},
			},
			"standard": {
				TriggerMargin: 0.02, RollingReserve: 0.25,
				PaceThresholds: []config.PaceThreshold{{TimeRemaining: "72h", MinWeeklyRemaining: 0.50}},
			},
		},
	}
}

type decisionResponseForTest struct {
	Snapshot decision.UsageSnapshot `json:"snapshot"`
	Result   decision.Result        `json:"result"`
}

type schedulerResponseForTest struct {
	Result       decision.Result `json:"result"`
	SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	Run          *domain.Run     `json:"run,omitempty"`
}

func createClaudeCandidate(
	t *testing.T,
	db *store.DB,
	profileID, model, budgetGroup, taskID string,
	priority int,
) {
	t.Helper()
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: profileID, ProviderAccountID: "claude-main", HarnessType: "claude-code",
		Model: model, BudgetModelGroup: budgetGroup, WorkspaceProvider: "devx",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: taskID, Name: taskID, Priority: priority, ExecutionProfileID: profileID, Type: domain.OneOff,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
}

func claudeAllowancePayload(fableRemaining, sharedRemaining float64, untilReset time.Duration) string {
	shortReset := apiNow.Add(5 * time.Hour).Format(time.RFC3339Nano)
	weeklyReset := apiNow.Add(untilReset).Format(time.RFC3339Nano)
	return fmt.Sprintf(`{
  "providerId":"claude", "fetchedAt":%q,
  "lines":[
    {"type":"progress","label":"Session","used":0,"limit":100,"periodDurationMs":18000000,"resetsAt":%q},
    {"type":"progress","label":"Weekly","used":%f,"limit":100,"periodDurationMs":604800000,"resetsAt":%q},
    {"type":"progress","label":"Fable","used":%f,"limit":100,"periodDurationMs":604800000,"resetsAt":%q}
  ]}`,
		apiNow.Format(time.RFC3339Nano), shortReset, (1-sharedRemaining)*100, weeklyReset,
		(1-fableRemaining)*100, weeklyReset)
}

type fakeExecutor struct {
	execute func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error
}

type fakeHarnessDiscoverer struct {
	catalog discovery.Catalog
	calls   atomic.Int32
}

func (f *fakeHarnessDiscoverer) Discover(context.Context) discovery.Catalog {
	f.calls.Add(1)
	return f.catalog
}

type fakeRevisionResolver struct {
	revisions map[string]string
	failures  map[string]error
	calls     *atomic.Int32
}

func (f fakeRevisionResolver) Resolve(_ context.Context, profile domain.ExecutionProfile) (string, error) {
	if f.calls != nil {
		f.calls.Add(1)
	}
	if err := f.failures[profile.ID]; err != nil {
		return "", err
	}
	return f.revisions[profile.ID], nil
}

func (f fakeExecutor) Execute(ctx context.Context, run domain.Run, task domain.Task, profile domain.ExecutionProfile) error {
	return f.execute(ctx, run, task, profile)
}

func postJSON[T any](t *testing.T, url string, body any) T {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem any
		_ = json.NewDecoder(resp.Body).Decode(&problem)
		t.Fatalf("POST %s: status=%d body=%#v", url, resp.StatusCode, problem)
	}
	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func patchJSON(t *testing.T, url string, body, result any) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		contents, _ := io.ReadAll(response.Body)
		t.Fatalf("PATCH %s: status=%d body=%s", url, response.StatusCode, contents)
	}
	if err := json.NewDecoder(response.Body).Decode(result); err != nil {
		t.Fatal(err)
	}
}

func getJSON(t *testing.T, url string, result any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status=%d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		t.Fatal(err)
	}
}

func requestStatus(t *testing.T, method, url, body string, want int) {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		contents, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, url, response.StatusCode, want, contents)
	}
}

const codexPayload = `{
  "providerId":"codex", "fetchedAt":"2026-07-16T18:00:00Z",
  "lines":[{"type":"progress","label":"Weekly","used":40,"limit":100,
  "resetsAt":"2026-07-18T18:00:00Z"}]}`

const claudePayload = `{
  "providerId":"claude", "fetchedAt":"2026-07-16T18:00:00Z",
  "lines":[
    {"type":"progress","label":"Session","used":20,"limit":100,"resetsAt":"2026-07-16T20:00:00Z"},
    {"type":"progress","label":"Weekly","used":32,"limit":100,"resetsAt":"2026-07-17T17:00:00Z"}
  ]}`
