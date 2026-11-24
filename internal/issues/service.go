package issues

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

type Service struct {
	Store    *store.DB
	Provider Provider
	Now      func() time.Time
	Command  func(context.Context, string, string, ...string) (string, error)
	mu       sync.Mutex
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *Service) provider() Provider {
	if s.Provider != nil {
		return s.Provider
	}
	return GitHub{}
}
func (s *Service) IsIssue(ctx context.Context, task string) bool {
	_, err := s.Store.IssueAttemptForTask(ctx, task)
	return !errors.Is(err, store.ErrNotFound)
}
func (s *Service) Preview(ctx context.Context, source domain.IssueSource) ([]domain.IssueCandidate, error) {
	rows, err := s.provider().List(ctx, source)
	if err != nil {
		return nil, err
	}
	items, err := s.Store.IssueItems(ctx, "")
	if err != nil {
		return nil, err
	}
	known := map[int64]domain.IssueItem{}
	for _, i := range items {
		if i.Host == source.Host {
			known[i.Snapshot.ID] = i
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].Number < rows[j].Number
		}
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	out := []domain.IssueCandidate{}
	for _, i := range rows {
		c := domain.IssueCandidate{Issue: i, Reason: Eligible(source, i)}
		if item, ok := known[i.ID]; ok {
			c.ItemID = item.ID
			c.Reason = "Already tracked: " + item.State
		}
		c.Eligible = c.Reason == ""
		out = append(out, c)
	}
	return out, nil
}
func (s *Service) Sync(ctx context.Context, id string, force bool) ([]domain.IssueCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	source, err := s.Store.IssueSource(ctx, id)
	if err != nil {
		return nil, err
	}
	if !source.Enabled {
		return nil, fmt.Errorf("source is paused")
	}
	if !force && source.NextSyncAt != nil && s.now().Before(*source.NextSyncAt) {
		return nil, nil
	}
	rows, err := s.Preview(ctx, source)
	now := s.now()
	next := now.Add(5 * time.Minute)
	if source.Auth == "public" {
		next = now.Add(15 * time.Minute)
	}
	source.NextSyncAt = &next
	if err != nil {
		source.LastError = err.Error()
		backoff := now.Add(time.Hour)
		source.NextSyncAt = &backoff
		_ = s.Store.RecordIssueSync(ctx, source)
		return nil, err
	}
	source.LastSyncedAt = &now
	source.LastError = ""
	for _, c := range rows {
		// Existing records are refreshed without creating another attempt.
		if c.ItemID != "" {
			if _, err = s.Store.QueueIssue(ctx, source, c.Issue, "", "", now); err != nil {
				return nil, err
			}
			continue
		}
		if !c.Eligible {
			continue
		}
		if _, err = s.Store.QueueIssue(ctx, source, c.Issue, Prompt(source, c.Issue), "", now); err != nil {
			if errors.Is(err, store.ErrConflict) {
				break
			}
			return nil, err
		}
	}
	if err = s.Store.RecordIssueSync(ctx, source); err != nil {
		return nil, err
	}
	return rows, nil
}
func (s *Service) Run(ctx context.Context) {
	s.Poll(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Poll(ctx)
		}
	}
}
func (s *Service) Poll(ctx context.Context) {
	sources, err := s.Store.IssueSources(ctx)
	if err != nil {
		return
	}
	for _, source := range sources {
		if ctx.Err() != nil {
			return
		}
		if source.Enabled {
			bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
			_, _ = s.Sync(bounded, source.ID, false)
			cancel()
		}
	}
	// Reconcile interrupted publications by looking for the exact recorded commit.
	items, err := s.Store.IssueItems(ctx, "")
	if err != nil {
		return
	}
	for _, i := range items {
		for _, a := range i.Attempts {
			if a.State == "publishing" {
				bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
				_ = s.Reconcile(bounded, a)
				cancel()
			}
		}
	}
}

