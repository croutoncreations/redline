package apicredits

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	fakeConsoleSessionKey = "sk-ant-sid01-FAKEKEYFORTESTSONLY0000000000000000"
	fakeConsoleOrg        = "22222222-2222-4222-8222-222222222222"
)

// fakeConsole serves the organizations list and one organization's credits.
func fakeConsole(t *testing.T, creditsStatus int, creditsBody string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	orgs := string(loadConsoleFixture(t, "console-organizations.json"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(context.Background()))
		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write([]byte(orgs))
		case "/api/organizations/" + fakeConsoleOrg + "/prepaid/credits":
			w.WriteHeader(creditsStatus)
			_, _ = w.Write([]byte(creditsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func testConsoleClient(server *httptest.Server) ConsoleClient {
	return ConsoleClient{HTTPClient: server.Client(), BaseURL: server.URL, UserAgent: "Synthetic-UA/1.0",
		Now: func() time.Time { return fixtureNow }}
}

func TestConsoleClientReadsTheConfiguredOrganization(t *testing.T) {
	server, seen := fakeConsole(t, http.StatusOK, string(loadConsoleFixture(t, "console-prepaid-credits.json")))
	report, err := testConsoleClient(server).Fetch(context.Background(), Credential{SessionKey: fakeConsoleSessionKey, OrganizationID: fakeConsoleOrg})
	if err != nil {
		t.Fatal(err)
	}
	if report.Promo.Remaining != 62.4 || report.FetchedAt == nil || !report.FetchedAt.Equal(fixtureNow) {
		t.Fatalf("report = %+v", report)
	}
	if len(*seen) != 1 {
		t.Fatalf("requests = %d, want only the credits request", len(*seen))
	}
	req := (*seen)[0]
	if req.Method != http.MethodGet || req.URL.Path != "/api/organizations/"+fakeConsoleOrg+"/prepaid/credits" {
		t.Fatalf("request = %s %s", req.Method, req.URL.Path)
	}
	if req.Header.Get("Cookie") != "sessionKey="+fakeConsoleSessionKey || req.Header.Get("Accept") != "application/json" ||
		req.Header.Get("User-Agent") != "Synthetic-UA/1.0" || req.Header.Get("Authorization") != "" {
		t.Fatalf("headers = %v", req.Header)
	}
}

// Without organization_id the one organization with API access is read.
func TestConsoleClientFindsTheAPIOrganization(t *testing.T) {
	server, seen := fakeConsole(t, http.StatusOK, string(loadConsoleFixture(t, "console-prepaid-credits.json")))
	if _, err := testConsoleClient(server).Fetch(context.Background(), Credential{SessionKey: fakeConsoleSessionKey}); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 || (*seen)[0].URL.Path != "/api/organizations" {
		t.Fatalf("requests = %d", len(*seen))
	}
}

func TestConsoleClientMapsStatuses(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized:    ErrConsoleSessionExpired,
		http.StatusForbidden:       ErrConsoleSessionExpired,
		http.StatusNotFound:        ErrConsoleOrganizationNotFound,
		http.StatusTooManyRequests: ErrConsoleRateLimited,
	} {
		server, _ := fakeConsole(t, status, `{"error":"`+fakeConsoleSessionKey+`"}`)
		_, err := testConsoleClient(server).Fetch(context.Background(), Credential{SessionKey: fakeConsoleSessionKey, OrganizationID: fakeConsoleOrg})
		if !errors.Is(err, want) {
			t.Fatalf("%d: err = %v, want %v", status, err, want)
		}
		if strings.Contains(err.Error(), "sk-ant") {
			t.Fatalf("%d: error leaks the key", status)
		}
	}
	server, _ := fakeConsole(t, http.StatusInternalServerError, `{"error":"`+fakeConsoleSessionKey+`"}`)
	_, err := testConsoleClient(server).Fetch(context.Background(), Credential{SessionKey: fakeConsoleSessionKey, OrganizationID: fakeConsoleOrg})
	if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "sk-ant") {
		t.Fatalf("500: err = %v", err)
	}
	// A missing organization is not a sign-in problem.
	if errors.Is(ErrConsoleOrganizationNotFound, ErrConsoleSessionExpired) {
		t.Fatal("not found reads as signed out")
	}
}

func TestConsoleClientClassifiesStatusBeforeReadingBody(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusForbidden:       ErrConsoleSessionExpired,
		http.StatusTooManyRequests: ErrConsoleRateLimited,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error"`))
		}))
		_, err := testConsoleClient(server).Fetch(context.Background(), Credential{SessionKey: fakeConsoleSessionKey, OrganizationID: fakeConsoleOrg})
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("%d: err = %v, want %v", status, err, want)
		}
	}
}

