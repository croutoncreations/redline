// Package usage selects exactly one allowance source for each provider account.
package usage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
)

type Source interface {
	Name() string
	Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error)
}

type Status struct {
	Active    string    `json:"active"`
	ChangedAt time.Time `json:"changed_at,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	Failures  int       `json:"consecutive_failures"`
}

type sourceState struct {
	Status
	nextProbe time.Time
}

type Manager struct {
	OpenUsage       Source
	Native          Source
	FailureLimit    int
	ReprobeInterval time.Duration
	MaxSnapshotAge  time.Duration
	Now             func() time.Time
	mu              sync.Mutex
	states          map[string]sourceState
}

func NewManager(openUsage, native Source, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{OpenUsage: openUsage, Native: native, FailureLimit: 2,
		ReprobeInterval: 15 * time.Minute, Now: now, states: make(map[string]sourceState)}
}

func (m *Manager) Fetch(ctx context.Context, accountID string, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	mode := provider.EffectiveUsageSource()
	if mode == "openusage" {
		return m.fetch(ctx, accountID, provider, m.OpenUsage)
	}
	if mode == "native" {
		return m.fetch(ctx, accountID, provider, m.Native)
	}

	m.mu.Lock()
	state := m.states[accountID]
	if state.Active == "" {
		state.Active = "openusage"
	}
	now := m.Now().UTC()
	active := state.Active
	shouldProbe := active == "native" && !now.Before(state.nextProbe)
	m.mu.Unlock()

	if active == "openusage" || shouldProbe {
		snapshot, raw, err := m.OpenUsage.Fetch(ctx, provider)
		fetchedAt := m.Now().UTC()
		freshnessFailure := false
		if err == nil {
			err = m.snapshotError(snapshot, fetchedAt)
			freshnessFailure = err != nil
		}
		if err == nil {
			m.success(accountID, "openusage", fetchedAt)
			return snapshot, raw, nil
		}
		if active == "openusage" {
			failures := m.failure(accountID, err)
			if !freshnessFailure && failures < m.failureLimit() {
				return decision.UsageSnapshot{}, nil, fmt.Errorf("openusage usage source: %w", err)
			}
		} else {
			m.probeFailure(accountID, err, fetchedAt)
		}
	}

	snapshot, raw, err := m.Native.Fetch(ctx, provider)
	fetchedAt := m.Now().UTC()
	if err == nil {
		err = m.snapshotError(snapshot, fetchedAt)
	}
	if err != nil {
		m.nativeFailure(accountID, err)
		return decision.UsageSnapshot{}, nil, fmt.Errorf("native usage source: %w", err)
	}
	m.success(accountID, "native", fetchedAt)
	if active != "native" {
		m.mu.Lock()
		state = m.states[accountID]
		state.nextProbe = fetchedAt.Add(m.reprobeInterval())
		m.states[accountID] = state
		m.mu.Unlock()
	}
	return snapshot, raw, nil
}

func (m *Manager) Status(accountID string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[accountID].Status
}

func (m *Manager) fetch(ctx context.Context, accountID string, provider config.Provider, source Source) (decision.UsageSnapshot, []byte, error) {
	snapshot, raw, err := source.Fetch(ctx, provider)
	now := m.Now().UTC()
	if err == nil {
		err = m.snapshotError(snapshot, now)
	}
	if err != nil {
		m.nativeFailure(accountID, err)
		return decision.UsageSnapshot{}, nil, fmt.Errorf("%s usage source: %w", source.Name(), err)
	}
	m.success(accountID, source.Name(), now)
	return snapshot, raw, nil
}

func (m *Manager) snapshotError(snapshot decision.UsageSnapshot, now time.Time) error {
	if snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("usage snapshot has no observation timestamp")
	}
	if snapshot.ObservedAt.After(now) {
		return fmt.Errorf("usage snapshot is from the future")
	}
	if now.Sub(snapshot.ObservedAt) > m.maxSnapshotAge() {
		return fmt.Errorf("usage snapshot is stale")
	}
	return nil
}

func (m *Manager) success(accountID, active string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[accountID]
	preserveFallbackError := active == "native" && state.Active == "openusage"
	if state.Active != active {
		state.ChangedAt = now
	}
	state.Active, state.Failures = active, 0
	if !preserveFallbackError {
		state.LastError = ""
	}
	m.states[accountID] = state
}

func (m *Manager) failure(accountID string, err error) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[accountID]
	state.Failures++
	state.LastError = err.Error()
	if state.Active == "" {
		state.Active = "openusage"
	}
	m.states[accountID] = state
	return state.Failures
}

func (m *Manager) probeFailure(accountID string, err error, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[accountID]
	state.LastError = err.Error()
	state.nextProbe = now.Add(m.reprobeInterval())
	m.states[accountID] = state
}

func (m *Manager) nativeFailure(accountID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[accountID]
	state.LastError = err.Error()
	m.states[accountID] = state
}

func (m *Manager) failureLimit() int {
	if m.FailureLimit <= 0 {
		return 2
	}
	return m.FailureLimit
}

func (m *Manager) reprobeInterval() time.Duration {
	if m.ReprobeInterval <= 0 {
		return 15 * time.Minute
	}
	return m.ReprobeInterval
}

func (m *Manager) maxSnapshotAge() time.Duration {
	if m.MaxSnapshotAge <= 0 {
		return 15 * time.Minute
	}
	return m.MaxSnapshotAge
}
