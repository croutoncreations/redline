package usage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/usage"
)

func TestAutoSourceStaysOnOpenUsageAfterOneTransientFailure(t *testing.T) {
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	open := &fakeSource{name: "openusage", results: []sourceResult{{err: errors.New("offline")}, {snapshot: snapshot("openusage", now)}}}
	native := &fakeSource{name: "native", results: []sourceResult{{snapshot: snapshot("native", now)}}}
	manager := usage.NewManager(open, native, func() time.Time { return now })
	provider := config.Provider{Provider: "codex", UsageSource: "auto", OpenUsageURL: "http://127.0.0.1:6736"}

	if _, _, err := manager.Fetch(context.Background(), "codex-main", provider); err == nil {
		t.Fatal("first transient failure should fail closed")
	}
	got, _, err := manager.Fetch(context.Background(), "codex-main", provider)
	if err != nil || got.Source != "openusage" || native.calls != 0 {
		t.Fatalf("snapshot=%#v err=%v native_calls=%d", got, err, native.calls)
	}
}

func TestAutoSourceFallsBackAfterTwoFailuresAndReprobesLater(t *testing.T) {
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	open := &fakeSource{name: "openusage", results: []sourceResult{
		{err: errors.New("offline one")}, {err: errors.New("offline two")}, {snapshot: snapshot("openusage", now.Add(time.Hour))},
	}}
	native := &fakeSource{name: "native", results: []sourceResult{{snapshot: snapshot("native", now)}, {snapshot: snapshot("native", now)}}}
	manager := usage.NewManager(open, native, func() time.Time { return now })
	manager.ReprobeInterval = time.Hour
	manager.MaxSnapshotAge = 2 * time.Hour
	provider := config.Provider{Provider: "claude", UsageSource: "auto", OpenUsageURL: "http://127.0.0.1:6736"}

	_, _, _ = manager.Fetch(context.Background(), "claude-main", provider)
	got, _, err := manager.Fetch(context.Background(), "claude-main", provider)
	if err != nil || got.Source != "native" {
		t.Fatalf("fallback snapshot=%#v err=%v", got, err)
	}
	status := manager.Status("claude-main")
	if status.Active != "native" || status.LastError == "" || status.ChangedAt.IsZero() {
		t.Fatalf("status=%#v", status)
	}
	now = now.Add(30 * time.Minute)
	got, _, err = manager.Fetch(context.Background(), "claude-main", provider)
	if err != nil || got.Source != "native" || open.calls != 2 {
		t.Fatalf("sticky snapshot=%#v err=%v open_calls=%d", got, err, open.calls)
	}
	now = now.Add(31 * time.Minute)
	got, _, err = manager.Fetch(context.Background(), "claude-main", provider)
	if err != nil || got.Source != "openusage" || manager.Status("claude-main").Active != "openusage" {
		t.Fatalf("reprobe snapshot=%#v err=%v status=%#v", got, err, manager.Status("claude-main"))
	}
}

func TestAutoSourceImmediatelyFallsBackWhenOpenUsageSnapshotIsStale(t *testing.T) {
	now := time.Date(2026, 7, 24, 18, 30, 0, 0, time.UTC)
	open := &fakeSource{name: "openusage", results: []sourceResult{{snapshot: snapshot("openusage", now.Add(-time.Hour))}}}
	native := &fakeSource{
		name: "native", results: []sourceResult{{snapshot: snapshot("native", now.Add(time.Microsecond))}},
		onFetch: func() { now = now.Add(time.Microsecond) },
	}
	manager := usage.NewManager(open, native, func() time.Time { return now })
	manager.MaxSnapshotAge = 15 * time.Minute

	got, _, err := manager.Fetch(context.Background(), "claude-main", config.Provider{Provider: "claude", UsageSource: "auto"})
	if err != nil || got.Source != "native" || open.calls != 1 || native.calls != 1 {
		t.Fatalf("snapshot=%#v err=%v open_calls=%d native_calls=%d", got, err, open.calls, native.calls)
	}
	status := manager.Status("claude-main")
	if status.Active != "native" || !strings.Contains(status.LastError, "stale") {
		t.Fatalf("status=%#v", status)
	}
}

