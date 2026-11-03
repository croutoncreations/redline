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

func TestResetFiresaleStopDefaultsToActiveKeyOrNoop(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{true: "active", false: "no active"}[active], func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					if active {
						w.Write([]byte(`{"announcement":{"key":"announcement:new"},"firesale":{"active":true,"announcement_key":"announcement:active"}}`))
					} else {
						w.Write([]byte(`{"firesale":{"active":false}}`))
					}
					return
				}
				posts++
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				want := ""
				if active {
					want = "announcement:active"
				}
				if body["announcement_key"] != want || body["action"] != "stop" {
					t.Errorf("body=%v want key=%q", body, want)
				}
				w.Write([]byte(`{"active":false}`))
			}))
			defer server.Close()
			var out, err bytes.Buffer
			if code := cli.Run([]string{"--api", server.URL, "reset", "firesale", "stop"}, &out, &err, time.Now); code != 0 || posts != 1 {
				t.Fatalf("code=%d posts=%d err=%s", code, posts, &err)
			}
		})
	}
}

func TestResetFiresaleUsesCurrentOrExplicitAnnouncement(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		for _, explicit := range []bool{false, true} {
			t.Run(action+map[bool]string{true: " explicit", false: " current"}[explicit], func(t *testing.T) {
				gets, posts := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.Method + " " + r.URL.Path {
					case "GET /v1/reset-signals":
						gets++
						w.Write([]byte(`{"announcement":{"key":"announcement:123"},"firesale":{"active":true,"announcement_key":"announcement:123"}}`))
					case "POST /v1/reset-signals/firesale":
						posts++
						var body map[string]string
						json.NewDecoder(r.Body).Decode(&body)
						if body["action"] != action || body["announcement_key"] != "announcement:123" {
							t.Errorf("body=%v", body)
						}
						w.Write([]byte(`{"active":true}`))
					default:
						t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer server.Close()
				args := []string{"--api", server.URL, "reset", "firesale", action}
				if explicit {
					args = append(args, "--announcement", "announcement:123")
				}
				var out, err bytes.Buffer
				if code := cli.Run(args, &out, &err, time.Now); code != 0 || posts != 1 || gets != map[bool]int{true: 0, false: 1}[explicit] {
					t.Fatalf("code=%d gets=%d posts=%d err=%s", code, gets, posts, &err)
				}
			})
		}
	}
}
