package notification_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
	redprocess "github.com/croutoncreations/redline/internal/process"
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
	wantName, wantArgs := redprocess.ShellCommand("./notify")
	if runner.command.Name != wantName || strings.Join(runner.command.Args, " ") != strings.Join(wantArgs, " ") ||
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

func TestServiceStopsBeforeDeliveryWhenCreatingRecordFails(t *testing.T) {
	createErr := errors.New("database unavailable")
	store := &fakeNotificationStore{createErr: createErr}
	sink := &fakeNotificationSink{}
	service := notification.Service{
		Enabled: true, Events: map[string]bool{domain.EventRunFailed: true}, Store: store, Sink: sink,
	}

	err := service.Notify(context.Background(), domain.NotificationEvent{Type: domain.EventRunFailed})

	if !errors.Is(err, createErr) {
		t.Fatalf("Notify() error = %v, want create error", err)
	}
	if sink.calls != 0 {
		t.Fatalf("sink calls = %d, want 0", sink.calls)
	}
	if store.completed {
		t.Fatal("completed delivery record that was never created")
	}
}

func TestServiceReturnsCompletionFailureAfterSuccessfulDelivery(t *testing.T) {
	completionErr := errors.New("completion write failed")
	store := &fakeNotificationStore{completionErr: completionErr}
	sink := &fakeNotificationSink{}
	service := notification.Service{
		Enabled: true, Events: map[string]bool{domain.EventRunFailed: true}, Store: store, Sink: sink,
	}

	err := service.Notify(context.Background(), domain.NotificationEvent{Type: domain.EventRunFailed})

	if !errors.Is(err, completionErr) {
		t.Fatalf("Notify() error = %v, want completion error", err)
	}
	if sink.calls != 1 {
		t.Fatalf("sink calls = %d, want 1", sink.calls)
	}
	if !store.completed || store.status != "delivered" || store.lastError != "" {
		t.Fatalf("completion = %#v, want delivered without an error", store)
	}
}

func TestServicePreservesDeliveryAndCompletionFailures(t *testing.T) {
	deliveryErr := errors.New("sink offline")
	completionErr := errors.New("completion write failed")
	store := &fakeNotificationStore{completionErr: completionErr}
	sink := &fakeNotificationSink{err: deliveryErr}
	service := notification.Service{
		Enabled: true, Events: map[string]bool{domain.EventRunFailed: true}, Store: store, Sink: sink,
	}

	err := service.Notify(context.Background(), domain.NotificationEvent{Type: domain.EventRunFailed})

	if !errors.Is(err, deliveryErr) || !errors.Is(err, completionErr) {
		t.Fatalf("Notify() error = %v, want both delivery and completion errors", err)
	}
	if sink.calls != 1 {
		t.Fatalf("sink calls = %d, want 1", sink.calls)
	}
	if !store.completed || store.status != "failed" || store.lastError != deliveryErr.Error() {
		t.Fatalf("completion = %#v, want failed with delivery error", store)
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
	created       bool
	completed     bool
	status        string
	lastError     string
	createErr     error
	completionErr error
}

func (s *fakeNotificationStore) CreateNotificationDelivery(_ context.Context, _ string, payload json.RawMessage, _ time.Time) (int64, error) {
	s.created = len(payload) > 0
	return 1, s.createErr
}

func (s *fakeNotificationStore) CompleteNotificationDelivery(_ context.Context, _ int64, status, lastError string, _ time.Time) error {
	s.completed = true
	s.status, s.lastError = status, lastError
	return s.completionErr
}

type fakeNotificationSink struct {
	calls int
	err   error
}

func (s *fakeNotificationSink) Deliver(context.Context, domain.NotificationEvent) error {
	s.calls++
	return s.err
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
