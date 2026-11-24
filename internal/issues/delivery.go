package issues

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	redprocess "github.com/croutoncreations/redline/internal/process"
	"github.com/croutoncreations/redline/internal/store"
)

func (s *Service) command(ctx context.Context, dir, name string, args ...string) (string, error) {
	if s.Command != nil {
		return s.Command(ctx, dir, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GH_PROMPT_DISABLED=1")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s failed; inspect repository access and local configuration", name, args[0])
	}
	return strings.TrimSpace(string(out)), nil
}
func remoteMatches(raw string, source domain.IssueSource) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "git@"+source.Host+":") {
		return strings.EqualFold(strings.TrimSuffix(strings.TrimPrefix(raw, "git@"+source.Host+":"), ".git"), source.Repository)
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), source.Host) || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme != "https" && u.Scheme != "ssh" {
		return false
	}
	if u.Scheme == "https" && u.User != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"), source.Repository)
}
func (s *Service) CheckRepository(ctx context.Context, source domain.IssueSource, p domain.ExecutionProfile) error {
	if p.WorkspaceProvider != "git-worktree" || p.Repository == "" || p.AgentContextID != "" {
		return fmt.Errorf("issue sources require a local git-worktree execution profile")
	}
	remote, err := s.command(ctx, p.Repository, "git", "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if !remoteMatches(remote, source) {
		return fmt.Errorf("profile origin must match the issue source repository")
	}
	pushURL, err := s.command(ctx, p.Repository, "git", "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return err
	}
	if !remoteMatches(pushURL, source) {
		return fmt.Errorf("profile push destination must match the issue source repository")
	}
	return nil
}
func readResult(path string) (string, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512*1024 {
		return "", "", fmt.Errorf("missing, oversized, or invalid issue result manifest")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var m struct {
		Summary string `json:"summary"`
		Outcome string `json:"outcome"`
	}
	if err = json.Unmarshal(data, &m); err != nil || strings.TrimSpace(m.Summary) == "" {
		return "", "", fmt.Errorf("issue result must contain a summary and supported outcome")
	}
	switch m.Outcome {
	case "changes_ready", "investigation_complete", "needs_clarification", "already_addressed", "no_change_needed":
		return m.Outcome, m.Summary, nil
	}
	return "", "", fmt.Errorf("unsupported issue result outcome")
}

// Finish executes before cleanup. The caller retains issue workspaces even when
// coding succeeded but verification or publication did not.
func (s *Service) Finish(ctx context.Context, run domain.Run, task domain.Task, p domain.ExecutionProfile, w domain.Workspace, result string, success bool) (string, string, []domain.RunArtifact, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.Store.IssueAttemptForTask(ctx, task.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "", "", nil, nil
	}
	if err != nil {
		return "", "", nil, err
	}
	a.Workspace = w
	a.UpdatedAt = s.now()
	if !success {
		a.State = "failed"
		a.Error = "Agent execution failed"
		return s.saveResult(ctx, a, nil)
	}
	outcome, summary, err := readResult(result)
	a.Summary = summary
	if err != nil {
		a.State = "needs_attention"
		a.Error = err.Error()
		return s.saveResult(ctx, a, err)
	}
	if outcome != "changes_ready" {
		if outcome == "investigation_complete" && a.Source.Goal != "investigation" {
			a.State = "needs_attention"
			a.Error = "Investigation result does not satisfy the configured delivery goal"
		} else {
			a.State = outcome
		}
		return s.saveResult(ctx, a, nil)
	}
	if a.Source.Goal == "investigation" {
		a.State = "needs_attention"
		a.Error = "Investigation jobs cannot publish code"
		return s.saveResult(ctx, a, nil)
	}
	if err = s.CheckRepository(ctx, a.Source, p); err == nil {
		err = s.checkWorkspace(ctx, a, p)
	}
	if err == nil && a.Source.VerifyCommand != "" {
		name, args := redprocess.ShellCommand(a.Source.VerifyCommand)
		_, err = s.command(ctx, w.Directory, name, args...)
	}
	if err != nil {
		a.State = "needs_attention"
		a.Error = "Verification failed: " + err.Error()
		return s.saveResult(ctx, a, err)
	}
	commit, commitErr := s.command(ctx, w.Directory, "git", "rev-parse", "HEAD")
	admitted, runErr := s.Store.GetRun(ctx, a.RunID)
	if commitErr != nil || runErr != nil || !shaPattern.MatchString(commit) || commit == admitted.SourceRevision {
		a.State = "needs_attention"
		a.Error = "No new commit was produced"
		return s.saveResult(ctx, a, nil)
	}
	a.Commit = commit
	if _, err = s.command(ctx, w.Directory, "git", "merge-base", "--is-ancestor", admitted.SourceRevision, commit); err != nil {
		a.State = "needs_attention"
		a.Error = "Implementation is not based on the admitted revision"
		return s.saveResult(ctx, a, err)
	}
	if err = s.checkWorkspace(ctx, a, p); err != nil {
		a.State = "needs_attention"
		a.Error = err.Error()
		return s.saveResult(ctx, a, err)
	}
	if a.Source.Goal == "local_changes" {
		a.State = "local_changes"
		return s.saveResult(ctx, a, nil)
	}
	err = s.publish(ctx, &a, a.Profile)
	return s.saveResult(ctx, a, err)
}
func (s *Service) saveResult(ctx context.Context, a domain.IssueAttempt, deliveryErr error) (string, string, []domain.RunArtifact, error) {
	a.UpdatedAt = s.now()
	if err := s.Store.SaveIssueAttempt(ctx, a); err != nil {
		return "", "", nil, err
	}
	if err := s.Store.RefreshIssueRunDelivery(ctx, a); err != nil {
		return "", "", nil, err
	}
	artifacts := []domain.RunArtifact{}
	if a.PRURL != "" {
		artifacts = append(artifacts, domain.RunArtifact{Type: "pull_request", Label: "Draft PR", URL: a.PRURL})
	}
	if deliveryErr == nil && a.Error != "" {
		deliveryErr = fmt.Errorf("%s", a.Error)
	}
	summary := a.Summary
	if summary == "" {
		summary = "Issue attempt: " + a.State
	}
	return a.State, summary, artifacts, deliveryErr
}
func (s *Service) checkWorkspace(ctx context.Context, a domain.IssueAttempt, p domain.ExecutionProfile) error {
	// The workspace must be the exact local worktree Redline prepared for this run.
	expected := filepath.Join(p.Repository, ".redline", "worktrees", a.RunID)
	directory, err := filepath.Abs(a.Workspace.Directory)
	if err != nil {
		return err
	}
	expected, err = filepath.Abs(expected)
	if err != nil {
		return err
	}
	actual, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fmt.Errorf("workspace is unavailable")
	}
	expected, err = filepath.EvalSymlinks(expected)
	if err != nil || actual != expected {
		return fmt.Errorf("workspace does not match the issue run")
	}
	branch, err := s.command(ctx, actual, "git", "symbolic-ref", "--short", "HEAD")
	if err != nil || branch != a.Workspace.Branch || !strings.HasPrefix(branch, "redline/") {
		return fmt.Errorf("issue workspace branch changed")
	}
	status, err := s.command(ctx, actual, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("issue changes must be committed before delivery")
	}
	head, err := s.command(ctx, actual, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if a.Commit != "" && head != a.Commit {
		return fmt.Errorf("workspace commit changed since publication intent")
	}
	return nil
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

func (s *Service) publish(ctx context.Context, a *domain.IssueAttempt, p domain.ExecutionProfile) error {
	fail := func(err error) error { a.State = "delivery_failed"; a.Error = err.Error(); return err }
	current, err := s.Store.IssueSource(ctx, a.Source.ID)
	if err != nil {
		return fail(err)
	}
	a.Source.Auth = current.Auth
	a.Source.Account = current.Account
	a.Source.CredentialRef = current.CredentialRef
	issue, err := s.provider().Get(ctx, a.Source, a.Snapshot.Number)
	if err != nil {
		return fail(err)
	}
	if issue.ID != a.Snapshot.ID {
		return fail(fmt.Errorf("issue identity changed"))
	}
	if why := Eligible(current, issue); why != "" {
		return fail(fmt.Errorf("publication held: %s", why))
	}
	if issue.Body != a.Snapshot.Body || issue.Title != a.Snapshot.Title {
		return fail(fmt.Errorf("publication held: issue requirements changed"))
	}
	if a.Source.Auth == "public" {
		return fail(fmt.Errorf("public mode cannot publish; retain local changes or connect authenticated source"))
	}
	if err = s.CheckRepository(ctx, a.Source, p); err != nil {
		return fail(err)
	}
	if err = s.checkWorkspace(ctx, *a, p); err != nil {
		return fail(err)
	}
	commit, err := s.command(ctx, a.Workspace.Directory, "git", "rev-parse", "HEAD")
	if err != nil || !shaPattern.MatchString(commit) {
		return fail(fmt.Errorf("cannot establish delivery commit"))
	}
	a.Commit = commit
	run, err := s.Store.GetRun(ctx, a.RunID)
	if err != nil {
		return fail(err)
	}
	if run.SourceRevision == commit {
		return fail(fmt.Errorf("issue run produced no new commit"))
	}
	if _, err = s.command(ctx, a.Workspace.Directory, "git", "merge-base", "--is-ancestor", run.SourceRevision, commit); err != nil {
		return fail(fmt.Errorf("delivery commit is not based on the admitted revision"))
	}
	a.State = "publishing"
	a.Error = ""
	a.UpdatedAt = s.now()
	// Intent is durable before either remote write. A timeout can be reconciled.
	if err = s.Store.SaveIssueAttempt(ctx, *a); err != nil {
		return err
	}
	existing, err := s.provider().Pull(ctx, a.Source, a.Workspace.Branch)
	if err != nil {
		return fail(err)
	}
	if existing != nil {
		return s.acceptPull(a, existing, p.BaseBranch)
	}
	if linked, e := s.provider().LinkedPull(ctx, a.Source, a.Snapshot.Number); e != nil {
		return fail(e)
	} else if linked != "" {
		return fail(fmt.Errorf("publication held: an open PR already references this issue: %s", linked))
	}
	if _, err = s.push(ctx, *a); err != nil {
		return fail(err)
	}
	base := p.BaseBranch
	body := fmt.Sprintf("Addresses %s\n\n%s\n\n<!-- redline-attempt:%s commit:%s -->", a.Snapshot.URL, a.Summary, a.ID, a.Commit)
	pull, err := s.provider().CreatePull(ctx, a.Source, a.Workspace.Branch, base, fmt.Sprintf("Address #%d: %s", a.Snapshot.Number, a.Snapshot.Title), body)
	if err != nil {
		return fail(err)
	}
	return s.acceptPull(a, pull, base)
}
func (s *Service) acceptPull(a *domain.IssueAttempt, p *Pull, base string) error {
	u, err := url.Parse(p.URL)
	if err != nil || u.Scheme != "https" || u.Host != a.Source.Host || !strings.HasPrefix(u.Path, "/"+a.Source.Repository+"/pull/") || p.Head.Ref != a.Workspace.Branch || p.Head.SHA != a.Commit || p.Base.Ref != base || !p.Draft || p.State != "open" {
		a.State = "delivery_failed"
		a.Error = "Existing PR does not match the recorded draft, branch, base, and commit"
		return fmt.Errorf("%s", a.Error)
	}
	a.State = "delivered"
	a.PRURL = p.URL
	a.Error = ""
	return nil
}
func (s *Service) authenticatedGit(ctx context.Context, source domain.IssueSource, dir string, args ...string) (string, error) {
	if s.Command != nil {
		return s.command(ctx, dir, "git", args...)
	}
	token, err := ResolveToken(ctx, source)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	if token != "" {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://"+source.Host+"/.extraheader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s failed; local work is preserved", args[0])
	}
	return strings.TrimSpace(string(out)), nil
}
func (s *Service) push(ctx context.Context, a domain.IssueAttempt) (string, error) {
	remote, err := s.command(ctx, a.Workspace.Directory, "git", "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return "", err
	}
	if !remoteMatches(remote, a.Source) {
		return "", fmt.Errorf("push destination changed; publication held")
	}
	return s.authenticatedGit(ctx, a.Source, a.Workspace.Directory, "push", remote, a.Commit+":refs/heads/"+a.Workspace.Branch)
}

func (s *Service) RetryDelivery(ctx context.Context, task string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.Store.IssueAttemptForTask(ctx, task)
	if err != nil {
		return err
	}
	if a.State != "delivery_failed" && a.State != "publishing" {
		return fmt.Errorf("%w: attempt is not awaiting publication", store.ErrConflict)
	}
	p := a.Profile
	// Re-run trusted verification because the retained workspace may have changed.
	if err = s.checkWorkspace(ctx, a, p); err != nil {
		return err
	}
	if a.Source.VerifyCommand != "" {
		name, args := redprocess.ShellCommand(a.Source.VerifyCommand)
		if _, err = s.command(ctx, a.Workspace.Directory, name, args...); err != nil {
			return err
		}
	}
	err = s.publish(ctx, &a, p)
	_, _, _, saved := s.saveResult(ctx, a, err)
	return saved
}
func (s *Service) Reconcile(ctx context.Context, a domain.IssueAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := a.Profile
	current, err := s.Store.IssueSource(ctx, a.Source.ID)
	if err != nil {
		return err
	}
	a.Source.Auth = current.Auth
	a.Source.Account = current.Account
	a.Source.CredentialRef = current.CredentialRef
	pull, err := s.provider().Pull(ctx, a.Source, a.Workspace.Branch)
	if err != nil {
		return err
	}
	if pull == nil {
		a.State = "delivery_failed"
		a.Error = "Publication interrupted; retry delivery to resume"
	} else {
		_ = s.acceptPull(&a, pull, p.BaseBranch)
	}
	_, _, _, err = s.saveResult(ctx, a, nil)
	return err
}
