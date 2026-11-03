package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	redprocess "github.com/croutoncreations/redline/internal/process"
	"github.com/croutoncreations/redline/internal/push"
)

type Store interface {
	CreateNotificationDelivery(context.Context, string, json.RawMessage, time.Time) (int64, error)
	CompleteNotificationDelivery(context.Context, int64, string, string, time.Time) error
}

type Sink interface {
	Deliver(context.Context, domain.NotificationEvent) error
}

type Service struct {
	Push    *push.Dispatcher
	Enabled bool
	Events  map[string]bool
	Store   Store
	Sink    Sink
	Timeout time.Duration
	Now     func() time.Time
}

func (s Service) Notify(ctx context.Context, event domain.NotificationEvent) error {
	if !s.Enabled || !s.Events[event.Type] {
		return nil
	}
	_, err := s.deliver(ctx, event, false)
	return err
}

// Test bypasses subscriptions, but never the global enabled setting.
func (s Service) Test(ctx context.Context, event domain.NotificationEvent) (domain.NotificationDelivery, error) {
	if !s.Enabled {
		return domain.NotificationDelivery{}, fmt.Errorf("notifications are disabled; set notifications.enabled: true in your config")
	}
	return s.deliver(ctx, event, false)
}

// TestPush follows the ordinary delivery path, awaiting its content-free test wake.
func (s Service) TestPush(ctx context.Context, event domain.NotificationEvent) (domain.NotificationDelivery, error) {
	if !s.Enabled {
		return domain.NotificationDelivery{}, fmt.Errorf("notifications are disabled; set notifications.enabled: true in your config")
	}
	return s.deliver(ctx, event, true)
}

func (s Service) deliver(ctx context.Context, event domain.NotificationEvent, testPush bool) (domain.NotificationDelivery, error) {
	var record domain.NotificationDelivery
	if event.Version == 0 {
		event.Version = 1
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = s.now()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return record, fmt.Errorf("encode notification event: %w", err)
	}
	createdAt := s.now()
	deliveryID, err := s.Store.CreateNotificationDelivery(
		context.WithoutCancel(ctx), event.Type, payload, createdAt,
	)
	if err != nil {
		return record, err
	}
	var pushResult *domain.PushResult
	if s.Push != nil {
		kind := event.Type
		if testPush {
			kind = "test"
		}
		result := s.Push.Dispatch(context.WithoutCancel(ctx), deliveryID, kind, testPush)
		if testPush {
			pushResult = &result
		}
	} else if testPush {
		pushResult = &domain.PushResult{}
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	deliveryErr := s.Sink.Deliver(deliveryCtx, event)
	status := "delivered"
	lastError := ""
	if deliveryErr != nil {
		status = "failed"
		lastError = deliveryErr.Error()
	}
	updatedAt := s.now()
	completionErr := s.Store.CompleteNotificationDelivery(
		context.WithoutCancel(ctx), deliveryID, status, lastError, updatedAt,
	)
	if completionErr != nil {
		return record, errors.Join(deliveryErr, completionErr)
	}
	record = domain.NotificationDelivery{Push: pushResult, ID: deliveryID, EventType: event.Type, Status: status, Payload: payload, Attempts: 1, LastError: lastError, CreatedAt: createdAt, UpdatedAt: updatedAt}
	return record, deliveryErr
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type CommandSink struct {
	Command string
	Runner  redprocess.Runner
}

func (s CommandSink) Deliver(ctx context.Context, event domain.NotificationEvent) error {
	if strings.TrimSpace(s.Command) == "" {
		return nil
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode notification command payload: %w", err)
	}
	shellName, shellArgs := redprocess.ShellCommand(s.Command)
	exitCode, err := s.runner().Run(ctx, redprocess.Command{
		Name: shellName, Args: shellArgs, Stdin: strings.NewReader(string(payload)),
		Env: append(os.Environ(),
			"REDLINE_EVENT_TYPE="+event.Type,
			"REDLINE_PROVIDER_ACCOUNT_ID="+event.ProviderAccountID,
			"REDLINE_TASK_ID="+event.TaskID,
			"REDLINE_RUN_ID="+event.RunID,
		),
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		return fmt.Errorf("run notification command: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("notification command exited with code %d", exitCode)
	}
	return nil
}

func (s CommandSink) runner() redprocess.Runner {
	if s.Runner != nil {
		return s.Runner
	}
	return redprocess.ExecRunner{}
}
