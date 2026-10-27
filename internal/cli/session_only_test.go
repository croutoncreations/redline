package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
	"github.com/croutoncreations/redline/internal/config"
)

func TestAddProfileSessionOnlyFlags(t *testing.T) {
	home, configPath := profileHome(t)
	var out, errOut bytes.Buffer
	exit := cli.Run([]string{"--config", configPath, "account", "add-profile", "team", "--dir", "~/.claude-cc-kilo", "--weekly-policy", "session_only", "--session-reserve", "0.6"}, &out, &errOut, time.Now)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, errOut.String())
	}
	accounts, err := config.ReadManagedAccounts(filepath.Join(home, "accounts.yaml"))
	if err != nil || len(accounts) != 1 || accounts[0].WeeklyPolicy != "session_only" || accounts[0].SessionReserve == nil || *accounts[0].SessionReserve != .6 {
		t.Fatalf("accounts=%+v err=%v", accounts, err)
	}
}

func TestAccountSessionOnlyFlags(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"accounts":[{"id":"team","weekly_policy":"session_only","session_reserve":0.5}]}`))
	}))
	defer server.Close()
	var out, errOut bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "account", "edit", "team", "--weekly-policy", "session_only", "--session-reserve", "0", "--json"}, &out, &errOut, time.Now)
	if exit != 0 || body["weekly_policy"] != "session_only" || body["session_reserve"] != float64(0) {
		t.Fatalf("exit=%d body=%v stderr=%s", exit, body, errOut.String())
	}
	var response struct {
		Accounts []struct {
			WeeklyPolicy   string  `json:"weekly_policy"`
			SessionReserve float64 `json:"session_reserve"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || len(response.Accounts) != 1 || response.Accounts[0].WeeklyPolicy != "session_only" || response.Accounts[0].SessionReserve != .5 {
		t.Fatalf("response=%s err=%v", out.String(), err)
	}
}
