package cli

import (
	"context"
	"sync/atomic"

	"github.com/jfox/redline/internal/config"
)

// relayDialerRun is injected so relay authority transitions can be tested
// without network connections, credentials, or timing-dependent retries.
type relayDialerRun func(context.Context)

type relayDialerFactory func(config.ResolvedRelay, func() string) (relayDialerRun, error)

type relaySupervisor struct {
	initial     config.ResolvedRelay
	updates     <-chan config.ResolvedRelay
	unsubscribe func()
	newDialer   relayDialerFactory
	active      *activeRelayDialer
}

type activeRelayDialer struct {
	connection relayConnection
	token      *relayTokenSource
	cancel     context.CancelFunc
	done       chan struct{}
}

type relayTokenSource struct{ value atomic.Value }

func newRelayTokenSource(token string) *relayTokenSource {
	source := &relayTokenSource{}
	source.value.Store(token)
	return source
}

func (s *relayTokenSource) Load() string { return s.value.Load().(string) }
func (s *relayTokenSource) Store(token string) {
	s.value.Store(token)
}

type relayConnection struct {
	url                 string
	sessionID           string
	reconnectGeneration uint64
}

// newRelaySupervisor captures the coordinator's initial snapshot from the same
// subscription it retains for updates. The startup banner and initial dial
// decision can therefore use one generation without a Current/Subscribe race.
func newRelaySupervisor(runtime config.RelayRuntime, newDialer relayDialerFactory) *relaySupervisor {
	updates, unsubscribe := runtime.Subscribe()
	initial := <-updates
	return &relaySupervisor{
		initial:     initial,
		updates:     updates,
		unsubscribe: unsubscribe,
		newDialer:   newDialer,
	}
}

func (s *relaySupervisor) Initial() config.ResolvedRelay { return s.initial }

func (s *relaySupervisor) Close() { s.unsubscribe() }

func (s *relaySupervisor) Run(ctx context.Context) error {
	defer s.Close()
	defer s.stopActive()
	if err := s.apply(ctx, s.initial); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case snapshot, ok := <-s.updates:
			if !ok {
				return nil
			}
			if err := s.apply(ctx, snapshot); err != nil {
				return err
			}
		}
	}
}

func (s *relaySupervisor) apply(ctx context.Context, snapshot config.ResolvedRelay) error {
	if !snapshot.CanDial() {
		s.stopActive()
		return nil
	}
	connection := relayConnection{
		url:                 snapshot.URL,
		sessionID:           snapshot.SessionID,
		reconnectGeneration: snapshot.ReconnectGeneration,
	}
	if s.active != nil && s.active.connection == connection {
		// A 204 refresh deliberately preserves the live socket. Future
		// reconnects inside that same dialer's Run loop must nevertheless read
		// the newly accepted token.
		s.active.token.Store(snapshot.EntitlementToken.Value())
		return nil
	}
	s.stopActive()
	token := newRelayTokenSource(snapshot.EntitlementToken.Value())
	run, err := s.newDialer(snapshot, token.Load)
	if err != nil {
		return err
	}
	dialerCtx, cancel := context.WithCancel(ctx)
	active := &activeRelayDialer{
		connection: connection,
		token:      token,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	s.active = active
	go func() {
		defer close(active.done)
		run(dialerCtx)
	}()
	return nil
}

func (s *relaySupervisor) stopActive() {
	if s.active == nil {
		return
	}
	active := s.active
	s.active = nil
	active.cancel()
	<-active.done
}
