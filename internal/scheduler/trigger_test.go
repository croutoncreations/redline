package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/scheduler"
)

func TestTriggerCoalescesAndRunsInLoopGoroutine(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	loop := scheduler.NewLoop(true, time.Hour, []string{"provider"}, func(ctx context.Context, _ string) error {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); loop.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("initial cycle missing")
	}
	for range 100 {
		loop.Trigger()
	}
	select {
	case <-entered:
		t.Fatal("trigger ran concurrently")
	default:
	}
	release <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("trigger did not wake loop")
	}
	release <- struct{}{}
	select {
	case <-entered:
		t.Fatal("triggers were not coalesced")
	case <-time.After(50 * time.Millisecond):
	}
	loop.Trigger()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("subsequent trigger missing")
	}
}

func TestDisabledLoopIgnoresTrigger(t *testing.T) {
	loop := scheduler.NewLoop(false, time.Hour, []string{"provider"}, func(context.Context, string) error { t.Error("disabled loop dispatched"); return nil })
	loop.Trigger()
	loop.Run(context.Background())
}
