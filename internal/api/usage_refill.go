package api

import (
	"time"

	"github.com/croutoncreations/redline/internal/decision"
)

// newWindowShift is how far resets_at must move to count as a new window.
// Sources that derive it as now + reset_after_seconds jitter by seconds per
// poll; a real refill moves it by a whole window (5 hours or a week).
const newWindowShift = time.Hour

// usageWindowRefilled detects either replenished allowance or a new window.
// Epsilon is zero for redemption verification and absorbs noise for firesales.
func usageWindowRefilled(pre, post decision.UsageWindow, epsilon float64) bool {
	return post.Remaining > pre.Remaining+epsilon || post.ResetsAt.Sub(pre.ResetsAt) >= newWindowShift
}
