package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/issues"
)

type issueProvider struct{ err error }

func (p issueProvider) List(context.Context, domain.IssueSource) ([]domain.IssueSnapshot, error) {
	return []domain.IssueSnapshot{{ID: 100, Number: 1, Title: "A bug", Body: "Fix it", State: "open", Labels: []string{"redline"}, CreatedAt: time.Now()}}, p.err
}
func (p issueProvider) Get(ctx context.Context, s domain.IssueSource, n int) (domain.IssueSnapshot, error) {
	rows, e := p.List(ctx, s)
	return rows[0], e
}
func (p issueProvider) LinkedPull(context.Context, domain.IssueSource, int) (string, error) {
	return "", p.err
}
func (p issueProvider) Pull(context.Context, domain.IssueSource, string) (*issues.Pull, error) {
	return nil, p.err
}
func (p issueProvider) CreatePull(context.Context, domain.IssueSource, string, string, string, string) (*issues.Pull, error) {
	return nil, errors.New("unexpected publication")
}
func issueAPIFixture(t *testing.T) *Server {
	t.Helper()
	s, db, _ := redemptionServer(t, "codex")
	profile := domain.ExecutionProfile{ID: "local", ProviderAccountID: "main", HarnessType: "codex-cli", WorkspaceProvider: "git-worktree", Repository: filepath.Join(t.TempDir(), "repo"), BaseBranch: "main"}
	if e := db.CreateProfile(t.Context(), profile, s.now()); e != nil {
		t.Fatal(e)
	}
	s.issues.Provider = issueProvider{}
	s.issues.Command = func(context.Context, string, string, ...string) (string, error) {
		return "https://github.com/owner/repo.git", nil
	}
	return s
}

const sourceBody = `{"id":"backlog","repository":"owner/repo","execution_profile_id":"local","auth":"public","enabled":true}`

func TestIssueSourceAPIAndHistory(t *testing.T) {
	s := issueAPIFixture(t)
	if w := manualAction(s, "POST", "/v1/issue-sources", sourceBody, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated mutation: %d", w.Code)
	}
	if w := manualAction(s, "POST", "/v1/issue-sources", sourceBody, "secret"); w.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", w.Code, w.Body)
	}
	if w := manualAction(s, "GET", "/v1/issue-sources/backlog/preview", "", "secret"); w.Code != 200 {
		t.Fatalf("preview=%d %s", w.Code, w.Body)
	}
	for range 2 {
		if w := manualAction(s, "POST", "/v1/issue-sources/backlog/sync", "", "secret"); w.Code != 200 {
			t.Fatalf("sync=%d %s", w.Code, w.Body)
		}
	}
	w := manualAction(s, "GET", "/v1/issues", "", "secret")
	var items []domain.IssueItem
	if e := json.Unmarshal(w.Body.Bytes(), &items); e != nil || len(items) != 1 || len(items[0].Attempts) != 1 {
		t.Fatalf("history=%s err=%v", w.Body, e)
	}
	if w := manualAction(s, "POST", "/v1/issues/"+items[0].ID+"/retry", "", "secret"); w.Code != 409 {
		t.Fatalf("active retry=%d", w.Code)
	}
	if w := manualAction(s, "POST", "/v1/issue-sources/backlog/disable", "", "secret"); w.Code != 200 {
		t.Fatalf("pause=%d %s", w.Code, w.Body)
	}
	a := items[0].Attempts[0]
	if _, e := s.store.AdmitTask(t.Context(), "run", a.TaskID, "main", "base", s.now()); e == nil {
		t.Fatal("paused source admitted at transactional boundary")
	}
}
func TestIssueSourceAPIRejectsUntrustedDefinitions(t *testing.T) {
	s := issueAPIFixture(t)
	for _, body := range []string{
		`{"id":"bad","host":"https://github.com","repository":"owner/repo","execution_profile_id":"local"}`,
		`{"id":"bad","repository":"../repo","execution_profile_id":"local"}`,
		`{"id":"bad","repository":"owner/repo","execution_profile_id":"local","auth":"environment","credential_ref":"literal token"}`,
		`{"id":"bad","repository":"owner/repo","execution_profile_id":"local","goal":"merge"}`,
	} {
		if w := manualAction(s, "POST", "/v1/issue-sources", body, "secret"); w.Code != 400 {
			t.Fatalf("accepted %s: %d %s", body, w.Code, w.Body)
		}
	}
}
func TestIssueSourceOutageDoesNotStarveOrdinaryJobs(t *testing.T) {
	s := issueAPIFixture(t)
	if w := manualAction(s, "POST", "/v1/issue-sources", sourceBody, "secret"); w.Code != 201 {
		t.Fatal(w.Body)
	}
	if _, e := s.issues.Sync(t.Context(), "backlog", true); e != nil {
		t.Fatal(e)
	}
	if e := s.store.CreateTask(t.Context(), domain.Task{ID: "ordinary", Name: "Ordinary", Prompt: "Do work", Priority: -100, ExecutionProfileID: "local", Type: domain.OneOff}, s.now()); e != nil {
		t.Fatal(e)
	}
	s.issues.Provider = issueProvider{err: errors.New("GitHub unavailable")}
	ctx := context.WithValue(t.Context(), issueDispatchKey{}, true)
	task, _, _, result, e := s.selectTask(ctx, "main", "base", decision.UsageSnapshot{Weekly: decision.UsageWindow{Remaining: 1}}, decision.Result{Decision: decision.Admit, UnlockedTier: domain.DispatchBehind}, "")
	if e != nil || task.ID != "ordinary" || len(result.CandidateRejections) != 1 {
		t.Fatalf("task=%s result=%+v err=%v", task.ID, result, e)
	}
}
