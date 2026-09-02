package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// defaultRunLimit keeps the phone payload small. The runs screen is for
// spotting what just happened, not for browsing history.
const defaultRunLimit = 50

// RunSummary is one row on the runs screen.
//
// The service's run record carries filesystem paths, workspace details, and
// scheduler internals that a phone has no use for. This is the subset worth
// showing, with the interpretation already applied so each platform does not
// repeat it.
type RunSummary struct {
	ID       string `json:"id"`
	ShortID  string `json:"short_id"`
	TaskID   string `json:"task_id"`
	State    string `json:"state"`
	Outcome  string `json:"outcome,omitempty"`
	ExitCode int    `json:"exit_code"`

	Running   bool `json:"running"`
	Succeeded bool `json:"succeeded"`

	StartedAt     time.Time `json:"started_at"`
	RelativeLabel string    `json:"relative_label"`
	DurationLabel string    `json:"duration_label"`

	Summary        string `json:"summary,omitempty"`
	Error          string `json:"error,omitempty"`
	PullRequestURL string `json:"pull_request_url,omitempty"`
}

// RunListView is the runs screen.
type RunListView struct {
	Runs         []RunSummary `json:"runs"`
	TotalCount   int          `json:"total_count"`
	FailedCount  int          `json:"failed_count"`
	RunningCount int          `json:"running_count"`
}

// runRecord mirrors the fields of the service's run record that the phone uses.
type runRecord struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"task_id"`
	State       string     `json:"state"`
	Outcome     string     `json:"outcome"`
	ExitCode    int        `json:"exit_code"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	Summary     string     `json:"summary"`
	Error       string     `json:"error"`
	Artifacts   []struct {
		Type  string `json:"type"`
		Label string `json:"label"`
		URL   string `json:"url"`
	} `json:"artifacts"`
}

// FetchRuns returns the runs screen as JSON.
func (c *Client) FetchRuns() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var records []runRecord
	path := fmt.Sprintf("/v1/runs?limit=%d", defaultRunLimit)
	if err := c.get(ctx, path, &records); err != nil {
		return "", err
	}

	now := c.now()
	view := RunListView{Runs: make([]RunSummary, 0, len(records))}
	for _, record := range records {
		summary := summariseRun(record, now)
		view.Runs = append(view.Runs, summary)
		view.TotalCount++
		if summary.Running {
			view.RunningCount++
		} else if !summary.Succeeded {
			view.FailedCount++
		}
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("encode runs: %w", err)
	}
	return string(encoded), nil
}

func summariseRun(record runRecord, now time.Time) RunSummary {
	// A run with no completion time is still going. Treating the zero time as
	// an end would render a long-running job as having taken no time at all.
	running := record.CompletedAt == nil || record.CompletedAt.IsZero()
	end := now
	if !running {
		end = *record.CompletedAt
	}

	summary := RunSummary{
		ID:            record.ID,
		ShortID:       shortRunID(record.ID),
		TaskID:        record.TaskID,
		State:         record.State,
		Outcome:       record.Outcome,
		ExitCode:      record.ExitCode,
		Running:       running,
		Succeeded:     !running && isSuccessfulRun(record),
		StartedAt:     record.StartedAt,
		RelativeLabel: relativeLabel(record.StartedAt, now),
		DurationLabel: durationLabel(end.Sub(record.StartedAt)),
		Summary:       record.Summary,
		Error:         record.Error,
	}

	for _, artifact := range record.Artifacts {
		if artifact.Type == "pull_request" && artifact.URL != "" {
			summary.PullRequestURL = artifact.URL
			break
		}
	}
	return summary
}

// isSuccessfulRun trusts the service's own outcome rather than inferring
// success from the exit code, because a run can finish cleanly and still be
// recorded as failed by the finalize step.
func isSuccessfulRun(record runRecord) bool {
	outcome := record.Outcome
	if outcome == "" {
		outcome = record.State
	}
	return outcome == "completed" && record.ExitCode == 0
}

// shortRunID takes the leading segment of a UUID. Full run IDs are 36
// characters, which crowds out everything else on a phone-width row.
func shortRunID(id string) string {
	if index := strings.Index(id, "-"); index > 0 {
		return id[:index]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// durationLabel renders how long a run took, at the coarsest useful precision:
// seconds matter for a quick job, but nobody reads "1h 12m 09s".
func durationLabel(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		minutes := int(elapsed.Minutes())
		seconds := int(elapsed.Seconds()) % 60
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	default:
		hours := int(elapsed.Hours())
		minutes := int(elapsed.Minutes()) % 60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
}

// relativeLabel answers "when was this?" the way a person would.
func relativeLabel(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	elapsed := now.Sub(at)
	if elapsed < 0 {
		return "just now"
	}
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	default:
		days := int(elapsed.Hours() / 24)
		if days == 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// FetchRunLogs returns the text of one log stream for a run.
//
// Streams are stdout, stderr, and the prepare/finalize variants; an empty
// stream means stdout.
func (c *Client) FetchRunLogs(runID, stream string) (string, error) {
	if strings.TrimSpace(runID) == "" {
		return "", errors.New("run id is required")
	}
	if stream == "" {
		stream = "stdout"
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var payload struct {
		Content string `json:"content"`
	}
	path := fmt.Sprintf("/v1/runs/%s/logs?stream=%s", url.PathEscape(runID), url.QueryEscape(stream))
	if err := c.get(ctx, path, &payload); err != nil {
		return "", err
	}
	return renderLogs(payload.Content), nil
}

// renderLogs turns harness output into something readable on a phone.
//
// Agent harnesses emit JSONL transcripts. Rendering those raw fills the screen
// with escaped JSON whose useful content is a few sentences of prose and the
// names of the tools that ran. Lines that are not transcript JSON are passed
// through untouched, because plenty of runs emit ordinary output such as test
// results, and mangling those would lose the very thing being looked for.
func renderLogs(content string) string {
	lines := strings.Split(content, "\n")
	rendered := make([]string, 0, len(lines))

	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "{") {
			// The endpoint returns the tail of a file, so the first line is
			// often cut mid-JSON. Such a fragment is unreadable and would
			// otherwise be the first thing on screen.
			if index == 0 && looksLikeJSONFragment(trimmed) {
				continue
			}
			rendered = append(rendered, line)
			continue
		}

		var entry transcriptEntry
		if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
			// Not a transcript line after all; show it as it came.
			rendered = append(rendered, line)
			continue
		}
		if text := entry.render(); text != "" {
			rendered = append(rendered, text)
		}
	}

	return strings.Join(rendered, "\n")
}

// looksLikeJSONFragment reports whether a line is the tail end of a JSON object
// rather than ordinary output: it carries JSON punctuation without ever having
// opened a structure of its own.
func looksLikeJSONFragment(line string) bool {
	if strings.HasPrefix(line, "{") || strings.HasPrefix(line, "[") {
		return false
	}
	// Quoted keys followed by a colon are the giveaway. Plain command output
	// occasionally contains braces, but rarely this shape.
	return strings.Contains(line, `":`) && (strings.Contains(line, "}") || strings.Contains(line, "]"))
}

type transcriptEntry struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
}

