package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/scheduler"
)

func TestStatusUsesJSONArrayBeforeFirstCycle(t *testing.T) {
	loop := scheduler.NewLoop(false, time.Minute, []string{"codex-main"}, func(context.Context, string) error { return nil })
	encoded, err := json.Marshal(loop.Status())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"providers":[]`) {
		t.Fatalf("status = %s", encoded)
	}
}

func TestCycleDispatchesProvidersInStableOrderAndContinuesAfterError(t *testing.T) {
	var called []string
	loop := scheduler.NewLoop(true, time.Minute, []string{"codex-main", "claude-main"},
		func(_ context.Context, provider string) error {
			called = append(called, provider)
			if provider == "claude-main" {
				return errors.New("usage unavailable")
			}
			return nil
		})
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	loop.RunCycle(context.Background(), now)
	if want := []string{"claude-main", "codex-main"}; !reflect.DeepEqual(called, want) {
		t.Fatalf("providers = %#v, want %#v", called, want)
	}
	status := loop.Status()
	if status.PollInterval != "1m0s" || status.LastCycleAt == nil || !status.LastCycleAt.Equal(now) || len(status.Providers) != 2 {
		t.Fatalf("status = %#v", status)
	}
	if status.Providers[0].Error != "usage unavailable" || status.Providers[1].Error != "" {
		t.Fatalf("provider status = %#v", status.Providers)
	}
}

func TestDisabledLoopDoesNotDispatch(t *testing.T) {
	called := false
	loop := scheduler.NewLoop(false, time.Minute, []string{"codex-main"}, func(context.Context, string) error {
		called = true
		return nil
	})
	loop.RunCycle(context.Background(), time.Now())
	if called || loop.Status().LastCycleAt != nil {
		t.Fatalf("disabled loop ran: called=%v status=%#v", called, loop.Status())
	}
}

func TestRunStartsImmediatelyAndStopsWithContext(t *testing.T) {
	dispatched := make(chan struct{}, 1)
	loop := scheduler.NewLoop(true, time.Hour, []string{"codex-main"}, func(context.Context, string) error {
		dispatched <- struct{}{}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		loop.Run(ctx)
		close(done)
	}()
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("initial cycle did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop")
	}
}
