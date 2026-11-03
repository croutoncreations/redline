package api

import (
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
)

// Some sources derive resets_at as now + reset_after_seconds, so the same
// window's reset time jitters later by seconds between polls. Only a jump of
// a meaningful fraction of a window is a new window.
func TestUsageWindowRefilledIgnoresResetTimeJitter(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	pre := decision.UsageWindow{Remaining: .4, ResetsAt: base.Add(24 * time.Hour)}
	for _, tc := range []struct {
		name  string
		shift time.Duration
		want  bool
	}{
		{"same window, seconds of drift", 7 * time.Second, false},
		{"same window, a few minutes of drift", 4 * time.Minute, false},
		{"next weekly window", 7 * 24 * time.Hour, true},
		{"early global reset starts a new week", 6 * 24 * time.Hour, true},
	} {
		post := pre
		post.ResetsAt = pre.ResetsAt.Add(tc.shift)
		if got := usageWindowRefilled(pre, post, .02); got != tc.want {
			t.Errorf("%s: refilled=%v want %v", tc.name, got, tc.want)
		}
	}
}
