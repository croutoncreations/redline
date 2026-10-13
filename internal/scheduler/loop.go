package scheduler

import (
	"context"
	"log"
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
	// name identifies the loop in log lines. It matches the loop's config key
	// ("scheduler", "usage_monitor") so a log line points at what to change.
	name      string
	enabled   bool
	interval  time.Duration
	providers []string
	dispatch  DispatchFunc

	mu      sync.RWMutex
	cycleMu sync.Mutex
	status  Status
	// failing tracks the last logged error and consecutive failed cycles per
	// provider so a persistent failure is logged once, not every cycle.
	failing map[string]*failureRun
}

type failureRun struct {
	message string
	cycles  int
}

func NewLoop(enabled bool, interval time.Duration, providers []string, dispatch DispatchFunc) *Loop {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ordered := append([]string(nil), providers...)
	sort.Strings(ordered)
	return &Loop{
		name:    "scheduler",
		enabled: enabled, interval: interval, providers: ordered, dispatch: dispatch,
		failing: make(map[string]*failureRun),
		status:  Status{Enabled: enabled, PollInterval: interval.String(), Providers: make([]ProviderStatus, 0)},
	}
}

// Named sets the loop's name for log output. Use the loop's config key.
func (l *Loop) Named(name string) *Loop {
	l.name = name
	return l
}

func (l *Loop) Run(ctx context.Context) {
	if !l.enabled {
		log.Printf("redline %s: disabled; set %s.enabled to true in the config to turn it on", l.name, l.name)
		return
	}
	log.Printf("redline %s: started for %d provider(s), polling every %s", l.name, len(l.providers), l.interval)
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
		err := l.dispatch(ctx, provider)
		if err != nil {
			result.Error = err.Error()
		}
		l.logOutcome(ctx, provider, err)
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

// logOutcome logs a provider failure the first time it appears or when its
// message changes, and logs recovery once it clears. The latest error is also
// kept in Status, but that is only visible to someone who thinks to query the
// API; the daemon log is where a headless service gets looked at first.
func (l *Loop) logOutcome(ctx context.Context, provider string, err error) {
	if ctx.Err() != nil {
		// Shutdown cancels in-flight work; that is not a provider failure.
		return
	}
	previous := l.failing[provider]
	if err == nil {
		if previous != nil {
			log.Printf("redline %s: provider %q recovered after %d failed cycle(s)", l.name, provider, previous.cycles)
			delete(l.failing, provider)
		}
		return
	}
	if previous != nil && previous.message == err.Error() {
		previous.cycles++
		return
	}
	cycles := 1
	if previous != nil {
		cycles += previous.cycles
	}
	l.failing[provider] = &failureRun{message: err.Error(), cycles: cycles}
	log.Printf("redline %s: provider %q failed, retrying in %s (repeats of this error are not logged until it clears): %v",
		l.name, provider, l.interval, err)
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
