package push

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

type Store interface {
	ActivePushRegistrations(context.Context, string) ([]store.PushRegistration, error)
	ActivePushRegistration(ctx context.Context, issuer, id string) (store.PushRegistration, bool, error)
	DeletePushRegistrationToken(context.Context, string, string) error
}
type Stats struct {
	Dropped   uint64 `json:"dropped"`
	Coalesced uint64 `json:"coalesced"`
}
type outcome struct {
	woken      bool
	configured bool
}

type job struct {
	registration store.PushRegistration
	hint         WakeHint
	done         chan outcome
}
type Dispatcher struct {
	store     Store
	issuer    string
	transport Transport
	logger    *log.Logger
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	queue     chan job
	pending   map[string]bool
	closed    bool
	started   bool
	stats     Stats
	workers   sync.WaitGroup
	// unavailable is set while push is known to be unconfigured, so each
	// outage logs once; a successful wake clears it. Guarded by mu.
	unavailable bool
}

// noteUnavailable logs the first "not configured" signal of an outage.
func (d *Dispatcher) noteUnavailable() {
	d.mu.Lock()
	first := !d.unavailable
	d.unavailable = true
	d.mu.Unlock()
	if first {
		d.logger.Print("push not configured")
	}
}

func (d *Dispatcher) noteAvailable() {
	d.mu.Lock()
	d.unavailable = false
	d.mu.Unlock()
}

func NewDispatcher(s Store, issuer string, t Transport, logger *log.Logger) *Dispatcher {
	if t == nil {
		t = Disabled{}
	}
	if logger == nil {
		logger = log.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{store: s, issuer: issuer, transport: t, logger: logger, ctx: ctx, cancel: cancel, queue: make(chan job, 256), pending: map[string]bool{}}
	return d
}
func (d *Dispatcher) Stats() Stats { d.mu.Lock(); defer d.mu.Unlock(); return d.stats }
func (d *Dispatcher) Close() {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		d.cancel()
		close(d.queue)
	}
	d.mu.Unlock()
	d.workers.Wait()
}
func kind(event string) string {
	switch strings.SplitN(event, ".", 2)[0] {
	case "reset":
		return "reset"
	case "firesale":
		return "firesale"
	case "test":
		return "test"
	default:
		return "job"
	}
}
func (d *Dispatcher) Dispatch(ctx context.Context, id int64, event string, wait bool) domain.PushResult {
	result := domain.PushResult{Configured: true}
	if _, disabled := d.transport.(Disabled); disabled {
		d.noteUnavailable()
		result.Configured = false
		return result
	}
	registrations, err := d.store.ActivePushRegistrations(ctx, d.issuer)
	if err != nil {
		d.logger.Print("push registration lookup failed")
		return result
	}
	var completions []chan outcome
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return result
	}
	if !d.started {
		d.started = true
		for range 4 {
			d.workers.Add(1)
			go d.worker()
		}
	}
	for _, registration := range registrations {
		if d.pending[registration.DeviceSessionID] {
			d.stats.Coalesced++
			continue
		}
		item := job{registration: registration, hint: WakeHint{DeliveryID: id, Kind: kind(event)}}
		if wait {
			item.done = make(chan outcome, 1)
		}
		select {
		case d.queue <- item:
			d.pending[registration.DeviceSessionID] = true
			if item.done != nil {
				completions = append(completions, item.done)
			}
		default:
			d.stats.Dropped++
			d.logger.Print("push queue full; wake dropped")
		}
	}
	d.mu.Unlock()
	for _, done := range completions {
		select {
		case sent := <-done:
			if !sent.configured {
				result.Configured = false
			}
			if sent.woken {
				result.Woken++
			}
		case <-ctx.Done():
			return result
		}
	}
	return result
}
func (d *Dispatcher) worker() {
	defer d.workers.Done()
	for item := range d.queue {
		d.mu.Lock()
		delete(d.pending, item.registration.DeviceSessionID)
		d.mu.Unlock()
		ok := false
		configured := true
		if d.ctx.Err() == nil {
			ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
			// Re-resolve at send time: while queued, the session may have been
			// revoked or rotated out (skip), or the device may have refreshed
			// its token (send to the current one; wakes are coalesced per device).
			r, active, err := d.store.ActivePushRegistration(ctx, d.issuer, item.registration.DeviceSessionID)
			switch {
			case err != nil:
				d.logger.Print("push registration lookup failed")
			case active:
				err = d.transport.Wake(ctx, Wake{Provider: r.Provider, Token: r.Token, Platform: r.Platform, Hint: item.hint})
				switch {
				case err == nil:
					ok = true
					d.noteAvailable()
				case errors.Is(err, ErrNotConfigured):
					configured = false
					d.noteUnavailable()
				case errors.Is(err, ErrUnregistered):
					if d.store.DeletePushRegistrationToken(ctx, r.DeviceSessionID, r.Token) != nil {
						d.logger.Print("push registration removal failed")
					}
				default:
					d.logger.Print("push wake failed")
				}
			}
			cancel()
		}
		if item.done != nil {
			item.done <- outcome{woken: ok, configured: configured}
		}
	}
}