// Revalidate runs for all dispatch paths before admission; network failures hold
// this candidate, and withdrawal never silently authorizes a fresh attempt.
func (s *Service) Revalidate(ctx context.Context, task *domain.Task, profile *domain.ExecutionProfile) (bool, error) {
	a, err := s.Store.IssueAttemptForTask(ctx, task.ID)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if a.State != "queued" {
		return false, fmt.Errorf("issue attempt is %s", a.State)
	}
	current, err := s.Store.IssueSource(ctx, a.Source.ID)
	if err != nil {
		return false, err
	}
	a.Source.Auth = current.Auth
	a.Source.Account = current.Account
	a.Source.CredentialRef = current.CredentialRef
	reason := ""
	if !current.Enabled {
		return false, fmt.Errorf("source is paused")
	} else {
		issue, e := s.provider().Get(ctx, a.Source, a.Snapshot.Number)
		if e != nil {
			return false, e
		}
		if issue.ID != a.Snapshot.ID {
			return false, fmt.Errorf("issue identity changed")
		}
		reason = Eligible(current, issue)
		if reason == "" && (issue.Body != a.Snapshot.Body || issue.Title != a.Snapshot.Title) {
			reason = "Issue requirements changed; inspect and explicitly retry"
		}
	}
	if reason != "" {
		if err = s.Store.WithdrawIssue(ctx, a, reason, s.now()); err != nil {
			return false, err
		}
		return false, nil
	}
	if !reflect.DeepEqual(*profile, a.Profile) {
		return false, s.Store.WithdrawIssue(ctx, a, "Execution profile changed; inspect and explicitly retry the issue", s.now())
	}
	linked, linkedErr := s.provider().LinkedPull(ctx, a.Source, a.Snapshot.Number)
	if linkedErr != nil {
		return false, linkedErr
	}
	if linked != "" {
		return false, s.Store.WithdrawIssue(ctx, a, "Open PR already references this issue: "+linked, s.now())
	}
	if err = s.CheckRepository(ctx, a.Source, *profile); err != nil {
		return false, err
	}
	*profile = a.Profile
	// All issue work retains its checkout until delivery is verified or inspected.
	profile.CleanupPolicy = "never"
	profile.FinalizeCommand = ""
	{
		base := profile.BaseBranch
		if base == "" || base == "HEAD" {
			return false, fmt.Errorf("issue profile requires an explicit base branch")
		}
		if _, err = s.command(ctx, profile.Repository, "git", "check-ref-format", "--branch", base); err != nil {
			return false, fmt.Errorf("invalid base branch")
		}
		baseRef := "refs/redline/issue-bases/" + a.ID
		if _, err = s.authenticatedGit(ctx, a.Source, profile.Repository, "fetch", "--no-tags", "--no-write-fetch-head", "origin", "+refs/heads/"+base+":"+baseRef); err != nil {
			return false, err
		}
		sha, e := s.command(ctx, profile.Repository, "git", "rev-parse", baseRef)
		if e != nil {
			return false, e
		}
		if !shaPattern.MatchString(sha) {
			return false, fmt.Errorf("invalid fetched base commit")
		}
		profile.BaseBranch = sha
	}
	return true, nil
}
func (s *Service) Retry(ctx context.Context, itemID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.Store.IssueItems(ctx, "")
	if err != nil {
		return "", err
	}
	for _, i := range items {
		if i.ID != itemID {
			continue
		}
		source, e := s.Store.IssueSource(ctx, i.SourceID)
		if e != nil {
			return "", e
		}
		issue, e := s.provider().Get(ctx, source, i.Snapshot.Number)
		if e != nil {
			return "", e
		}
		if issue.ID != i.Snapshot.ID {
			return "", fmt.Errorf("issue identity changed")
		}
		if why := Eligible(source, issue); why != "" {
			return "", fmt.Errorf("%s", why)
		}
		if i.State == "interrupted" {
			for _, attempt := range i.Attempts {
				if attempt.State == "interrupted" {
					run, e := s.Store.GetRun(ctx, attempt.RunID)
					if e != nil {
						return "", e
					}
					if run.Workspace.Branch != "" {
						pr, e := s.provider().Pull(ctx, source, run.Workspace.Branch)
						if e != nil {
							return "", e
						}
						if pr != nil {
							return "", fmt.Errorf("interrupted run has a PR; inspect %s before retrying", pr.URL)
						}
					}
					attempt.State = "failed"
					attempt.Error = "Operator explicitly retried interrupted coding work; previous workspace preserved"
					if e = s.Store.SaveIssueAttempt(ctx, attempt); e != nil {
						return "", e
					}
				}
			}
		}
		return s.Store.QueueIssue(ctx, source, issue, Prompt(source, issue), itemID, s.now())
	}
	return "", store.ErrNotFound
}
