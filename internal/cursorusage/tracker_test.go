package cursorusage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type stubFetcher struct {
	report Report
	err    error
	calls  int
}

func (s *stubFetcher) Fetch(context.Context) (Report, error) {
	s.calls++
	return s.report, s.err
}

func reportAt(source string, fetched time.Time, expires *time.Time) Report {
	report := newReport()
	report.Source = source
	report.FetchedAt = timePointer(fetched)
	report.ExpiresAt = expires
	report.Meters = []Meter{{Key: "grokBot", Label: "Grok Bot", Group: GroupWeekly, Unit: "percent", Used: 10, Limit: floatPointer(100)}}
	return report
}

func TestTrackerAutoPrefersFreshOpenUsageAndNeverTouchesNative(t *testing.T) {
	now := fixedNow
	openUsage := &stubFetcher{report: reportAt(SourceOpenUsage, now.Add(-time.Minute), timePointer(now.Add(4*time.Minute)))}
	native := &stubFetcher{err: errors.New("must not be called")}
	tracker := NewTracker(ModeAuto, openUsage, native, 5*time.Minute, true, func() time.Time { return now })
	if err := tracker.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, ok := tracker.Report()
	if !ok || report.Source != SourceOpenUsage || report.Stale || native.calls != 0 {
		t.Fatalf("report=%#v native calls=%d", report, native.calls)
	}
}

func TestTrackerAutoFallsBackToNativeWhenOpenUsageIsStaleOrDown(t *testing.T) {
	now := fixedNow
	for name, openUsage := range map[string]*stubFetcher{
		"stale": {report: reportAt(SourceOpenUsage, now.Add(-time.Hour), timePointer(now.Add(-55*time.Minute)))},
		"down":  {err: errors.New("connection refused")},
	} {
		t.Run(name, func(t *testing.T) {
			native := &stubFetcher{report: reportAt(SourceNative, now, nil)}
			tracker := NewTracker(ModeAuto, openUsage, native, 5*time.Minute, true, func() time.Time { return now })
			if err := tracker.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			report, _ := tracker.Report()
			if report.Source != SourceNative || report.ExpiresAt == nil || !report.ExpiresAt.Equal(now.Add(6*time.Minute)) {
				t.Fatalf("report = %#v", report)
			}
		})
	}
}

func TestTrackerAutoKeepsStaleOpenUsageWhenNativeFails(t *testing.T) {
	now := fixedNow
	openUsage := &stubFetcher{report: reportAt(SourceOpenUsage, now.Add(-time.Hour), timePointer(now.Add(-55*time.Minute)))}
	native := &stubFetcher{err: errors.New("cursor is not signed in on this Mac")}
	tracker := NewTracker(ModeAuto, openUsage, native, 5*time.Minute, true, func() time.Time { return now })
	_ = tracker.Refresh(context.Background())
	report, _ := tracker.Report()
	if report.Source != SourceOpenUsage || !report.Stale || !strings.Contains(report.RefreshError, "not signed in") {
		t.Fatalf("report = %#v", report)
	}
}

func TestTrackerKeepsLastGoodReportAfterFailure(t *testing.T) {
	now := fixedNow
	clock := now
	source := &stubFetcher{report: reportAt(SourceOpenUsage, now, timePointer(now.Add(5*time.Minute)))}
	tracker := NewTracker(ModeOpenUsage, source, nil, 5*time.Minute, false, func() time.Time { return clock })
	if _, ok := tracker.Report(); ok {
		t.Fatal("an implicit tracker without data stays hidden")
	}
	_ = tracker.Refresh(context.Background())
	source.err = errors.New("OpenUsage returned HTTP 503")
	clock = now.Add(10 * time.Minute)
	if err := tracker.Refresh(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	report, ok := tracker.Report()
	if !ok || len(report.Meters) != 1 || !report.Stale || !strings.Contains(report.RefreshError, "503") {
		t.Fatalf("report = %#v", report)
	}
}

func TestTrackerExplicitWithoutDataExplainsWhy(t *testing.T) {
	tracker := NewTracker(ModeNative, nil, &stubFetcher{err: errors.New("cursor is not signed in on this Mac")}, time.Minute, true, func() time.Time { return fixedNow })
	_ = tracker.Refresh(context.Background())
	report, ok := tracker.Report()
	if !ok || report.HasData() || report.Stale || !strings.Contains(report.RefreshError, "not signed in") {
		t.Fatalf("report = %#v", report)
	}
}

func TestTrackerOffDoesNothing(t *testing.T) {
	var tracker *Tracker
	if _, ok := tracker.Report(); ok {
		t.Fatal("nil tracker must be hidden")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tracker.Run(ctx)
}

func TestSanitizeMessageRedactsSecretsAndPaths(t *testing.T) {
	got := sanitizeMessage("failed for /Users/someone/Library/x Bearer abc.def token=xyz eyJhbGciOi.eyJzdWIi.sig " + strings.Repeat("x", 300))
	for _, leaked := range []string{"/Users/someone", "abc.def", "xyz", "eyJhbGciOi"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("%q leaked in %q", leaked, got)
		}
	}
	if len(got) > 200 {
		t.Fatalf("len = %d", len(got))
	}
}

// Polls follow the source's expiry so a fresh OpenUsage card is picked up
// seconds after it rotates, instead of up to a full interval later.
func TestTrackerAlignsNextPollToReportExpiry(t *testing.T) {
	now := fixedNow
	cases := map[string]struct {
		expires *time.Time
		err     error
		want    time.Duration
	}{
		"expires soon":       {expires: timePointer(now.Add(2 * time.Minute)), want: 2*time.Minute + expiryGrace},
		"already expired":    {expires: timePointer(now.Add(-time.Minute)), want: minRetry},
		"capped at interval": {expires: timePointer(now.Add(time.Hour)), want: 5 * time.Minute},
		"no expiry":          {want: 5 * time.Minute},
		"failed refresh":     {err: errors.New("down"), want: 5 * time.Minute},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fetch := &stubFetcher{report: reportAt(SourceOpenUsage, now.Add(-time.Minute), tc.expires), err: tc.err}
			tracker := NewTracker(ModeOpenUsage, fetch, nil, 5*time.Minute, true, func() time.Time { return now })
			_ = tracker.Refresh(context.Background())
			if got := tracker.nextDelay(now); got != tc.want {
				t.Fatalf("nextDelay = %v, want %v", got, tc.want)
			}
		})
	}
}
