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

	"github.com/jfox/redline/internal/domain"
	redprocess "github.com/jfox/redline/internal/process"
)

type Store interface {
	CreateNotificationDelivery(context.Context, string, json.RawMessage, time.Time) (int64, error)
	CompleteNotificationDelivery(context.Context, int64, string, string, time.Time) error
}

type Sink interface {
	Deliver(context.Context, domain.NotificationEvent) error
}

type Service struct {
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
	if event.Version == 0 {
		event.Version = 1
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = s.now()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode notification event: %w", err)
	}
	deliveryID, err := s.Store.CreateNotificationDelivery(
		context.WithoutCancel(ctx), event.Type, payload, s.now(),
	)
	if err != nil {
		return err
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
	completionErr := s.Store.CompleteNotificationDelivery(
		context.WithoutCancel(ctx), deliveryID, status, lastError, s.now(),
	)
	if deliveryErr != nil && completionErr != nil {
		return errors.Join(deliveryErr, completionErr)
	}
	if deliveryErr != nil {
		return deliveryErr
	}
	return completionErr
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
		return fmt.Errorf("notification command is required")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode notification command payload: %w", err)
	}
	exitCode, err := s.runner().Run(ctx, redprocess.Command{
		Name: "/bin/sh", Args: []string{"-lc", s.Command}, Stdin: strings.NewReader(string(payload)),
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
