package cli

import (
	"context"

	"github.com/jfox/redline/internal/config"
)

// relayDialerRun is injected so relay authority transitions can be tested
// without network connections, credentials, or timing-dependent retries.
type relayDialerRun func(context.Context)

type relayDialerFactory func(config.ResolvedRelay) (relayDialerRun, error)

type relaySupervisor struct {
	initial     config.ResolvedRelay
	updates     <-chan config.ResolvedRelay
	unsubscribe func()
	newDialer   relayDialerFactory
	active      *activeRelayDialer
}

type activeRelayDialer struct {
	connection relayConnection
	cancel     context.CancelFunc
	done       chan struct{}
}

type relayConnection struct {
	url       string
	sessionID string
	token     config.RelayEntitlementToken
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
		url:       snapshot.URL,
		sessionID: snapshot.SessionID,
		token:     snapshot.EntitlementToken,
	}
	if s.active != nil && s.active.connection == connection {
		return nil
	}
	s.stopActive()
	run, err := s.newDialer(snapshot)
	if err != nil {
		return err
	}
	dialerCtx, cancel := context.WithCancel(ctx)
	active := &activeRelayDialer{
		connection: connection,
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
