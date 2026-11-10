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
)

func TestResetRedeemExitCodesBodyAndJSON(t *testing.T) {
	for outcome, exit := range map[string]int{"redeemed": 0, "dry_run_ok": 0, "nothing_to_reset": 3, "cooldown": 4, "unavailable": 4, "gone": 5, "failed": 1, "unknown": 1} {
		t.Run(outcome, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.EscapedPath() != "/v1/providers/team%2Fcodex/banked-resets/redeem" {
					t.Errorf("%s %s", r.Method, r.URL)
				}
				var body map[string]bool
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["dry_run"] {
					t.Errorf("%v %v", body, err)
				}
				code := "test"
				if outcome == "failed" {
					code = "ineligible" // Claude real redemptions fail with exit 1, not gone/5.
				}
				fmt.Fprintf(w, `{"outcome":%q,"code":%q,"reason":"reason"}`, outcome, code)
			}))
			defer server.Close()
			var out, errOut bytes.Buffer
			got := cli.RunWithInput([]string{"--api", server.URL, "reset", "redeem", "--provider", "team/codex", "--yes", "--json"}, strings.NewReader(""), &out, &errOut, time.Now)
			if got != exit || !strings.Contains(out.String(), `"outcome": "`+outcome+`"`) {
				t.Fatalf("exit=%d out=%s err=%s", got, &out, &errOut)
			}
		})
	}
}

func TestResetRedeemUnknownHumanOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"outcome":"unknown","reason":"check the provider"}`)
	}))
	defer server.Close()
	var out, errOut bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "reset", "redeem", "--provider", "main", "--yes"}, strings.NewReader(""), &out, &errOut, time.Now)
	if exit != 1 || !strings.Contains(out.String(), "outcome unknown") {
		t.Fatalf("exit=%d out=%s err=%s", exit, &out, &errOut)
	}
}

func TestResetRedeemReplayedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"outcome":"redeemed","replayed":true,"completed_at":"2026-10-22T12:00:00Z"}`)
	}))
	defer server.Close()
	var out, errOut bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "reset", "redeem", "--provider", "main", "--yes"}, strings.NewReader(""), &out, &errOut, time.Now)
	if exit != 0 || !strings.Contains(out.String(), "already redeemed at 2026-10-22T12:00:00Z") || strings.Contains(out.String(), "refill verified") {
		t.Fatalf("exit=%d out=%s err=%s", exit, &out, &errOut)
	}
}

func TestResetRedeemDryRunNeedsNoConfirmationAndNonTTYRefuses(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]bool
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body["dry_run"] {
			t.Error(body)
		}
		fmt.Fprint(w, `{"outcome":"dry_run_ok","reason":"authenticated"}`)
	}))
	defer server.Close()
	var out, errOut bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "reset", "redeem", "--provider", "main"}, strings.NewReader("y\n"), &out, &errOut, time.Now)
	if exit != 1 || calls != 0 || !strings.Contains(errOut.String(), "--yes") {
		t.Fatalf("%d %d %s", exit, calls, &errOut)
	}
	out.Reset()
	errOut.Reset()
	exit = cli.RunWithInput([]string{"--api", server.URL, "reset", "redeem", "--provider", "main", "--dry-run"}, strings.NewReader(""), &out, &errOut, time.Now)
	if exit != 0 || calls != 1 || !strings.Contains(out.String(), "dry_run_ok") {
		t.Fatalf("%d %d %s %s", exit, calls, &out, &errOut)
	}
}
