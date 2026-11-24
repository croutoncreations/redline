package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

type fakeProvider struct {
	issue   domain.IssueSnapshot
	list    []domain.IssueSnapshot
	err     error
	linked  string
	pull    *Pull
	creates int
	timeout bool
}

func (f *fakeProvider) List(context.Context, domain.IssueSource) ([]domain.IssueSnapshot, error) {
	if f.list != nil {
		return f.list, f.err
	}
	return []domain.IssueSnapshot{f.issue}, f.err
}
func (f *fakeProvider) Get(context.Context, domain.IssueSource, int) (domain.IssueSnapshot, error) {
	return f.issue, f.err
}
func (f *fakeProvider) LinkedPull(context.Context, domain.IssueSource, int) (string, error) {
	return f.linked, f.err
}
func (f *fakeProvider) Pull(context.Context, domain.IssueSource, string) (*Pull, error) {
	return f.pull, f.err
}
func (f *fakeProvider) CreatePull(_ context.Context, s domain.IssueSource, branch, base, title, body string) (*Pull, error) {
	f.creates++
	p := &Pull{URL: "https://github.com/owner/repo/pull/42", State: "open", Draft: true}
	p.Head.Ref = branch
	p.Base.Ref = base
	parts := strings.Split(body, "commit:")
	p.Head.SHA = strings.TrimSpace(strings.TrimSuffix(parts[1], "-->"))
	f.pull = p
	if f.timeout {
		return nil, errors.New("response lost")
	}
	return p, nil
}
func fixture(t *testing.T) (*Service, *fakeProvider, domain.IssueSource, domain.ExecutionProfile) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p := domain.ExecutionProfile{ID: "profile", ProviderAccountID: "provider", HarnessType: "command", WorkspaceProvider: "git-worktree", Repository: t.TempDir(), BaseBranch: "main"}
	if err = db.CreateProfile(t.Context(), p, time.Now()); err != nil {
		t.Fatal(err)
	}
	source := domain.IssueSource{ID: "backlog", Repository: "owner/repo", ExecutionProfileID: p.ID, Enabled: true, Priority: 50}
	Defaults(&source)
	if err = db.CreateIssueSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{issue: domain.IssueSnapshot{ID: 100, Number: 1, URL: "https://github.com/owner/repo/issues/1", Title: "Fix a bug", Body: "Requirements", State: "open", Labels: []string{"redline"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}}
	s := &Service{Store: db, Provider: f}
	p, err = db.GetProfile(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, f, source, p
}
func attempts(t *testing.T, s *Service) []domain.IssueAttempt {
	t.Helper()
	items, e := s.Store.IssueItems(t.Context(), "")
	if e != nil {
		t.Fatal(e)
	}
	if len(items) != 1 {
		t.Fatalf("items=%+v", items)
	}
	return items[0].Attempts
}
func queue(t *testing.T, s *Service, source domain.IssueSource, f *fakeProvider) domain.IssueAttempt {
	t.Helper()
	if _, e := s.Sync(t.Context(), source.ID, true); e != nil {
		t.Fatal(e)
	}
	return attempts(t, s)[0]
}

func TestSyncDeduplicatesAcrossSourcesAndRestarts(t *testing.T) {
	s, f, source, _ := fixture(t)
	a := queue(t, s, source, f)
	other := source
	other.ID = "overlap"
	if e := s.Store.CreateIssueSource(t.Context(), other); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for n := 0; n < 5; n++ {
		wg.Go(func() {
			if _, e := s.Sync(t.Context(), other.ID, true); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	restarted := &Service{Store: s.Store, Provider: f}
	if _, e := restarted.Sync(t.Context(), source.ID, true); e != nil {
		t.Fatal(e)
	}
	got := attempts(t, s)
	if len(got) != 1 || got[0].TaskID != a.TaskID {
		t.Fatalf("attempts=%+v", got)
	}
	if e := s.Store.SetTaskControl(t.Context(), a.TaskID, "retry", time.Now()); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("generic retry bypass: %v", e)
	}
	task, e := s.Store.GetTask(t.Context(), a.TaskID)
	if e != nil || task.Type != domain.OneOff || !strings.Contains(task.Prompt, "external issue evidence") {
		t.Fatalf("task=%+v err=%v", task, e)
	}
}
func TestQueueBoundAndFilters(t *testing.T) {
	s, f, source, _ := fixture(t)
	source.MaxQueued = 1
	if e := s.Store.SaveIssueSource(t.Context(), source); e != nil {
		t.Fatal(e)
	}
	two := f.issue
	two.ID = 101
	two.Number = 2
	two.CreatedAt = two.CreatedAt.Add(time.Hour)
	f.list = []domain.IssueSnapshot{two, f.issue}
	queue(t, s, source, f)
	items, _ := s.Store.IssueItems(t.Context(), "")
	if len(items) != 1 || items[0].Snapshot.Number != 1 {
		t.Fatalf("oldest issue not queued: %+v", items)
	}
	i := f.issue
	i.Assignees = []string{"human"}
	if Eligible(source, i) != "Issue is assigned" {
		t.Fatal("assigned issue admitted")
	}
	i.Assignees = nil
	i.Labels = append(i.Labels, "redline:hold")
	if Eligible(source, i) == "" {
		t.Fatal("held issue admitted")
	}
}
func TestRevalidationWithdrawsChangedIssueAndFailsClosed(t *testing.T) {
	for _, kind := range []string{"closed", "label_removed", "body_changed", "outage", "paused", "existing_pr"} {
		t.Run(kind, func(t *testing.T) {
			s, f, source, p := fixture(t)
			a := queue(t, s, source, f)
			task, _ := s.Store.GetTask(t.Context(), a.TaskID)
			switch kind {
			case "closed":
				f.issue.State = "closed"
			case "label_removed":
				f.issue.Labels = nil
			case "body_changed":
				f.issue.Body = "new requirements"
			case "outage":
				f.err = errors.New("offline")
			case "paused":
				source.Enabled = false
				_ = s.Store.SaveIssueSource(t.Context(), source)
			case "existing_pr":
				f.linked = "https://github.com/owner/repo/pull/2"
			}
			ready, err := s.Revalidate(t.Context(), &task, &p)
			if ready {
				t.Fatal("ineligible issue admitted")
			}
			got := attempts(t, s)[0]
			if kind == "outage" || kind == "paused" {
				if err == nil || got.State != "queued" {
					t.Fatalf("state=%s err=%v", got.State, err)
				}
			} else if got.State != "withdrawn" {
				t.Fatalf("state=%s err=%v", got.State, err)
			}
		})
	}
}
func TestAdmissionAndExplicitRetryKeepSeparateAttempts(t *testing.T) {
	s, f, source, p := fixture(t)
	a := queue(t, s, source, f)
	if _, e := s.Store.AdmitTask(t.Context(), "run1", a.TaskID, p.ProviderAccountID, "base", time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Store.AdmitTask(t.Context(), "run2", a.TaskID, p.ProviderAccountID, "base", time.Now()); e == nil {
		t.Fatal("double admission")
	}
	if _, e := s.Retry(t.Context(), a.ItemID); e == nil {
		t.Fatal("retry active issue")
	}
	if e := s.Store.CompleteRun(t.Context(), "run1", domain.RunCompletion{State: domain.RunFailed, Error: "test failure"}, time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Sync(t.Context(), source.ID, true); e != nil {
		t.Fatal(e)
	}
	if len(attempts(t, s)) != 1 {
		t.Fatal("poll retried failed work")
	}
	if _, e := s.Retry(t.Context(), a.ItemID); e != nil {
		t.Fatal(e)
	}
	got := attempts(t, s)
	if len(got) != 2 || got[0].RunID != "run1" || got[1].State != "queued" {
		t.Fatalf("attempts=%+v", got)
	}
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s %v", args, out, e)
	}
	return strings.TrimSpace(string(out))
}
func publication(t *testing.T) (*Service, *fakeProvider, domain.IssueAttempt, domain.Run, domain.ExecutionProfile, string) {
	t.Helper()
	s, f, source, p := fixture(t)
	git(t, p.Repository, "init", "-b", "main")
	git(t, p.Repository, "config", "user.email", "test@example.com")
	git(t, p.Repository, "config", "user.name", "Test")
	if e := os.WriteFile(filepath.Join(p.Repository, "file"), []byte("before"), 0600); e != nil {
		t.Fatal(e)
	}
	git(t, p.Repository, "add", "file")
	git(t, p.Repository, "commit", "-m", "base")
	base := git(t, p.Repository, "rev-parse", "HEAD")
	git(t, p.Repository, "remote", "add", "origin", "https://github.com/owner/repo.git")
	a := queue(t, s, source, f)
	run, e := s.Store.AdmitTask(t.Context(), "run-1", a.TaskID, p.ProviderAccountID, base, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(p.Repository, ".redline", "worktrees", run.ID)
	git(t, p.Repository, "worktree", "add", "-b", "redline/"+run.ID, dir, "main")
	w := domain.Workspace{Directory: dir, Branch: "redline/" + run.ID}
	if e = s.Store.MarkRunRunning(t.Context(), run.ID, w); e != nil {
		t.Fatal(e)
	}
	run.Workspace = w
	if e = os.WriteFile(filepath.Join(dir, "file"), []byte("after"), 0600); e != nil {
		t.Fatal(e)
	}
	git(t, dir, "add", "file")
	git(t, dir, "commit", "-m", "fix")
	a = attempts(t, s)[0]
	a.Workspace = w
	manifest := filepath.Join(t.TempDir(), "result.json")
	if e = os.WriteFile(manifest, []byte(`{"outcome":"changes_ready","summary":"Fixed the bug; tests passed."}`), 0600); e != nil {
		t.Fatal(e)
	}
	// Only intercept push; all repository and commit validation uses real git.
	s.Command = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "git" && args[0] == "push" {
			return "", nil
		}
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		out, e := cmd.Output()
		return strings.TrimSpace(string(out)), e
	}
	return s, f, a, run, p, manifest
}
func TestPublicationTimeoutRetriesOnlyDelivery(t *testing.T) {
	s, f, a, run, p, manifest := publication(t)
	f.timeout = true
	task, _ := s.Store.GetTask(t.Context(), a.TaskID)
	outcome, _, _, err := s.Finish(t.Context(), run, task, p, run.Workspace, manifest, true)
	if err == nil || outcome != "delivery_failed" || f.creates != 1 {
		t.Fatalf("outcome=%s err=%v creates=%d", outcome, err, f.creates)
	}
	if e := s.Store.CompleteRun(t.Context(), run.ID, domain.RunCompletion{State: domain.RunCompleted, Outcome: outcome}, time.Now()); e != nil {
		t.Fatal(e)
	}
	if e := s.RetryDelivery(t.Context(), a.TaskID); e != nil {
		t.Fatal(e)
	}
	got := attempts(t, s)
	if len(got) != 1 || got[0].State != "delivered" || got[0].PRURL == "" || f.creates != 1 {
		t.Fatalf("attempts=%+v creates=%d", got, f.creates)
	}
}
func TestRestartReconcilesPublicationWithoutAnotherPR(t *testing.T) {
	s, f, a, run, p, manifest := publication(t)
	task, _ := s.Store.GetTask(t.Context(), a.TaskID)
	if _, _, _, e := s.Finish(t.Context(), run, task, p, run.Workspace, manifest, true); e != nil {
		t.Fatal(e)
	}
	a = attempts(t, s)[0]
	a.State = "publishing"
	if e := s.Store.SaveIssueAttempt(t.Context(), a); e != nil {
		t.Fatal(e)
	}
	if e := s.Store.RecoverInterruptedRuns(t.Context(), time.Now()); e != nil {
		t.Fatal(e)
	}
	restarted := &Service{Store: s.Store, Provider: f}
	if e := restarted.Reconcile(t.Context(), a); e != nil {
		t.Fatal(e)
	}
	if got := attempts(t, s)[0]; got.State != "delivered" || f.creates != 1 {
		t.Fatalf("state=%s creates=%d", got.State, f.creates)
	}
}
func TestDeliveryRefusesChangedRequirementsDirtyWorkAndWrongPR(t *testing.T) {
	for _, kind := range []string{"requirements", "dirty", "wrong_pr", "verify", "public", "invalid_result"} {
		t.Run(kind, func(t *testing.T) {
			s, f, a, run, p, manifest := publication(t)
			task, _ := s.Store.GetTask(t.Context(), a.TaskID)
			switch kind {
			case "requirements":
				f.issue.Body = "changed"
			case "dirty":
				_ = os.WriteFile(filepath.Join(run.Workspace.Directory, "file"), []byte("uncommitted"), 0600)
			case "wrong_pr":
				f.pull = &Pull{URL: "https://evil.invalid/pull/1", State: "open", Draft: true}
			case "verify":
				a.Source.VerifyCommand = "exit 1"
				_ = s.Store.SaveIssueAttempt(t.Context(), a)
			case "public":
				source, _ := s.Store.IssueSource(t.Context(), a.Source.ID)
				source.Auth = "public"
				_ = s.Store.SaveIssueSource(t.Context(), source)
			case "invalid_result":
				_ = os.WriteFile(manifest, []byte(`{"outcome":"completed"}`), 0600)
			}
			outcome, _, _, e := s.Finish(t.Context(), run, task, p, run.Workspace, manifest, true)
			if e == nil || outcome == "delivered" || f.creates != 0 {
				t.Fatalf("outcome=%s err=%v creates=%d", outcome, e, f.creates)
			}
			if _, e = os.Stat(run.Workspace.Directory); e != nil {
				t.Fatal("workspace lost")
			}
		})
	}
}
func TestNonCodeOutcomesNeverPublish(t *testing.T) {
	for _, outcome := range []string{"needs_clarification", "already_addressed", "no_change_needed"} {
		t.Run(outcome, func(t *testing.T) {
			s, f, a, run, p, path := publication(t)
			task, _ := s.Store.GetTask(t.Context(), a.TaskID)
			_ = os.WriteFile(path, []byte(fmt.Sprintf(`{"outcome":%q,"summary":"Explanation"}`, outcome)), 0600)
			got, _, _, err := s.Finish(t.Context(), run, task, p, run.Workspace, path, true)
			if err != nil || got != outcome || f.creates != 0 {
				t.Fatalf("got=%s err=%v", got, err)
			}
		})
	}
}
func TestGitHubPublicRESTPaginationAndFiltering(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" {
			t.Error("public request authenticated")
		}
		if r.URL.Query().Get("labels") != "redline" {
			t.Error("missing labels")
		}
		rows := []map[string]any{}
		if r.URL.Query().Get("page") == "1" {
			for n := 1; n <= 100; n++ {
				rows = append(rows, map[string]any{"id": n, "number": n, "state": "open", "pull_request": map[string]any{}})
			}
		} else {
			rows = append(rows, map[string]any{"id": 101, "number": 101, "state": "open", "title": "last"})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()
	g := GitHub{BaseURL: server.URL}
	source := domain.IssueSource{Auth: "public", Host: "github.com", Repository: "owner/repo", Labels: []string{"redline"}}
	rows, e := g.List(t.Context(), source)
	if e != nil || len(rows) != 1 || calls != 2 || rows[0].ID != 101 {
		t.Fatalf("rows=%+v calls=%d err=%v", rows, calls, e)
	}
}
func TestGitHubErrorsAndRedirectsDoNotLeakCredentials(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer destination.Close()
	for _, status := range []int{302, 403, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", destination.URL)
				w.WriteHeader(status)
				fmt.Fprint(w, "secret-token")
			}))
			defer server.Close()
			g := GitHub{BaseURL: server.URL, Token: func(context.Context, domain.IssueSource) (string, error) { return "secret-token", nil }}
			_, e := g.List(t.Context(), domain.IssueSource{Host: "github.com", Repository: "owner/repo"})
			if e == nil || strings.Contains(e.Error(), "secret-token") {
				t.Fatalf("err=%v", e)
			}
		})
	}
}

