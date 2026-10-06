package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/store"
)

// captureLog redirects the standard logger for the duration of a test. These
// tests are deliberately not parallel: the logger is process-global.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buffer)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buffer
}

func TestServerErrorsAreLoggedWithMethodAndPathButNotQuery(t *testing.T) {
	logs := captureLog(t)
	server, db := newAPIServer(t, codexPayload)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(server.URL + "/v1/dashboard?token=hunter2")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	got := logs.String()
	for _, want := range []string{"redline api GET /v1/dashboard: responded 500: "} {
		if !strings.Contains(got, want) {
			t.Fatalf("log = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "hunter2") {
		t.Fatalf("log leaked the query string: %q", got)
	}
}

func TestClientErrorsAreNotLoggedAsServerFailures(t *testing.T) {
	logs := captureLog(t)
	server, _ := newAPIServer(t, codexPayload)
	response, err := http.Get(server.URL + "/v1/tasks/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
	if logs.Len() != 0 {
		t.Fatalf("log = %q, want nothing for a 404", logs.String())
	}
}

func TestUsageFetchFailureNamesProviderAndSource(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := httptest.NewServer(api.NewServer(testConfig(usage.URL), db, func() time.Time { return apiNow }))
	defer server.Close()
	logs := captureLog(t)

	response, err := http.Post(server.URL+"/v1/providers/codex-main/refresh", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	want := `fetch usage for provider "codex-main" (usage_source "auto"): openusage usage source: `
	if response.StatusCode != http.StatusInternalServerError || !strings.Contains(body.Error, want) ||
		!strings.Contains(body.Error, "OpenUsage returned HTTP 503") {
		t.Fatalf("status=%d error=%q, want it to contain %q", response.StatusCode, body.Error, want)
	}
	if !strings.Contains(logs.String(), fmt.Sprintf("redline api POST /v1/providers/codex-main/refresh: responded 500: %s", body.Error)) {
		t.Fatalf("log = %q, want the request and the same error the caller saw", logs.String())
	}
}
