package cursorusage

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Source modes.
const (
	ModeOff       = "off"
	ModeOpenUsage = "openusage"
	ModeNative    = "native"
	ModeAuto      = "auto"
)

// Tracker keeps the latest Cursor report in memory. It is independent of the
// scheduler: nothing it observes is stored as a usage snapshot or consulted
// by dispatch decisions.
type Tracker struct {
	Mode      string
	OpenUsage fetcher
	Native    fetcher
	Interval  time.Duration
	Now       func() time.Time
	// Explicit is true when the operator configured Cursor tracking. An
	// implicit (default) tracker stays hidden until it has data, so people
	// without Cursor never see an empty card.
	Explicit bool

	mu      sync.Mutex
	latest  Report
	known   bool
	lastErr string
	running bool
}

// Fetcher is one Cursor collector.
type Fetcher = fetcher

// NewTracker builds a tracker for mode. openUsage and native may be nil when
// the mode does not use them.
func NewTracker(mode string, openUsage, native Fetcher, interval time.Duration, explicit bool, now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Tracker{Mode: mode, OpenUsage: openUsage, Native: native, Interval: interval, Explicit: explicit, Now: now}
}

// Refresh collects once and updates the cached report. A failed refresh
// keeps the last good report and records why.
func (t *Tracker) Refresh(ctx context.Context) error {
	if t == nil || t.Mode == ModeOff {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	report, err := t.collect(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		t.lastErr = sanitizeMessage(err.Error())
		return err
	}
	t.latest, t.known, t.lastErr = report, true, ""
	return nil
}

func (t *Tracker) collect(ctx context.Context) (Report, error) {
	switch t.Mode {
	case ModeOpenUsage:
		return t.fromOpenUsage(ctx)
	case ModeNative:
		return t.fromNative(ctx)
	case ModeAuto:
		report, err := t.fromOpenUsage(ctx)
		if err == nil && !report.At(t.Now()).Stale {
			return report, nil
		}
		native, nativeErr := t.fromNative(ctx)
		if nativeErr == nil {
			return native, nil
		}
		if err == nil {
			// OpenUsage's data is old but it is still the best we have.
			report.RefreshError = sanitizeMessage("Built-in tracking: " + nativeErr.Error())
			return report, nil
		}
		return Report{}, fmt.Errorf("OpenUsage: %v; built-in: %v", err, nativeErr)
	default:
		return Report{}, fmt.Errorf("cursor usage source %q is not supported", t.Mode)
	}
}

func (t *Tracker) fromOpenUsage(ctx context.Context) (Report, error) {
	if t.OpenUsage == nil {
		return Report{}, fmt.Errorf("OpenUsage is not configured")
	}
	return t.OpenUsage.Fetch(ctx)
}

func (t *Tracker) fromNative(ctx context.Context) (Report, error) {
	if t.Native == nil {
		return Report{}, fmt.Errorf("built-in Cursor tracking is not configured")
	}
	report, err := t.Native.Fetch(ctx)
	if err != nil {
		return Report{}, err
	}
	if report.FetchedAt != nil && report.ExpiresAt == nil {
		// Matches OpenUsage's convention: current until the next poll is due.
		report.ExpiresAt = timePointer(report.FetchedAt.Add(t.Interval + time.Minute))
	}
	return report, nil
}

// Report returns the report to show now, or false when it should be hidden.
func (t *Tracker) Report() (Report, bool) {
	if t == nil || t.Mode == ModeOff {
		return Report{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.Now()
	if !t.known {
		if !t.Explicit {
			return Report{}, false
		}
		report := newReport()
		report.RefreshError = t.lastErr
		if report.RefreshError == "" {
			report.RefreshError = "Waiting for the first Cursor usage reading."
		}
		return report.At(now), true
	}
	report := t.latest.At(now)
	if report.RefreshError == "" {
		report.RefreshError = t.lastErr
	}
	return report, true
}

// expiryGrace is how long after a report expires the tracker asks again.
// OpenUsage refreshes its card at expiry, so asking a moment later picks up
// the new reading instead of holding an expired one for a whole interval.
const expiryGrace = 5 * time.Second

// minRetry bounds how quickly the tracker re-asks when the source has not
// refreshed yet, so a stuck source is not hammered.
const minRetry = 10 * time.Second

// nextDelay is how long to wait before the next refresh. Polls are aligned to
// the current report's expiry, never later than Interval, so a report is not
// shown as out of date merely because Redline's poll phase lags the source's.
func (t *Tracker) nextDelay(now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.known || t.lastErr != "" || t.latest.ExpiresAt == nil {
		return t.Interval
	}
	delay := t.latest.ExpiresAt.Sub(now) + expiryGrace
	if delay < minRetry {
		// Already expired: the source has not refreshed yet. Retry soon.
		delay = minRetry
	}
	if delay > t.Interval {
		delay = t.Interval
	}
	return delay
}

// Run refreshes immediately and then again shortly after each report expires
// (at most every Interval) until ctx ends. It is safe to call once; later
// calls return at once.
func (t *Tracker) Run(ctx context.Context) {
	if t == nil || t.Mode == ModeOff {
		return
	}
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return
	}
	t.running = true
	t.mu.Unlock()
	_ = t.Refresh(ctx)
	timer := time.NewTimer(t.nextDelay(t.Now()))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_ = t.Refresh(ctx)
			timer.Reset(t.nextDelay(t.Now()))
		}
	}
}
