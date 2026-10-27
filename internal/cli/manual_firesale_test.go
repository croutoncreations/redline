package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

func TestManualFiresaleCommands(t *testing.T) {
	for _, tc := range []struct {
		args                []string
		method, path, until string
	}{
		{[]string{"start", "--provider", "claude-main", "--for", "15m"}, "POST", "/v1/firesales", "15m"},
		{[]string{"start", "--provider", "claude-main", "--until-reset"}, "POST", "/v1/firesales", "weekly_reset"},
		{[]string{"stop", "--provider", "claude-main"}, "DELETE", "/v1/firesales/claude-main", ""},
		{[]string{"status"}, "GET", "/v1/firesales", ""},
	} {
		t.Run(tc.method+tc.until, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("request=%s %s", r.Method, r.URL.Path)
				}
				if r.Method == "POST" {
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					if body["account"] != "claude-main" || body["until"] != tc.until {
						t.Errorf("body=%v", body)
					}
				}
				if r.Method == "DELETE" {
					w.WriteHeader(204)
				} else {
					w.Write([]byte(`{"manual":[],"eligible":[]}`))
				}
			}))
			defer server.Close()
			var out, stderr bytes.Buffer
			args := append([]string{"--api", server.URL, "firesale"}, tc.args...)
			if code := cli.Run(args, &out, &stderr, time.Now); code != 0 || calls != 1 {
				t.Fatalf("code=%d calls=%d stderr=%s", code, calls, &stderr)
			}
			if tc.method != "DELETE" && !json.Valid(out.Bytes()) {
				t.Fatalf("not JSON: %s", &out)
			}
		})
	}
}

func TestManualFiresaleCommandValidation(t *testing.T) {
	for _, args := range [][]string{{}, {"start"}, {"start", "--provider", "main"}, {"start", "--provider", "main", "--for", "15m", "--until-reset"}, {"start", "--provider", "main", "--for", "4m"}, {"start", "--provider", "main", "--for", "169h"}, {"stop"}, {"status", "unexpected"}, {"stop", "--provider", "main", "--for", "15m"}} {
		var out, stderr bytes.Buffer
		if code := cli.Run(append([]string{"firesale"}, args...), &out, &stderr, time.Now); code == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}
