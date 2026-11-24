package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

type Provider interface {
	LinkedPull(context.Context, domain.IssueSource, int) (string, error)
	List(context.Context, domain.IssueSource) ([]domain.IssueSnapshot, error)
	Get(context.Context, domain.IssueSource, int) (domain.IssueSnapshot, error)
	Pull(context.Context, domain.IssueSource, string) (*Pull, error)
	CreatePull(context.Context, domain.IssueSource, string, string, string, string) (*Pull, error)
}
type Pull struct {
	URL  string `json:"html_url"`
	Head struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	State string `json:"state"`
	Draft bool   `json:"draft"`
}
type GitHub struct {
	Client  *http.Client
	BaseURL string
	Token   func(context.Context, domain.IssueSource) (string, error)
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func Defaults(s *domain.IssueSource) {
	if s.Host == "" {
		s.Host = "github.com"
	}
	s.Host = strings.ToLower(s.Host)
	if s.Auth == "" {
		s.Auth = "gh"
	}
	if s.Goal == "" {
		s.Goal = "draft_pr"
	}
	if len(s.Labels) == 0 {
		s.Labels = []string{"redline"}
	}
	if s.ExcludeLabels == nil {
		s.ExcludeLabels = []string{"redline:hold"}
	}
	if s.DispatchTier == "" {
		s.DispatchTier = domain.DispatchBehind
	}
	if s.MaxQueued == 0 {
		s.MaxQueued = 10
	}
}
func Validate(s domain.IssueSource) error {
	if s.ID == "" || len(s.ID) > 100 || !repoPattern.MatchString(s.Repository) || strings.Contains(s.Repository, "..") || !hostPattern.MatchString(s.Host) || !strings.Contains(s.Host, ".") {
		return fmt.Errorf("source ID, GitHub hostname, and owner/repository are required")
	}
	if s.ExecutionProfileID == "" {
		return fmt.Errorf("execution profile is required")
	}
	if s.Auth != "gh" && s.Auth != "public" && s.Auth != "environment" {
		return fmt.Errorf("authentication must be gh, public, or environment")
	}
	if s.Auth == "environment" && !envPattern.MatchString(s.CredentialRef) {
		return fmt.Errorf("credential_ref must name an environment variable")
	}
	if s.Goal != "draft_pr" && s.Goal != "local_changes" && s.Goal != "investigation" {
		return fmt.Errorf("unknown issue goal")
	}
	if s.MaxQueued < 1 || s.MaxQueued > 100 {
		return fmt.Errorf("max_queued must be between 1 and 100")
	}
	switch s.DispatchTier {
	case domain.DispatchBehind, domain.DispatchWellBehind, domain.DispatchExpiring, domain.DispatchFiresale:
	default:
		return fmt.Errorf("unknown dispatch tier")
	}
	for _, l := range append(append([]string{}, s.Labels...), s.ExcludeLabels...) {
		if strings.TrimSpace(l) == "" || len(l) > 100 {
			return fmt.Errorf("labels must be nonempty and at most 100 characters")
		}
	}
	return nil
}
func ResolveToken(ctx context.Context, s domain.IssueSource) (string, error) {
	switch s.Auth {
	case "public":
		return "", nil
	case "environment":
		if v := strings.TrimSpace(os.Getenv(s.CredentialRef)); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("GitHub credential environment variable is unavailable to the service")
	case "gh":
		args := []string{"auth", "token", "--hostname", s.Host}
		if s.Account != "" {
			args = append(args, "--user", s.Account)
		}
		out, err := exec.CommandContext(ctx, "gh", args...).Output()
		if err != nil || len(bytes.TrimSpace(out)) == 0 {
			return "", fmt.Errorf("GitHub login unavailable: run gh auth login --hostname %s as the service user", s.Host)
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("unknown GitHub authentication mode")
}
func (g GitHub) request(ctx context.Context, s domain.IssueSource, method, path string, body, out any) error {
	resolver := g.Token
	if resolver == nil {
		resolver = ResolveToken
	}
	token, err := resolver(ctx, s)
	if err != nil {
		return err
	}
	base := g.BaseURL
	if base == "" {
		base = "https://api.github.com"
		if s.Host != "github.com" {
			base = "https://" + s.Host + "/api/v3"
		}
	}
	var reader io.Reader
	if body != nil {
		data, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Redline-Issue-Sources")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fmt.Errorf("GitHub API redirects require source reconfiguration")
	}
	res, err := copyClient.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub returned HTTP %d; check access or rate limits", res.StatusCode)
	}
	if out != nil {
		if err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out); err != nil {
			return fmt.Errorf("invalid or oversized GitHub response")
		}
	}
	return nil
}

type rawIssue struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	URL       string    `json:"html_url"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	PR json.RawMessage `json:"pull_request"`
}

func (r rawIssue) snapshot() domain.IssueSnapshot {
	i := domain.IssueSnapshot{ID: r.ID, Number: r.Number, URL: r.URL, Title: r.Title, Body: r.Body, State: r.State, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Labels: []string{}, Assignees: []string{}}
	for _, l := range r.Labels {
		i.Labels = append(i.Labels, l.Name)
	}
	for _, a := range r.Assignees {
		i.Assignees = append(i.Assignees, a.Login)
	}
	return i
}
func (g GitHub) List(ctx context.Context, s domain.IssueSource) ([]domain.IssueSnapshot, error) {
	out := []domain.IssueSnapshot{}
	for page := 1; page <= 20; page++ {
		q := url.Values{"state": {"open"}, "sort": {"created"}, "direction": {"asc"}, "per_page": {"100"}, "page": {fmt.Sprint(page)}, "labels": {strings.Join(s.Labels, ",")}}
		var rows []rawIssue
		if err := g.request(ctx, s, "GET", "/repos/"+s.Repository+"/issues?"+q.Encode(), nil, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if len(r.PR) > 0 && string(r.PR) != "null" {
				continue
			}
			if r.ID <= 0 || r.Number <= 0 {
				return nil, fmt.Errorf("GitHub returned an incomplete issue")
			}
			out = append(out, r.snapshot())
		}
		if len(rows) < 100 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("issue list exceeded 2,000 entries; narrow source labels")
}
func (g GitHub) Get(ctx context.Context, s domain.IssueSource, n int) (domain.IssueSnapshot, error) {
	var r rawIssue
	err := g.request(ctx, s, "GET", fmt.Sprintf("/repos/%s/issues/%d", s.Repository, n), nil, &r)
	if err == nil && (r.ID <= 0 || r.Number != n || len(r.PR) > 0) {
		err = fmt.Errorf("GitHub returned an invalid issue")
	}
	return r.snapshot(), err
}
func (g GitHub) Pull(ctx context.Context, s domain.IssueSource, branch string) (*Pull, error) {
	q := url.Values{"state": {"all"}, "head": {strings.Split(s.Repository, "/")[0] + ":" + branch}, "per_page": {"100"}}
	var rows []Pull
	if err := g.request(ctx, s, "GET", "/repos/"+s.Repository+"/pulls?"+q.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	if len(rows) > 1 {
		return nil, fmt.Errorf("multiple pull requests for attempt branch require inspection")
	}
	if len(rows) == 1 {
		return &rows[0], nil
	}
	return nil, nil
}
func (g GitHub) CreatePull(ctx context.Context, s domain.IssueSource, branch, base, title, body string) (*Pull, error) {
	var p Pull
	err := g.request(ctx, s, "POST", "/repos/"+s.Repository+"/pulls", map[string]any{"head": branch, "base": base, "title": title, "body": body, "draft": true}, &p)
	return &p, err
}

// Cross references include PRs that GitHub has linked to this issue. Incomplete
// history is a read failure rather than evidence that nobody is working on it.
func (g GitHub) LinkedPull(ctx context.Context, s domain.IssueSource, n int) (string, error) {
	for page := 1; page <= 20; page++ {
		var events []struct {
			Event  string `json:"event"`
			Source struct {
				Issue struct {
					State string          `json:"state"`
					URL   string          `json:"html_url"`
					PR    json.RawMessage `json:"pull_request"`
				} `json:"issue"`
			} `json:"source"`
		}
		path := fmt.Sprintf("/repos/%s/issues/%d/timeline?per_page=100&page=%d", s.Repository, n, page)
		if err := g.request(ctx, s, "GET", path, nil, &events); err != nil {
			return "", err
		}
		for _, e := range events {
			if e.Event == "cross-referenced" && e.Source.Issue.State == "open" && len(e.Source.Issue.PR) > 0 && string(e.Source.Issue.PR) != "null" {
				return e.Source.Issue.URL, nil
			}
		}
		if len(events) < 100 {
			return "", nil
		}
	}
	return "", fmt.Errorf("issue history exceeded limit; inspect linked PRs manually")
}
