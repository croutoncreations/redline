package cursorusage

import (
	"fmt"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
)

// monthSeconds is used when a pool reports a reset but no period.
const monthSeconds = int64((30 * 24 * time.Hour) / time.Second)

// Snapshot turns a Cursor report into a scheduling snapshot built only from
// the monthly subscription pools.
//
// The account window is Cursor's own "Total usage" meter when reported, else
// the tighter of the two pools. Each pool is also a model-scoped monthly
// allowance (model:cursor_models:monthly, model:other_models:monthly), so a
// task is gated by the pool its model actually draws from.
//
// On-demand spend, credits and Grok Bot are deliberately left out: they are
// not included quota, and scheduling must never spend money or count paid
// overage as headroom.
func Snapshot(report Report, now time.Time) (decision.UsageSnapshot, error) {
	if report.FetchedAt == nil {
		return decision.UsageSnapshot{}, fmt.Errorf("cursor usage has not been read yet")
	}
	pools := map[string]Meter{}
	for _, meter := range report.Meters {
		if meter.Group != GroupMonthly || meter.Unit != "percent" || meter.Limit == nil || *meter.Limit <= 0 || meter.ResetsAt == nil {
			continue
		}
		pools[meter.Key] = meter
	}
	cursorPool, hasCursor := pools["autoUsage"]
	otherPool, hasOther := pools["apiUsage"]
	if !hasCursor && !hasOther {
		return decision.UsageSnapshot{}, fmt.Errorf("cursor did not report its monthly subscription pools")
	}
	snapshot := decision.UsageSnapshot{
		Provider: "cursor", ObservedAt: report.FetchedAt.UTC(), Source: report.Source, Confidence: "high",
	}
	if len(report.Errors) > 0 {
		snapshot.Confidence = "medium"
	}
	for _, pool := range []struct {
		meter Meter
		ok    bool
		group string
	}{{cursorPool, hasCursor, config.CursorModelsGroup}, {otherPool, hasOther, config.CursorOtherModelsGroup}} {
		if !pool.ok {
			continue
		}
		snapshot.Allowances = append(snapshot.Allowances, decision.AllowanceWindow{
			Key: "model:" + pool.group + ":monthly", SourceLabel: pool.meter.Label, Scope: "model", Role: "monthly",
			Remaining: remaining(pool.meter), ResetsAt: pool.meter.ResetsAt.UTC(), PeriodDurationSeconds: period(pool.meter),
		})
	}
	account, ok := pools["totalUsage"]
	if !ok {
		account = cursorPool
		if !hasCursor || (hasOther && remaining(otherPool) < remaining(cursorPool)) {
			account = otherPool
		}
	}
	snapshot.Weekly = decision.UsageWindow{
		Remaining: remaining(account), ResetsAt: account.ResetsAt.UTC(), PeriodSeconds: period(account),
	}
	if err := snapshot.Validate(); err != nil {
		return decision.UsageSnapshot{}, err
	}
	return snapshot, nil
}

func remaining(meter Meter) float64 {
	fraction := 1 - meter.Used / *meter.Limit
	if fraction < 0 {
		return 0
	}
	if fraction > 1 {
		return 1
	}
	return fraction
}

func period(meter Meter) int64 {
	if meter.WindowSeconds != nil && *meter.WindowSeconds > 0 {
		return *meter.WindowSeconds
	}
	return monthSeconds
}
