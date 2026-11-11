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

// QueueCandidate is one task in a provider's dispatch queue.
type QueueCandidate struct {
	TaskID   string `json:"task_id"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason,omitempty"`
	IsNextUp bool   `json:"is_next_up"`
}

// QueueView is the queue screen for one provider.
type QueueView struct {
	ProviderAccountID string `json:"provider_account_id"`

	SnapshotLabel  string `json:"snapshot_label,omitempty"`
	SnapshotStale  bool   `json:"snapshot_stale"`
	ProviderReason string `json:"provider_reason,omitempty"`

	DispatchAvailable bool `json:"dispatch_available"`

	NextUpTaskID string `json:"next_up_task_id,omitempty"`
	NextUpName   string `json:"next_up_name,omitempty"`

	ReadyCount   int `json:"ready_count"`
	BlockedCount int `json:"blocked_count"`

	Candidates []QueueCandidate `json:"candidates"`
}

// FetchQueue returns the dispatch queue for one provider.
//
// The service answers with the scheduler's own ordering, which this preserves:
// re-sorting here would show a different queue from the one that will actually
// run.
func (c *Client) FetchQueue(providerAccountID string) (string, error) {
	if strings.TrimSpace(providerAccountID) == "" {
		return "", errors.New("provider account id is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var payload struct {
		ProviderAccountID  string    `json:"provider_account_id"`
		SnapshotObservedAt time.Time `json:"snapshot_observed_at"`
		SnapshotStale      bool      `json:"snapshot_stale"`
		DispatchAvailable  bool      `json:"dispatch_available"`
		ProviderReason     string    `json:"provider_reason"`
		SelectedTaskID     string    `json:"selected_task_id"`
		Candidates         []struct {
			TaskID     string     `json:"task_id"`
			Name       string     `json:"name"`
			Priority   int        `json:"priority"`
			Eligible   bool       `json:"eligible"`
			Reason     string     `json:"reason"`
			EligibleAt *time.Time `json:"eligible_at"`
		} `json:"candidates"`
	}

	path := "/v1/providers/" + url.PathEscape(providerAccountID) + "/candidates"
	if err := c.get(ctx, path, &payload); err != nil {
		return "", err
	}

	now := c.now()
	view := QueueView{
		ProviderAccountID: payload.ProviderAccountID,
		SnapshotStale:     payload.SnapshotStale,
		ProviderReason:    payload.ProviderReason,
		DispatchAvailable: payload.DispatchAvailable,
		NextUpTaskID:      payload.SelectedTaskID,
		Candidates:        make([]QueueCandidate, 0, len(payload.Candidates)),
	}
	if !payload.SnapshotObservedAt.IsZero() {
		view.SnapshotLabel = relativeLabel(payload.SnapshotObservedAt, now)
	}

	for _, candidate := range payload.Candidates {
		entry := QueueCandidate{
			TaskID:   candidate.TaskID,
			Name:     candidate.Name,
			Priority: candidate.Priority,
			Eligible: candidate.Eligible,
			Reason:   queueReason(candidate.Reason, candidate.EligibleAt, now),
			IsNextUp: candidate.TaskID != "" && candidate.TaskID == payload.SelectedTaskID,
		}
		if entry.IsNextUp {
			view.NextUpName = candidate.Name
		}
		if candidate.Eligible {
			view.ReadyCount++
		} else {
			view.BlockedCount++
		}
		view.Candidates = append(view.Candidates, entry)
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("encode queue: %w", err)
	}
	return string(encoded), nil
}

// queueReason renders why a candidate is blocked.
//
// A cooldown arrives as a timestamp, which is turned into a wait: "cooldown
// until 2026-09-03T02:32:31Z" asks the reader to do UTC arithmetic on a phone.
// Older services send only the prose, so that is parsed as a fallback rather
// than losing the feature against a desktop that has not been updated.
func queueReason(reason string, eligibleAt *time.Time, now time.Time) string {
	if eligibleAt != nil && !eligibleAt.IsZero() {
		return cooldownLabel(eligibleAt.Sub(now))
	}
	return humaniseQueueReason(reason, now)
}

// cooldownLabel words a remaining cooldown.
func cooldownLabel(remaining time.Duration) string {
	if remaining <= 0 {
		// The cooldown has passed but the snapshot predates that, so the task
		// is about to become eligible rather than blocked for a negative time.
		return "cooldown just ended"
	}
	return "cooldown for " + durationLabel(remaining)
}

// cooldownPrefix is how the scheduler words a cooldown block.
const cooldownPrefix = "cooldown until "

// humaniseQueueReason rewrites a cooldown timestamp as a wait.
//
// "cooldown until 2026-09-03T03:03:49Z" requires the reader to work out how
// long that is, on a phone, probably in another timezone. Everything else is
// already an explanation and passes through untouched.
func humaniseQueueReason(reason string, now time.Time) string {
	trimmed := strings.TrimSpace(reason)
	if !strings.HasPrefix(trimmed, cooldownPrefix) {
		return trimmed
	}
	stamp := strings.TrimSpace(strings.TrimPrefix(trimmed, cooldownPrefix))
	until, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		// An unexpected format is still information; showing it beats hiding
		// the fact that the task is blocked.
		return trimmed
	}
	return cooldownLabel(until.Sub(now))
}

// providerControls are the actions the service accepts for a provider.
//
// Enumerated rather than interpolated: the control becomes part of the URL, so
// an unchecked value could address an unintended endpoint.
var providerControls = map[string]bool{"pause": true, "resume": true, "refresh": true}

// ControlProvider pauses, resumes, or refreshes a provider.
func (c *Client) ControlProvider(providerAccountID, control string) error {
	if strings.TrimSpace(providerAccountID) == "" {
		return errors.New("provider account id is required")
	}
	if !providerControls[control] {
		return fmt.Errorf("unsupported provider control %q", control)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	path := "/v1/providers/" + url.PathEscape(providerAccountID) + "/" + control
	_, err := c.postNoBody(ctx, path, nil)
	return err
}

// taskControls are the actions the service accepts for a task.
var taskControls = map[string]bool{"enable": true, "disable": true}

// ControlTask enables or disables a task.
func (c *Client) ControlTask(taskID, control string) error {
	if strings.TrimSpace(taskID) == "" {
		return errors.New("task id is required")
	}
	if !taskControls[control] {
		return fmt.Errorf("unsupported task control %q", control)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	path := "/v1/tasks/" + url.PathEscape(taskID) + "/" + control
	_, err := c.postNoBody(ctx, path, nil)
	return err
}
