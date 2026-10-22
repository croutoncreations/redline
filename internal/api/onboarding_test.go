package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func TestUnauthenticatedDashboardExplainsLocalSignInWithoutExposingSecrets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("")
	cfg.APIToken = strings.Repeat("secret", 8)
	cfg.SourcePath = "/private/operator/config.yaml"
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	for _, tt := range []struct{ path, accept, content, want string }{
		{"/", "text/html", "text/html", "redline --api"},
		{"/", "application/json", "application/json", "authentication is required"},
		{"/v1/dashboard", "text/html", "application/json", "authentication is required"},
	} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+tt.path, nil)
		req.Header.Set("Accept", tt.accept)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized || !strings.Contains(res.Header.Get("Content-Type"), tt.content) || !strings.Contains(string(body), tt.want) || strings.Contains(string(body), cfg.APIToken) || strings.Contains(string(body), cfg.SourcePath) {
			t.Fatalf("unexpected unauthenticated response: status=%d content-type=%s", res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
}

func onboardingRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", output, err)
		}
	}
	return dir
}

func TestLocalWorktreeProfileRejectsInvalidRepositoryBeforeSaving(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	valid := onboardingRepository(t)
	notGit := t.TempDir()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ repo, branch, message string }{
		{filepath.Join(t.TempDir(), "missing"), "", "existing directory"},
		{file, "", "existing directory"},
		{notGit, "", "Git working tree"},
		{valid, "missing-branch", "existing commit"},
	} {
		body, _ := json.Marshal(map[string]any{"id": "invalid", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "git-worktree", "repository": tt.repo, "base_branch": tt.branch})
		res, err := http.Post(server.URL+"/v1/profiles", "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		response, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(response), tt.message) {
			t.Fatalf("status=%d response=%s", res.StatusCode, response)
		}
	}
	var profiles []domain.ExecutionProfile
	getJSON(t, server.URL+"/v1/profiles", &profiles)
	if len(profiles) != 0 {
		t.Fatal("invalid profile was saved")
	}
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{"id": "valid", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "git-worktree", "repository": valid})
	requestStatus(t, http.MethodPatch, server.URL+"/v1/profiles/valid", fmt.Sprintf(`{"repository":%q}`, notGit), http.StatusBadRequest)
	var profile domain.ExecutionProfile
	getJSON(t, server.URL+"/v1/profiles/valid", &profile)
	if profile.Repository != valid {
		t.Fatal("invalid update changed the stored profile")
	}
	// A valid correction and a linked worktree remain supported.
	requestStatus(t, http.MethodPatch, server.URL+"/v1/profiles/valid", `{"base_branch":"main"}`, http.StatusOK)
	linked := filepath.Join(t.TempDir(), "linked")
	if output, err := exec.Command("git", "-C", valid, "worktree", "add", "--detach", linked).CombinedOutput(); err != nil {
		t.Fatalf("linked worktree: %s: %v", output, err)
	}
	requestStatus(t, http.MethodPatch, server.URL+"/v1/profiles/valid", fmt.Sprintf(`{"repository":%q}`, linked), http.StatusOK)
}

func TestDashboardReportsLoadedServiceSetupMetadata(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("")
	cfg.SourcePath = filepath.Join(t.TempDir(), "My config.yaml")
	cfg.ListenAddress = "127.0.0.1:17436"
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	var data struct {
		ConfigPath    string `json:"config_path"`
		ListenAddress string `json:"listen_address"`
		HostOS        string `json:"host_os"`
	}
	getJSON(t, server.URL+"/v1/dashboard", &data)
	if data.ConfigPath != cfg.SourcePath || data.ListenAddress != cfg.ListenAddress || data.HostOS == "" {
		t.Fatalf("setup metadata = %#v", data)
	}
}
