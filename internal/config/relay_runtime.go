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
// intentionally limited to atomic replacement and observation: license
// exchange, renewal, and configuration mutation belong to later phases.
type RelayCoordinator struct {
	mu          sync.RWMutex
	current     ResolvedRelay
	nextID      uint64
	subscribers map[uint64]chan ResolvedRelay
}

func NewRelayCoordinator(initial ResolvedRelay) *RelayCoordinator {
	return &RelayCoordinator{current: initial, subscribers: make(map[uint64]chan ResolvedRelay)}
}

func (r *RelayCoordinator) Current() ResolvedRelay {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

// Update atomically replaces the current value and offers that same value to
// every subscriber. A slow subscriber retains the newest snapshot rather than
// blocking relay renewal or configuration changes.
func (r *RelayCoordinator) Update(next ResolvedRelay) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = next
	for _, updates := range r.subscribers {
		select {
		case updates <- next:
			continue
		default:
		}
		select {
		case <-updates:
		default:
		}
		select {
		case updates <- next:
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
	updates <- r.current
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