// render reduces one transcript entry to the line worth showing, or "" for the
// entries that are pure bookkeeping.
func (e transcriptEntry) render() string {
	switch e.Type {
	case "assistant":
		parts := make([]string, 0, len(e.Message.Content))
		for _, block := range e.Message.Content {
			switch block.Type {
			case "text":
				if text := strings.TrimSpace(block.Text); text != "" {
					parts = append(parts, text)
				}
			case "tool_use":
				// That a command ran is worth knowing; its full arguments
				// are usually longer than the phone screen.
				if block.Name != "" {
					parts = append(parts, "· "+block.Name)
				}
			}
		}
		return strings.Join(parts, "\n")
	case "system":
		// Token counts and progress pings are noise here.
		if e.Subtype == "task_started" {
			return "— task started —"
		}
		return ""
	default:
		// Tool results and progress entries are the bulk of a transcript and
		// almost never the thing being looked for.
		return ""
	}
}

// DispatchView is the outcome of asking the service to run a task now.
//
// Dispatch has three distinct answers and they must not be collapsed: the run
// started, the scheduler considered it and held back, or the request was
// refused outright. Only the first means something is now happening.
type DispatchView struct {
	Started bool   `json:"started"`
	Refused bool   `json:"refused"`
	RunID   string `json:"run_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

// DispatchTask asks the service to run a task now.
//
// A refusal is an answer rather than a failure, so a held-back or rejected
// dispatch returns a populated view instead of an error. Only transport and
// auth problems are errors.
func (c *Client) DispatchTask(taskID string) (string, error) {
	if strings.TrimSpace(taskID) == "" {
		return "", errors.New("task id is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var payload struct {
		Run *struct {
			ID string `json:"id"`
		} `json:"run"`
		Result struct {
			Decision string `json:"decision"`
			Mode     string `json:"mode"`
			Reason   string `json:"reason"`
		} `json:"result"`
	}

	// The endpoint rejects any request body, so this sends none.
	status, err := c.postNoBody(ctx, "/v1/tasks/"+url.PathEscape(taskID)+"/dispatch", &payload)

	view := DispatchView{}
	switch {
	case err != nil:
		// A conflict carries the service's explanation of why it will not run,
		// which is exactly what the user needs to see.
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
			view.Refused = true
			view.Reason = apiErr.Message
			break
		}
		return "", err
	case status == 202:
		// 202 is the only status that means a run actually started.
		view.Started = true
		if payload.Run != nil {
			view.RunID = payload.Run.ID
		}
		view.Reason = payload.Result.Reason
		view.Mode = payload.Result.Mode
	default:
		// 200: the scheduler looked and decided not to start anything.
		view.Reason = payload.Result.Reason
		view.Mode = payload.Result.Mode
	}

	encoded, marshalErr := json.Marshal(view)
	if marshalErr != nil {
		return "", fmt.Errorf("encode dispatch result: %w", marshalErr)
	}
	return string(encoded), nil
}

// TaskSummary is one row on the tasks screen.
type TaskSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	State        string `json:"state"`
	Enabled      bool   `json:"enabled"`
	Dispatchable bool   `json:"dispatchable"`
}

// TaskListView is the tasks screen.
type TaskListView struct {
	Tasks []TaskSummary `json:"tasks"`
}

// FetchTasks returns the tasks screen as JSON.
func (c *Client) FetchTasks() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var records []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		State   string `json:"state"`
		Enabled bool   `json:"enabled"`
	}
	if err := c.get(ctx, "/v1/tasks", &records); err != nil {
		return "", err
	}

	view := TaskListView{Tasks: make([]TaskSummary, 0, len(records))}
	for _, record := range records {
		view.Tasks = append(view.Tasks, TaskSummary{
			ID:    record.ID,
			Name:  record.Name,
			Type:  record.Type,
			State: record.State,
			// Dispatch requires an enabled task in the queued state. Offering
			// the button otherwise guarantees a 409, so the UI needs to know
			// before the user taps.
			Enabled:      record.Enabled,
			Dispatchable: record.Enabled && record.State == "queued",
		})
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("encode tasks: %w", err)
	}
	return string(encoded), nil
}
