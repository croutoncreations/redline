package config

import "sync"

// RelayRuntime provides an immutable point-in-time view of relay state.
// Consumers take one snapshot per operation so mode, readiness, and dial
// inputs cannot be assembled from different generations.
type RelayRuntime interface {
	Current() ResolvedRelay
	Subscribe() (<-chan ResolvedRelay, func())
}

// RelayCoordinator owns the process's current resolved relay snapshot. It is
// intentionally limited to atomic replacement and observation; RelayManager
// owns configuration and lifecycle mutation.
type RelayCoordinator struct {
	mu          sync.RWMutex
	current     ResolvedRelay
	nextID      uint64
	subscribers map[uint64]chan ResolvedRelay
}

func NewRelayCoordinator(initial ResolvedRelay) *RelayCoordinator {
	return &RelayCoordinator{current: cloneResolvedRelay(initial), subscribers: make(map[uint64]chan ResolvedRelay)}
}

func (r *RelayCoordinator) Current() ResolvedRelay {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneResolvedRelay(r.current)
}

func cloneResolvedRelay(value ResolvedRelay) ResolvedRelay {
	if value.Deactivation != nil {
		intent := *value.Deactivation
		value.Deactivation = &intent
	}
	return value
}

func resolvedRelayEqual(left, right ResolvedRelay) bool {
	leftIntent, rightIntent := left.Deactivation, right.Deactivation
	left.Deactivation, right.Deactivation = nil, nil
	if left != right {
		return false
	}
	if leftIntent == nil || rightIntent == nil {
		return leftIntent == nil && rightIntent == nil
	}
	return *leftIntent == *rightIntent
}

// Modify atomically derives and publishes a snapshot. It is the safe boundary
// for independent runtime facts (such as socket connectivity) that would
// otherwise lose a concurrent entitlement update in a Current/Update race.
func (r *RelayCoordinator) Modify(fn func(ResolvedRelay) ResolvedRelay) ResolvedRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := fn(cloneResolvedRelay(r.current))
	if resolvedRelayEqual(next, r.current) {
		return next
	}
	r.current = cloneResolvedRelay(next)
	r.publishLocked(r.current)
	return next
}

// Update atomically replaces the current value and offers that same value to
// every subscriber. A slow subscriber retains the newest snapshot rather than
// blocking relay renewal or configuration changes.
func (r *RelayCoordinator) Update(next ResolvedRelay) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = cloneResolvedRelay(next)
	r.publishLocked(r.current)
}

// UpdateController publishes an entitlement result without overwriting a
// newer, independently observed socket fact or presentation-only managed
// state. Authority/configuration generation changes intentionally do not carry
// connection truth forward.
func (r *RelayCoordinator) UpdateController(next ResolvedRelay) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.current
	if current.Generation == next.Generation && current.URL == next.URL && current.SessionID == next.SessionID && current.ReconnectGeneration == next.ReconnectGeneration {
		next.Connected = current.Connected
		next.Label = current.Label
	}
	r.current = cloneResolvedRelay(next)
	r.publishLocked(r.current)
}

func (r *RelayCoordinator) publishLocked(next ResolvedRelay) {
	for _, updates := range r.subscribers {
		snapshot := cloneResolvedRelay(next)
		select {
		case updates <- snapshot:
			continue
		default:
		}
		select {
		case <-updates:
		default:
		}
		select {
		case updates <- snapshot:
		default:
		}
	}
}

// Subscribe returns the current snapshot immediately and subsequent updates.
// The cancellation function is idempotent and must be called by long-lived
// consumers when they stop.
func (r *RelayCoordinator) Subscribe() (<-chan ResolvedRelay, func()) {
	r.mu.Lock()
	id := r.nextID
	r.nextID++
	updates := make(chan ResolvedRelay, 1)
	updates <- cloneResolvedRelay(r.current)
	r.subscribers[id] = updates
	r.mu.Unlock()

	var once sync.Once
	return updates, func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.subscribers, id)
			close(updates)
			r.mu.Unlock()
		})
	}
}