// Only a Console session key is ever sent; an API key, OpenAI key or
// OAuth token pasted by mistake is refused before any request.
func TestConsoleClientRefusesOtherKeysLocally(t *testing.T) {
	server, seen := fakeConsole(t, http.StatusOK, string(loadConsoleFixture(t, "console-prepaid-credits.json")))
	for _, key := range []string{"sk-ant-api03-abc", "sess-abc", "sk-ant-oat01-abc", "eyJhbGciOi.jwt"} {
		_, err := testConsoleClient(server).Fetch(context.Background(), Credential{SessionKey: key, OrganizationID: fakeConsoleOrg})
		if !errors.Is(err, ErrNotConsoleSessionKey) {
			t.Fatalf("%s: err = %v", key[:6], err)
		}
	}
	if _, err := testConsoleClient(server).Fetch(context.Background(), Credential{SessionKey: " "}); !errors.Is(err, ErrNoConsoleSessionKey) {
		t.Fatalf("empty: err = %v", err)
	}
	if len(*seen) != 0 {
		t.Fatalf("requests = %d", len(*seen))
	}
}

// A malformed organization_id never reaches a URL.
func TestConsoleClientRefusesMalformedOrganization(t *testing.T) {
	server, seen := fakeConsole(t, http.StatusOK, "{}")
	for _, org := range []string{"../organizations", "org-abc", fakeConsoleOrg + "/x"} {
		if _, err := testConsoleClient(server).Fetch(context.Background(), Credential{SessionKey: fakeConsoleSessionKey, OrganizationID: org}); err == nil {
			t.Fatalf("%q accepted", org)
		}
	}
	if len(*seen) != 0 {
		t.Fatalf("requests = %d", len(*seen))
	}
}

func TestDefaultConsoleClientTargetsOnlyTheConsole(t *testing.T) {
	client := ConsoleClient{}
	if got := client.organizationsURL(); got != "https://platform.claude.com/api/organizations" {
		t.Fatalf("organizations = %s", got)
	}
	if got := client.creditsURL(fakeConsoleOrg); got != "https://platform.claude.com/api/organizations/"+fakeConsoleOrg+"/prepaid/credits" {
		t.Fatalf("credits = %s", got)
	}
	if ua := client.userAgent(); !strings.Contains(ua, "Mozilla/5.0") {
		t.Fatalf("default user agent = %q, want a browser-like one", ua)
	}
}

func TestConsoleClientDoesNotFollowRedirects(t *testing.T) {
	target, targetSeen := fakeConsole(t, http.StatusOK, string(loadConsoleFixture(t, "console-prepaid-credits.json")))
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	client := ConsoleClient{BaseURL: redirect.URL, Now: func() time.Time { return fixtureNow }}
	if _, err := client.Fetch(context.Background(), Credential{SessionKey: fakeConsoleSessionKey}); err == nil {
		t.Fatal("followed a redirect")
	}
	if len(*targetSeen) != 0 {
		t.Fatal("session key was forwarded to the redirect target")
	}
}

// consoleOrganizations serves only GET /api/organizations with the given
// status and body.
func consoleOrganizations(t *testing.T, status int, body string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(context.Background()))
		if r.URL.Path != "/api/organizations" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func TestConsoleClientAPIOrganizationFindsTheOneAPIOrganization(t *testing.T) {
	server, seen := fakeConsole(t, http.StatusOK, "{}")
	org, err := testConsoleClient(server).APIOrganization(context.Background(), fakeConsoleSessionKey)
	if err != nil {
		t.Fatal(err)
	}
	if org.UUID != fakeConsoleOrg || org.DisplayName() != "Synthetic Org" {
		t.Fatalf("org = %+v", org)
	}
	if len(*seen) != 1 || (*seen)[0].URL.Path != "/api/organizations" ||
		(*seen)[0].Header.Get("Cookie") != "sessionKey="+fakeConsoleSessionKey {
		t.Fatalf("requests = %d", len(*seen))
	}
}