func TestExplicitNativeNeverCallsOpenUsage(t *testing.T) {
	now := time.Now()
	open := &fakeSource{name: "openusage"}
	native := &fakeSource{name: "native", results: []sourceResult{{snapshot: snapshot("native", now)}}}
	manager := usage.NewManager(open, native, func() time.Time { return now })
	got, _, err := manager.Fetch(context.Background(), "codex-main", config.Provider{Provider: "codex", UsageSource: "native"})
	if err != nil || got.Source != "native" || open.calls != 0 {
		t.Fatalf("snapshot=%#v err=%v open_calls=%d", got, err, open.calls)
	}
}

func TestExplicitOpenUsageNeverCallsNative(t *testing.T) {
	now := time.Now()
	open := &fakeSource{name: "openusage", results: []sourceResult{{snapshot: snapshot("openusage", now)}}}
	native := &fakeSource{name: "native"}
	manager := usage.NewManager(open, native, func() time.Time { return now })
	got, _, err := manager.Fetch(context.Background(), "codex-main", config.Provider{Provider: "codex", UsageSource: "openusage"})
	if err != nil || got.Source != "openusage" || native.calls != 0 {
		t.Fatalf("snapshot=%#v err=%v native_calls=%d", got, err, native.calls)
	}
}

func TestExplicitOpenUsageRejectsStaleSnapshotWithoutCallingNative(t *testing.T) {
	now := time.Date(2026, 7, 24, 18, 30, 0, 0, time.UTC)
	open := &fakeSource{name: "openusage", results: []sourceResult{{snapshot: snapshot("openusage", now.Add(-time.Hour))}}}
	native := &fakeSource{name: "native"}
	manager := usage.NewManager(open, native, func() time.Time { return now })
	manager.MaxSnapshotAge = 15 * time.Minute
	_, _, err := manager.Fetch(context.Background(), "claude-main", config.Provider{Provider: "claude", UsageSource: "openusage"})
	if err == nil || !strings.Contains(err.Error(), "stale") || native.calls != 0 {
		t.Fatalf("err=%v native_calls=%d", err, native.calls)
	}
}

func TestExplicitNativeFailureNeverCallsOpenUsage(t *testing.T) {
	now := time.Now()
	open := &fakeSource{name: "openusage"}
	native := &fakeSource{name: "native", results: []sourceResult{{err: errors.New("native error")}}}
	manager := usage.NewManager(open, native, func() time.Time { return now })
	_, _, err := manager.Fetch(context.Background(), "codex-main", config.Provider{Provider: "codex", UsageSource: "native"})
	if err == nil || open.calls != 0 {
		t.Fatalf("err=%v open_calls=%d (expected error and no open calls)", err, open.calls)
	}
}

type sourceResult struct {
	snapshot decision.UsageSnapshot
	raw      []byte
	err      error
}

type fakeSource struct {
	name    string
	results []sourceResult
	calls   int
	onFetch func()
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error) {
	if f.onFetch != nil {
		f.onFetch()
	}
	index := f.calls
	f.calls++
	if index >= len(f.results) {
		return decision.UsageSnapshot{}, nil, errors.New("unexpected fetch")
	}
	r := f.results[index]
	return r.snapshot, r.raw, r.err
}

func snapshot(source string, now time.Time) decision.UsageSnapshot {
	return decision.UsageSnapshot{Provider: "codex", ObservedAt: now, Source: source, Confidence: "high",
		Weekly: decision.UsageWindow{Remaining: .9, ResetsAt: now.Add(6 * 24 * time.Hour)}}
}
