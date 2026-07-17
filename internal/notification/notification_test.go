package notification_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/notification"
	redprocess "github.com/jfox/redline/internal/process"
)

func TestCommandSinkSendsJSONOnStdinAndMetadataInEnvironment(t *testing.T) {
	runner := &captureNotificationRunner{}
	event := domain.NotificationEvent{
		Version: 1, Type: domain.EventRunCompleted, RunID: "run-1", TaskID: "task-1",
		ProviderAccountID: "codex-main", Message: "Run completed",
	}
	err := (notification.CommandSink{Command: "./notify", Runner: runner}).Deliver(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if runner.command.Name != "/bin/sh" || strings.Join(runner.command.Args, " ") != "-lc ./notify" ||
		!strings.Contains(runner.stdin, `"type":"run.completed"`) ||
		!containsNotificationEnv(runner.command.Env, "REDLINE_EVENT_TYPE=run.completed") {
		t.Fatalf("command=%#v stdin=%s", runner.command, runner.stdin)
	}
}

func TestServicePersistsFailedDelivery(t *testing.T) {
	store := &fakeNotificationStore{}
	service := notification.Service{
		Enabled: true, Events: map[string]bool{domain.EventRunFailed: true}, Store: store,
		Sink: failingSink{}, Timeout: time.Second,
		Now: func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) },
	}
	err := service.Notify(context.Background(), domain.NotificationEvent{Version: 1, Type: domain.EventRunFailed, Message: "failed"})
	if err == nil || store.status != "failed" || !strings.Contains(store.lastError, "offline") {
		t.Fatalf("err=%v store=%#v", err, store)
	}
}

func TestServiceIgnoresDisabledAndUnsubscribedEvents(t *testing.T) {
	store := &fakeNotificationStore{}
	service := notification.Service{Enabled: true, Events: map[string]bool{domain.EventRunFailed: true}, Store: store, Sink: failingSink{}}
	if err := service.Notify(context.Background(), domain.NotificationEvent{Type: domain.EventRunCompleted}); err != nil || store.created {
		t.Fatalf("err=%v created=%v", err, store.created)
	}
}

func TestServiceBoundsDeliveryWithTimeout(t *testing.T) {
	store := &fakeNotificationStore{}
	service := notification.Service{
		Enabled: true, Events: map[string]bool{domain.EventRunFailed: true}, Store: store,
		Sink: blockingSink{}, Timeout: 10 * time.Millisecond,
	}
	err := service.Notify(context.Background(), domain.NotificationEvent{Type: domain.EventRunFailed})
	if !errors.Is(err, context.DeadlineExceeded) || store.status != "failed" {
		t.Fatalf("err=%v store=%#v", err, store)
	}
}

type captureNotificationRunner struct {
	command redprocess.Command
	stdin   string
}

func (r *captureNotificationRunner) Run(_ context.Context, command redprocess.Command) (int, error) {
	r.command = command
	data, _ := io.ReadAll(command.Stdin)
	r.stdin = string(data)
	return 0, nil
}

type fakeNotificationStore struct {
	created   bool
	status    string
	lastError string
}

func (s *fakeNotificationStore) CreateNotificationDelivery(_ context.Context, _ string, payload json.RawMessage, _ time.Time) (int64, error) {
	s.created = len(payload) > 0
	return 1, nil
}

func (s *fakeNotificationStore) CompleteNotificationDelivery(_ context.Context, _ int64, status, lastError string, _ time.Time) error {
	s.status, s.lastError = status, lastError
	return nil
}

type failingSink struct{}

func (failingSink) Deliver(context.Context, domain.NotificationEvent) error {
	return errors.New("offline")
}

type blockingSink struct{}

func (blockingSink) Deliver(ctx context.Context, _ domain.NotificationEvent) error {
	<-ctx.Done()
	return ctx.Err()
}

func containsNotificationEnv(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