func TestLocalAndInvestigationGoals(t *testing.T) {
	for _, goal := range []string{"local_changes", "investigation"} {
		t.Run(goal, func(t *testing.T) {
			s, f, a, run, p, path := publication(t)
			a.Source.Goal = goal
			if e := s.Store.SaveIssueAttempt(t.Context(), a); e != nil {
				t.Fatal(e)
			}
			task, _ := s.Store.GetTask(t.Context(), a.TaskID)
			expected := "local_changes"
			if goal == "investigation" {
				expected = "investigation_complete"
				_ = os.WriteFile(path, []byte(`{"outcome":"investigation_complete","summary":"Found the cause; no changes needed."}`), 0600)
			}
			got, _, _, err := s.Finish(t.Context(), run, task, p, run.Workspace, path, true)
			if err != nil || got != expected || f.creates != 0 {
				t.Fatalf("got=%s err=%v creates=%d", got, err, f.creates)
			}
		})
	}
}
func TestPublicCredentialsCanBeUpgradedForDelivery(t *testing.T) {
	s, f, a, run, p, path := publication(t)
	source, _ := s.Store.IssueSource(t.Context(), a.Source.ID)
	source.Auth = "public"
	_ = s.Store.SaveIssueSource(t.Context(), source)
	task, _ := s.Store.GetTask(t.Context(), a.TaskID)
	if got, _, _, err := s.Finish(t.Context(), run, task, p, run.Workspace, path, true); err == nil || got != "delivery_failed" {
		t.Fatalf("got=%s err=%v", got, err)
	}
	_ = s.Store.CompleteRun(t.Context(), run.ID, domain.RunCompletion{State: domain.RunCompleted}, time.Now())
	source.Auth = "gh"
	_ = s.Store.SaveIssueSource(t.Context(), source)
	if e := s.RetryDelivery(t.Context(), a.TaskID); e != nil {
		t.Fatal(e)
	}
	stored, _ := s.Store.GetRun(t.Context(), run.ID)
	if f.creates != 1 || stored.Outcome != "delivered" || len(stored.Artifacts) != 1 {
		t.Fatalf("run=%+v creates=%d", stored, f.creates)
	}
}