func TestConsoleClientAPIOrganizationErrors(t *testing.T) {
	server, _ := consoleOrganizations(t, http.StatusUnauthorized, `{"error":"`+fakeConsoleSessionKey+`"}`)
	if _, err := testConsoleClient(server).APIOrganization(context.Background(), fakeConsoleSessionKey); !errors.Is(err, ErrConsoleSessionExpired) {
		t.Fatalf("401: err = %v", err)
	}
	server, _ = consoleOrganizations(t, http.StatusOK, `[{"uuid":"11111111-1111-4111-8111-111111111111","name":"Chat","capabilities":["chat"]}]`)
	if _, err := testConsoleClient(server).APIOrganization(context.Background(), fakeConsoleSessionKey); !errors.Is(err, ErrNoConsoleAPIOrganization) {
		t.Fatalf("none: err = %v", err)
	}
	server, _ = consoleOrganizations(t, http.StatusOK, `[{"uuid":"11111111-1111-4111-8111-111111111111","name":"One","capabilities":["api"]},`+
		`{"uuid":"22222222-2222-4222-8222-22222222222A","name":"Two","capabilities":["api"]},{"uuid":"33333333-3333-4333-8333-333333333333","name":"Chat","capabilities":["chat"]}]`)
	_, err := testConsoleClient(server).APIOrganization(context.Background(), fakeConsoleSessionKey)
	var several *SeveralConsoleOrganizationsError
	if !errors.Is(err, ErrSeveralConsoleOrganizations) || !errors.As(err, &several) || len(several.Organizations) != 2 ||
		several.Organizations[1].UUID != "22222222-2222-4222-8222-22222222222a" || several.Organizations[0].DisplayName() != "One" {
		t.Fatalf("several: err = %v", err)
	}
	server, _ = consoleOrganizations(t, http.StatusOK, `[{"uuid":"../../steal","name":"Bad","capabilities":["api"]},{"uuid":"`+fakeConsoleOrg+`","capabilities":["api"]}]`)
	if _, err := testConsoleClient(server).APIOrganization(context.Background(), fakeConsoleSessionKey); err == nil || errors.Is(err, ErrSeveralConsoleOrganizations) {
		t.Fatalf("malformed: err = %v", err)
	}
	// Another kind of key is refused before any request.
	server, seen := consoleOrganizations(t, http.StatusOK, `[]`)
	if _, err := testConsoleClient(server).APIOrganization(context.Background(), "sk-ant-api03-abc"); !errors.Is(err, ErrNotConsoleSessionKey) || len(*seen) != 0 {
		t.Fatalf("api key: err = %v requests = %d", err, len(*seen))
	}
}

func TestConsoleClientAPIOrganizationDoesNotFollowRedirects(t *testing.T) {
	target, targetSeen := fakeConsole(t, http.StatusOK, "{}")
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	if _, err := (ConsoleClient{BaseURL: redirect.URL}).APIOrganization(context.Background(), fakeConsoleSessionKey); err == nil {
		t.Fatal("followed a redirect")
	}
	if len(*targetSeen) != 0 {
		t.Fatal("session key was forwarded to the redirect target")
	}
}

// A personal organization is often named after its owner's email; the
// display name never repeats an address or a terminal control sequence.
func TestConsoleOrganizationDisplayNameIsSafeToPrint(t *testing.T) {
	for name, want := range map[string]string{
		"someone@example.invalid's Organization": "[account]'s Organization",
		"Acme\x1b[31m Team\u202e":                "Acme[31m Team",
		"  Padded  ":                             "Padded",
		strings.Repeat("x", 70):                  strings.Repeat("x", 59) + "…",
	} {
		if got := (ConsoleOrganization{Name: name}).DisplayName(); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", name, got, want)
		}
	}
}
