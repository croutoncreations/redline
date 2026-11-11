package api_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

// A profile directory decides which local login the service reads and which
// login runs use. Phones reach the API through Tailscale Serve and the relay,
// both on loopback, so the API refuses it outright; only the CLI on the Mac
// binds one.
func TestAPIRefusesClaudeProfileDirectories(t *testing.T) {
	server, _, accountsPath := accountsServer(t, "http://127.0.0.1:1")
	add := map[string]any{"id": "claude-alpha", "provider": "claude", "usage_source": "native", "claude_config_dir": "~/.claude-cc-alpha"}
	if status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", add); status != http.StatusBadRequest ||
		!strings.Contains(raw, "add-profile") {
		t.Fatalf("add = %d %s", status, raw)
	}
	card := map[string]any{"id": "claude-a", "provider": "claude", "openusage_card": "claude@ab12cd34"}
	if status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", card); status != http.StatusCreated {
		t.Fatalf("card account = %d %s", status, raw)
	}
	before, _ := os.ReadFile(accountsPath)
	edit := map[string]any{"claude_config_dir": "~/.claude-cc-alpha"}
	if status, _, raw := accountRequest(t, http.MethodPatch, server.URL+"/v1/accounts/claude-a", edit); status != http.StatusBadRequest {
		t.Fatalf("edit = %d %s", status, raw)
	}
	after, _ := os.ReadFile(accountsPath)
	if string(before) != string(after) {
		t.Fatal("a refused profile change rewrote the accounts file")
	}
}

// Profiles bound on the Mac are listed with their directory (home as "~"),
// read-only sign-in steps for that profile, and the native source.
func TestProfileAccountsAreListedWithTheirOwnSignInSteps(t *testing.T) {
	server, _, accountsPath := accountsServer(t, "http://127.0.0.1:1")
	home, _ := os.UserHomeDir()
	if err := config.WriteManagedAccounts(accountsPath, []config.ManagedAccount{{ID: "claude-alpha", Provider: "claude", Label: "Alpha",
		UsageSource: "native", ClaudeConfigDir: filepath.Join(home, ".claude-cc-alpha")}}); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(server.URL + "/v1/accounts")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Accounts []struct {
			ID, Label, Pending string
			UsageSource        string `json:"usage_source"`
			SignIn             string `json:"sign_in"`
			Dir                string `json:"claude_config_dir"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, account := range body.Accounts {
		if account.ID != "claude-alpha" {
			continue
		}
		if account.Dir != "~/.claude-cc-alpha" || account.Pending != "add" || account.UsageSource != "native" ||
			!strings.Contains(account.SignIn, "CLAUDE_CONFIG_DIR=~/.claude-cc-alpha claude auth login") ||
			!strings.Contains(account.SignIn, "never refreshes or copies") || strings.Contains(account.SignIn, home) {
			t.Fatalf("account = %#v", account)
		}
		return
	}
	t.Fatalf("profile account not listed: %#v", body.Accounts)
}
