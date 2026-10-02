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

// RunEvent is one step in a run's timeline.
type RunEvent struct {
	Type string `json:"type"`
	// Label is the type in words, because "workspace.prepare_started" is
	// machine vocabulary and this screen is read by a person.
	Label      string    `json:"label"`
	OccurredAt time.Time `json:"occurred_at"`
	// SinceStartLabel is how long after the run began this happened, which is
	// the reason to look at a timeline rather than a list of timestamps.
	SinceStartLabel string `json:"since_start_label,omitempty"`
}

// RunEventsView is a run's timeline.
type RunEventsView struct {
	Events []RunEvent `json:"events"`
}

// runEventLabels turns the service's event vocabulary into readable words.
//
// An unknown type falls back to itself rather than being dropped: the
// vocabulary grows server-side, and a step nobody has taught the app about is
// still better shown than hidden.
var runEventLabels = map[string]string{
	"run.started":               "Run started",
	"run.completed":             "Run completed",
	"run.failed":                "Run failed",
	"workspace.prepare_started": "Preparing workspace",
	"workspace.prepared":        "Workspace ready",
	"harness.started":           "Harness started",
	"harness.completed":         "Harness finished",
	"usage.recorded":            "Usage recorded",
	"finalize.started":          "Finalising",
	"finalize.completed":        "Finalised",
	"cleanup.started":           "Cleaning up",
	"cleanup.completed":         "Cleaned up",

	// The failure vocabulary matters most: on a failed run these are the
	// lines being looked for, so leaving them as raw identifiers would show
	// machine words at exactly the wrong moment.
	"harness.failed":           "Harness failed",
	"workspace.prepare_failed": "Workspace preparation failed",
	"finalize.failed":          "Finalising failed",
	"cleanup.failed":           "Cleanup failed",
}

// FetchRunEvents returns a run's timeline.
func (c *Client) FetchRunEvents(runID string) (string, error) {
	if strings.TrimSpace(runID) == "" {
		return "", errors.New("run id is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var records []struct {
		Type       string    `json:"type"`
		OccurredAt time.Time `json:"occurred_at"`
	}
	path := "/v1/runs/" + url.PathEscape(runID) + "/events"
	if err := c.get(ctx, path, &records); err != nil {
		return "", err
	}

	view := RunEventsView{Events: make([]RunEvent, 0, len(records))}
	var start time.Time
	for index, record := range records {
		if index == 0 {
			start = record.OccurredAt
		}
		label := runEventLabels[record.Type]
		if label == "" {
			label = record.Type
		}
		event := RunEvent{
			Type:       record.Type,
			Label:      label,
			OccurredAt: record.OccurredAt,
		}
		if index > 0 && !start.IsZero() && !record.OccurredAt.IsZero() {
			event.SinceStartLabel = durationLabel(record.OccurredAt.Sub(start))
		}
		view.Events = append(view.Events, event)
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("encode run events: %w", err)
	}
	return string(encoded), nil
}

// MarkRunRead clears the unread marker on one run.
func (c *Client) MarkRunRead(runID string) error {
	if strings.TrimSpace(runID) == "" {
		return errors.New("run id is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := c.postNoBody(ctx, "/v1/runs/"+url.PathEscape(runID)+"/read", nil)
	return err
}

// MarkAllRunsRead clears every unread marker.
func (c *Client) MarkAllRunsRead() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := c.postNoBody(ctx, "/v1/runs/read-all", nil)
	return err
}

// HealthView answers whether the scheduler is actually working.
type HealthView struct {
	Status   string `json:"status"`
	Degraded bool   `json:"degraded"`
	// Detail explains the status rather than making the reader go looking.
	Detail     string `json:"detail,omitempty"`
	ActiveRuns int    `json:"active_runs"`
}

// healthPayload mirrors the service's operational health record.
type healthPayload struct {
	Status               string `json:"status"`
	ActiveRuns           int    `json:"active_runs"`
	FailedRuns           int    `json:"failed_runs"`
	DispatchErrors       int    `json:"dispatch_errors"`
	NotificationFailures int    `json:"notification_failures"`
}

// render summarises health for display.
//
// Shared by FetchHealth and the capacity view so the pill cannot say one thing
// on one screen and something else on another.
func (h healthPayload) render() HealthView {
	view := HealthView{
		Status: h.Status,
		// Anything the service does not call healthy is worth surfacing, even
		// a status this build has not seen before.
		Degraded:   h.Status != "" && h.Status != "healthy",
		ActiveRuns: h.ActiveRuns,
	}

	parts := make([]string, 0, 3)
	if h.FailedRuns > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", h.FailedRuns))
	}
	if h.DispatchErrors > 0 {
		parts = append(parts, fmt.Sprintf("%d dispatch errors", h.DispatchErrors))
	}
	if h.NotificationFailures > 0 {
		parts = append(parts, fmt.Sprintf("%d notification failures", h.NotificationFailures))
	}
	view.Detail = strings.Join(parts, " \u00b7 ")
	return view
}

// FetchHealth returns the scheduler's operational health.
func (c *Client) FetchHealth() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var payload struct {
		Health healthPayload `json:"health"`
	}
	// Only the health member is needed, so only that is requested.
	if err := c.get(ctx, "/v1/dashboard?fields=health", &payload); err != nil {
		return "", err
	}

	encoded, err := json.Marshal(payload.Health.render())
	if err != nil {
		return "", fmt.Errorf("encode health: %w", err)
	}
	return string(encoded), nil
}
