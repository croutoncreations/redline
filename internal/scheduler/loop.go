package scheduler

import (
	"context"
	"sort"
	"sync"
	"time"
)

type DispatchFunc func(context.Context, string) error

type ProviderStatus struct {
	ProviderAccountID string    `json:"provider_account_id"`
	CheckedAt         time.Time `json:"checked_at"`
	Error             string    `json:"error,omitempty"`
}

type Status struct {
	Enabled      bool             `json:"enabled"`
	PollInterval string           `json:"poll_interval"`
	Running      bool             `json:"running"`
	LastCycleAt  *time.Time       `json:"last_cycle_at,omitempty"`
	NextCycleAt  *time.Time       `json:"next_cycle_at,omitempty"`
	Providers    []ProviderStatus `json:"providers"`
}

type Loop struct {
	enabled   bool
	interval  time.Duration
	providers []string
	dispatch  DispatchFunc

	mu      sync.RWMutex
	cycleMu sync.Mutex
	status  Status
}

func NewLoop(enabled bool, interval time.Duration, providers []string, dispatch DispatchFunc) *Loop {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ordered := append([]string(nil), providers...)
	sort.Strings(ordered)
	return &Loop{
		enabled: enabled, interval: interval, providers: ordered, dispatch: dispatch,
		status: Status{Enabled: enabled, PollInterval: interval.String(), Providers: make([]ProviderStatus, 0)},
	}
}

func (l *Loop) Run(ctx context.Context) {
	if !l.enabled {
		return
	}
	l.RunCycle(ctx, time.Now().UTC())
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			l.RunCycle(ctx, now.UTC())
		}
	}
}

func (l *Loop) RunCycle(ctx context.Context, now time.Time) {
	if !l.enabled {
		return
	}
	l.cycleMu.Lock()
	defer l.cycleMu.Unlock()
	l.mu.Lock()
	l.status.Running = true
	l.mu.Unlock()

	results := make([]ProviderStatus, 0, len(l.providers))
	for _, provider := range l.providers {
		if ctx.Err() != nil {
			break
		}
		result := ProviderStatus{ProviderAccountID: provider, CheckedAt: now}
		if err := l.dispatch(ctx, provider); err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
	}
	next := now.Add(l.interval)
	l.mu.Lock()
	l.status.Running = false
	l.status.LastCycleAt = timePointer(now)
	l.status.NextCycleAt = timePointer(next)
	l.status.Providers = results
	l.mu.Unlock()
}

func (l *Loop) Status() Status {
	l.mu.RLock()
	defer l.mu.RUnlock()
	status := l.status
	status.Providers = make([]ProviderStatus, len(l.status.Providers))
	copy(status.Providers, l.status.Providers)
	return status
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
