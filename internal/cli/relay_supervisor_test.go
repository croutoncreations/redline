package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jfox/redline/internal/config"
)

type supervisorTestRuntime struct {
	initial config.ResolvedRelay
	updates chan config.ResolvedRelay

	mu            sync.Mutex
	subscriptions int
	cancellations int
}

func newSupervisorTestRuntime(initial config.ResolvedRelay) *supervisorTestRuntime {
	return &supervisorTestRuntime{initial: initial, updates: make(chan config.ResolvedRelay)}
}

func (r *supervisorTestRuntime) Current() config.ResolvedRelay { return r.initial }

func (r *supervisorTestRuntime) Subscribe() (<-chan config.ResolvedRelay, func()) {
	r.mu.Lock()
	r.subscriptions++
	r.mu.Unlock()
	go func() { r.updates <- r.initial }()
	var once sync.Once
	return r.updates, func() {
		once.Do(func() {
			r.mu.Lock()
			r.cancellations++
			r.mu.Unlock()
		})
	}
}

func (r *supervisorTestRuntime) send(snapshot config.ResolvedRelay) {
	r.updates <- snapshot
}

func (r *supervisorTestRuntime) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.subscriptions, r.cancellations
}

type recordedDialer struct {
	snapshot config.ResolvedRelay
	started  chan config.ResolvedRelay
	stopped  chan config.ResolvedRelay
}

func (d recordedDialer) run(ctx context.Context) {
	d.started <- d.snapshot
	<-ctx.Done()
	d.stopped <- d.snapshot
}

type recordingDialerFactory struct {
	started chan config.ResolvedRelay
	stopped chan config.ResolvedRelay

	mu           sync.Mutex
	snapshots    []config.ResolvedRelay
	tokenSources []func() string
}

func newRecordingDialerFactory() *recordingDialerFactory {
	return &recordingDialerFactory{
		started: make(chan config.ResolvedRelay, 20),
		stopped: make(chan config.ResolvedRelay, 20),
	}
}

func (f *recordingDialerFactory) new(snapshot config.ResolvedRelay, tokenSource func() string) (relayDialerRun, error) {
	f.mu.Lock()
	f.snapshots = append(f.snapshots, snapshot)
	f.tokenSources = append(f.tokenSources, tokenSource)
	f.mu.Unlock()
	dialer := recordedDialer{snapshot: snapshot, started: f.started, stopped: f.stopped}
	return dialer.run, nil
}

func (f *recordingDialerFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.snapshots)
}

func (f *recordingDialerFactory) latestToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenSources[len(f.tokenSources)-1]()
}

func selfHostedSnapshot(url, session string) config.ResolvedRelay {
	return config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{
			Mode:      config.RelayModeSelfHosted,
			URL:       url,
			SessionID: session,
		},
		Readiness: config.RelayReadinessSelfHosted,
		Dial:      true,
	}
}

func waitSnapshot(t *testing.T, events <-chan config.ResolvedRelay) config.ResolvedRelay {
	t.Helper()
	select {
	case snapshot := <-events:
		return snapshot
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay supervisor event")
		return config.ResolvedRelay{}
	}
}

func startSupervisorTest(t *testing.T, initial config.ResolvedRelay) (*supervisorTestRuntime, *recordingDialerFactory, context.CancelFunc, <-chan error) {
	t.Helper()
	runtime := newSupervisorTestRuntime(initial)
	factory := newRecordingDialerFactory()
	supervisor := newRelaySupervisor(runtime, factory.new)
	if supervisor.Initial() != initial {
		t.Fatalf("captured initial snapshot = %#v, want %#v", supervisor.Initial(), initial)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	return runtime, factory, cancel, done
}

func stopSupervisorTest(t *testing.T, runtime *supervisorTestRuntime, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("supervisor error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay supervisor did not stop")
	}
	if subscriptions, cancellations := runtime.counts(); subscriptions != 1 || cancellations != 1 {
		t.Fatalf("subscriptions=%d cancellations=%d, want one of each", subscriptions, cancellations)
	}
}

func TestRelaySupervisorStartsWhenRuntimeBecomesDialable(t *testing.T) {
	off := config.ResolvedRelay{RelayManagedState: config.RelayManagedState{Mode: config.RelayModeOff}, Readiness: config.RelayReadinessOff}
	runtime, factory, cancel, done := startSupervisorTest(t, off)
	dialable := selfHostedSnapshot("https://relay-one.example.com", "session-abcdefghij0123")
	runtime.send(dialable)
	if got := waitSnapshot(t, factory.started); got != dialable {
		t.Fatalf("dialer snapshot = %#v, want %#v", got, dialable)
	}
	stopSupervisorTest(t, runtime, cancel, done)
	if got := waitSnapshot(t, factory.stopped); got != dialable {
		t.Fatalf("stopped snapshot = %#v, want %#v", got, dialable)
	}
}

