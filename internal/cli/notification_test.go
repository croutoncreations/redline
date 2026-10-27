package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
	"github.com/croutoncreations/redline/internal/notification"
)

func TestNotificationTestCommand(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprint(jsonOutput), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.Method != "POST" || r.URL.Path != "/v1/notifications/test" || body["event"] != "firesale.started" || body["provider"] != "codex-main" {
					t.Errorf("%s %s %v", r.Method, r.URL, body)
				}
				fmt.Fprint(w, `{"id":1,"event_type":"firesale.started","status":"delivered","payload":{"title":"Test: Codex firesale started","message":"All job tiers unlocked."}}`)
			}))
			defer s.Close()
			args := []string{"--api", s.URL, "notification", "test", "--event", "firesale.started", "--provider", "codex-main"}
			if jsonOutput {
				args = append(args, "--json")
			}
			var out, errOut bytes.Buffer
			code := cli.RunWithInput(args, strings.NewReader(""), &out, &errOut, time.Now)
			if code != 0 || calls != 1 || !strings.Contains(out.String(), "delivered") || !strings.Contains(out.String(), "Test: Codex firesale started") {
				t.Fatalf("code=%d calls=%d out=%s err=%s", code, calls, &out, &errOut)
			}
		})
	}
}

func TestNotificationTestAll(t *testing.T) {
	var events []string
	var times []time.Time
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		events = append(events, body["event"])
		times = append(times, time.Now())
		fmt.Fprint(w, `{"id":1,"status":"delivered","payload":{"title":"Test","message":"Sample"}}`)
	}))
	defer s.Close()
	var out, errOut bytes.Buffer
	code := cli.RunWithInput([]string{"--api", s.URL, "notification", "test", "--event", "all", "--json"}, strings.NewReader(""), &out, &errOut, time.Now)
	if code != 0 || len(events) != len(notification.EventTypes()) || len(events) != 10 {
		t.Fatalf("code=%d events=%v err=%s", code, events, &errOut)
	}
	for i := 1; i < len(times); i++ {
		if times[i].Sub(times[i-1]) < 900*time.Millisecond {
			t.Fatal("samples not spaced apart")
		}
	}
	var deliveries []map[string]any
	if err := json.Unmarshal(out.Bytes(), &deliveries); err != nil || len(deliveries) != len(events) {
		t.Fatalf("out=%s err=%v", &out, err)
	}
}