func TestSyncHealthCannotUndoSourcePauseOrCredentialEdit(t *testing.T) {
	s, _, source, _ := fixture(t)
	stale := source
	source.Auth = "environment"
	source.CredentialRef = "NEW_TOKEN"
	source.Enabled = false
	if e := s.Store.UpdateIssueSourceConfig(t.Context(), source); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	stale.LastSyncedAt = &now
	if e := s.Store.RecordIssueSync(t.Context(), stale); e != nil {
		t.Fatal(e)
	}
	got, e := s.Store.IssueSource(t.Context(), source.ID)
	if e != nil || got.Enabled || got.Auth != "environment" || got.CredentialRef != "NEW_TOKEN" || got.LastSyncedAt == nil {
		t.Fatalf("source=%+v err=%v", got, e)
	}
}

func TestPublisherRefusesDifferentPushDestination(t *testing.T) {
	s, f, a, run, p, path := publication(t)
	git(t, p.Repository, "remote", "set-url", "--push", "origin", "https://github.com/other/repo.git")
	task, _ := s.Store.GetTask(t.Context(), a.TaskID)
	got, _, _, err := s.Finish(t.Context(), run, task, p, run.Workspace, path, true)
	if err == nil || got == "delivered" || f.creates != 0 {
		t.Fatalf("outcome=%s err=%v creates=%d", got, err, f.creates)
	}
}

func TestAdmissionUsesAnAttemptSpecificFetchedBase(t *testing.T) {
	s, f, source, p := fixture(t)
	a := queue(t, s, source, f)
	task, _ := s.Store.GetTask(t.Context(), a.TaskID)
	sha := strings.Repeat("a", 40)
	var fetch []string
	s.Command = func(_ context.Context, dir, name string, args ...string) (string, error) {
		switch args[0] {
		case "remote":
			return "https://github.com/owner/repo.git", nil
		case "fetch":
			fetch = args
			return "", nil
		case "rev-parse":
			if args[1] != "refs/redline/issue-bases/"+a.ID {
				t.Fatalf("shared base ref: %v", args)
			}
			return sha, nil
		case "check-ref-format":
			return "", nil
		}
		return "", fmt.Errorf("unexpected command %v", args)
	}
	ready, err := s.Revalidate(t.Context(), &task, &p)
	if err != nil || !ready || p.BaseBranch != sha || p.CleanupPolicy != "never" || len(fetch) == 0 || !strings.Contains(fetch[len(fetch)-1], a.ID) {
		t.Fatalf("ready=%v profile=%+v fetch=%v err=%v", ready, p, fetch, err)
	}
}