func TestRelaySupervisorReplacesConnectionForRoutingChangesButNotLiveTokenRefresh(t *testing.T) {
	initial := selfHostedSnapshot("https://relay-one.example.com", "session-abcdefghij0123")
	runtime, factory, cancel, done := startSupervisorTest(t, initial)
	if got := waitSnapshot(t, factory.started); got != initial {
		t.Fatalf("initial dialer snapshot = %#v", got)
	}

	urlChange := initial
	urlChange.URL = "https://relay-two.example.com"
	runtime.send(urlChange)
	_ = waitSnapshot(t, factory.stopped)
	_ = waitSnapshot(t, factory.started)

	sessionChange := urlChange
	sessionChange.SessionID = "session-zyxwvutsrqpo9876"
	runtime.send(sessionChange)
	_ = waitSnapshot(t, factory.stopped)
	_ = waitSnapshot(t, factory.started)

	refreshed := sessionChange
	refreshed.EntitlementToken = config.NewRelayEntitlementToken("token-two")
	runtime.send(refreshed)
	runtime.send(refreshed)
	if factory.count() != 3 {
		t.Fatalf("successful live refresh restarted socket: starts=%d", factory.count())
	}
	if got := factory.latestToken(); got != "token-two" {
		t.Fatalf("running dialer token source=%q want refreshed token", got)
	}

	refreshed.ReconnectGeneration++
	runtime.send(refreshed)
	_ = waitSnapshot(t, factory.stopped)
	_ = waitSnapshot(t, factory.started)
	stopSupervisorTest(t, runtime, cancel, done)
	_ = waitSnapshot(t, factory.stopped)
}

func TestRelaySupervisorStopsForNondialableReadiness(t *testing.T) {
	for _, readiness := range []config.RelayReadiness{
		config.RelayReadinessOff,
		config.RelayReadinessNeedsLicense,
		config.RelayReadinessUnavailable,
	} {
		t.Run(string(readiness), func(t *testing.T) {
			initial := selfHostedSnapshot("https://relay.example.com", "session-abcdefghij0123")
			runtime, factory, cancel, done := startSupervisorTest(t, initial)
			_ = waitSnapshot(t, factory.started)
			next := initial
			next.Readiness = readiness
			if readiness == config.RelayReadinessOff {
				next = config.ResolvedRelay{
					RelayManagedState: config.RelayManagedState{Mode: config.RelayModeOff},
					Readiness:         config.RelayReadinessOff,
				}
			}
			runtime.send(next)
			if got := waitSnapshot(t, factory.stopped); got != initial {
				t.Fatalf("stopped snapshot = %#v", got)
			}
			stopSupervisorTest(t, runtime, cancel, done)
			if factory.count() != 1 {
				t.Fatalf("dialer starts = %d, want 1", factory.count())
			}
		})
	}
}

func TestRelaySupervisorDoesNotRestartForPresentationOnlyOrNoopUpdates(t *testing.T) {
	initial := selfHostedSnapshot("https://relay.example.com", "session-abcdefghij0123")
	runtime, factory, cancel, done := startSupervisorTest(t, initial)
	_ = waitSnapshot(t, factory.started)

	presentation := initial
	presentation.Label = "work mac"
	runtime.send(presentation)
	presentation.Readiness = config.RelayReadinessActive
	presentation.EntitlementToken = config.NewRelayEntitlementToken("still-the-same-connection")
	runtime.send(presentation)
	// The second unbuffered send cannot complete until the first update has
	// been reconciled, making this negative assertion deterministic.
	if factory.count() != 1 {
		t.Fatalf("dialer starts = %d, want 1", factory.count())
	}
	runtime.send(presentation)
	if factory.count() != 1 {
		t.Fatalf("no-op update restarted dialer: starts=%d", factory.count())
	}

	stopSupervisorTest(t, runtime, cancel, done)
	_ = waitSnapshot(t, factory.stopped)
}

func TestRelaySupervisorFactoryReceivesCoordinatorSnapshotUnmixed(t *testing.T) {
	initial := config.ResolvedRelay{RelayManagedState: config.RelayManagedState{Mode: config.RelayModeOff}, Readiness: config.RelayReadinessOff}
	runtime := config.NewRelayCoordinator(initial)
	factory := newRecordingDialerFactory()
	supervisor := newRelaySupervisor(runtime, factory.new)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()

	next := selfHostedSnapshot("https://coherent.example.com", "coherent-session-123456")
	next.Label = "presentation generation"
	next.EntitlementToken = config.NewRelayEntitlementToken("coherent-token")
	runtime.Update(next)
	if got := waitSnapshot(t, factory.started); got != next {
		t.Fatalf("dialer observed mixed snapshot = %#v, want %#v", got, next)
	}
	if got := runtime.Current(); got != next {
		t.Fatalf("API runtime observed mixed snapshot = %#v, want %#v", got, next)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("supervisor error = %v", err)
	}
	_ = waitSnapshot(t, factory.stopped)
}
