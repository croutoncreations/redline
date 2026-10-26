package cli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

func TestResetSignalsReadsCachedAPI(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/v1/reset-signals" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"enabled":true,"last_poll_at":"2026-10-13T04:16:00Z","errors":[]}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "reset", "signals"}, &stdout, &stderr, time.Now)
	if exit != 0 || calls != 1 || !strings.Contains(stdout.String(), `"enabled": true`) {
		t.Fatalf("exit=%d calls=%d out=%s err=%s", exit, calls, &stdout, &stderr)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := cli.Run([]string{"--api", server.URL, "reset", "signals", "extra"}, &stdout, &stderr, time.Now); exit != 1 || calls != 1 {
		t.Fatalf("unexpected arguments accepted: %d %d", exit, calls)
	}
}
