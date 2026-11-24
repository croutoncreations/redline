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

const fakeSessionKey = "sess-FAKEKEYFORTESTSONLY000000000000000000"

func fakePlatform(t *testing.T, status int, body string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(context.Background()))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func testClient(server *httptest.Server) Client {
	return Client{
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
		Now:        func() time.Time { return fixtureNow },
	}
}

func TestClientFetchesGrantsForTheOrganization(t *testing.T) {
	server, seen := fakePlatform(t, http.StatusOK, string(loadFixture(t)))
	report, err := testClient(server).Fetch(context.Background(), Credential{SessionKey: fakeSessionKey, OrganizationID: "org-test"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Promo.Remaining != 110 {
		t.Fatalf("promo remaining = %v", report.Promo.Remaining)
	}
	if report.FetchedAt == nil || !report.FetchedAt.Equal(fixtureNow) {
		t.Fatalf("fetched at = %v", report.FetchedAt)
	}
	if len(*seen) != 1 {
		t.Fatalf("requests = %d", len(*seen))
	}
	req := (*seen)[0]
	if req.Method != http.MethodGet || req.URL.Path != "/v1/dashboard/billing/credit_grants" {
		t.Fatalf("request = %s %s", req.Method, req.URL.Path)
	}
	if req.Header.Get("Authorization") != "Bearer "+fakeSessionKey || req.Header.Get("OpenAI-Organization") != "org-test" {
		t.Fatalf("headers = %v", req.Header)
	}
}

// The organization header is optional: a session key alone reads the
// default organization.
func TestClientOmitsEmptyOrganization(t *testing.T) {
	server, seen := fakePlatform(t, http.StatusOK, string(loadFixture(t)))
	if _, err := testClient(server).Fetch(context.Background(), Credential{SessionKey: fakeSessionKey}); err != nil {
		t.Fatal(err)
	}
	if _, present := (*seen)[0].Header["Openai-Organization"]; present {
		t.Fatal("sent an empty organization header")
	}
}

// A rejected session is a distinct, credential-free error so the dashboard
// can ask for a fresh key.
func TestClientRejectedSessionIsExpiredWithoutLeakingKey(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		server, _ := fakePlatform(t, status, `{"error":"bad key `+fakeSessionKey+`"}`)
		_, err := testClient(server).Fetch(context.Background(), Credential{SessionKey: fakeSessionKey})
		if !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("%d: err = %v, want ErrSessionExpired", status, err)
		}
		if strings.Contains(err.Error(), "sess-") {
			t.Fatalf("%d: error leaks key: %v", status, err)
		}
	}
}

func TestClientOtherFailuresNeverEchoBody(t *testing.T) {
	server, _ := fakePlatform(t, http.StatusTooManyRequests, `{"error":"`+fakeSessionKey+`"}`)
	_, err := testClient(server).Fetch(context.Background(), Credential{SessionKey: fakeSessionKey})
	if err == nil || strings.Contains(err.Error(), "sess-") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestClientWithoutSessionKeyMakesNoRequest(t *testing.T) {
	server, seen := fakePlatform(t, http.StatusOK, string(loadFixture(t)))
	_, err := testClient(server).Fetch(context.Background(), Credential{SessionKey: "  "})
	if !errors.Is(err, ErrNoSessionKey) || len(*seen) != 0 {
		t.Fatalf("err = %v, requests = %d", err, len(*seen))
	}
}

// Only a platform session key is ever sent. An API key pasted by mistake is
// refused locally, before it can leave the machine.
func TestClientRefusesNonSessionKeys(t *testing.T) {
	server, seen := fakePlatform(t, http.StatusOK, string(loadFixture(t)))
	for _, key := range []string{"sk-proj-abc", "sk-admin-abc", "eyJhbGciOi.jwt.token"} {
		_, err := testClient(server).Fetch(context.Background(), Credential{SessionKey: key})
		if !errors.Is(err, ErrNotSessionKey) {
			t.Fatalf("%s: err = %v", key[:6], err)
		}
	}
	if len(*seen) != 0 {
		t.Fatalf("requests = %d", len(*seen))
	}
}

// The production client only talks to api.openai.com over HTTPS; a
// configured BaseURL is a test seam that the default client never uses.
func TestDefaultClientTargetsOnlyThePlatformEndpoint(t *testing.T) {
	if got := (Client{}).endpoint(); got != "https://api.openai.com/v1/dashboard/billing/credit_grants" {
		t.Fatalf("endpoint = %s", got)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	target, targetSeen := fakePlatform(t, http.StatusOK, string(loadFixture(t)))
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	client := Client{BaseURL: redirect.URL, Now: func() time.Time { return fixtureNow }}
	if _, err := client.Fetch(context.Background(), Credential{SessionKey: fakeSessionKey}); err == nil {
		t.Fatal("followed a redirect")
	}
	if len(*targetSeen) != 0 {
		t.Fatal("session key was forwarded to the redirect target")
	}
}
