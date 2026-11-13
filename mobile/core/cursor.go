package core

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// cursorReport mirrors the desktop's cursor_usage member. A copy rather than
// an import: internal/cursorusage depends on the desktop's configuration,
// which depends on this package through the relay.
type cursorReport struct {
	Plan         string          `json:"plan,omitempty"`
	Source       string          `json:"source,omitempty"`
	FetchedAt    *time.Time      `json:"fetched_at,omitempty"`
	ExpiresAt    *time.Time      `json:"expires_at,omitempty"`
	Errors       []string        `json:"errors,omitempty"`
	RefreshError string          `json:"refresh_error,omitempty"`
	Meters       []cursorMeter   `json:"meters"`
	Balances     []cursorBalance `json:"balances"`
	NotReported  []string        `json:"not_reported,omitempty"`
}

type cursorMeter struct {
	Key      string     `json:"key"`
	Label    string     `json:"label"`
	Group    string     `json:"group"`
	Unit     string     `json:"unit"`
	Used     float64    `json:"used"`
	Limit    *float64   `json:"limit,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

type cursorBalance struct {
	Key       string      `json:"key"`
	Label     string      `json:"label"`
	Unit      string      `json:"unit"`
	Available float64     `json:"available"`
	ExpiresAt []time.Time `json:"expires_at,omitempty"`
}

// cursorStaleGrace matches the desktop's cursorusage.StaleGrace: OpenUsage
// writes its next card a few seconds after the last one expires, so a report
// is out of date only once the source has missed its refresh.
const cursorStaleGrace = 2 * time.Minute

// stale reports whether the report is past its expiry plus the grace. A
// report without an expiry is never presented as current.
func (r cursorReport) stale(now time.Time) bool {
	return r.FetchedAt != nil && (r.ExpiresAt == nil || !now.Before(r.ExpiresAt.Add(cursorStaleGrace)))
}

const (
	cursorGroupMonthly  = "monthly"
	cursorGroupWeekly   = "weekly"
	cursorGroupOnDemand = "on_demand"
	cursorSourceOpen    = "openusage"
	cursorSourceNative  = "cursor"
)

// CursorUsageView is Cursor's usage card: the monthly model pools, Grok Bot's
// weekly allowance, on-demand spend and credits, kept apart exactly as the
// desktop reports them.
//
// Interpreted here rather than on each platform, like the provider meters, so
// the phone, the web page and the Mac agree about what is out of date, what is
// used up, and what was never reported.
type CursorUsageView struct {
	Plan string `json:"plan,omitempty"`
	// State is the one-word status: "Up to date", "Out of date", "Partial" or
	// "No data". Tone is "healthy", "near" or "paused", for colour.
	State string `json:"state"`
	Tone  string `json:"tone"`
	// Stale is true once the desktop's source has missed its refresh. Every
	// value is then the last known one and the screen says so.
	Stale bool `json:"stale"`
	// UpdatedLabel says when and where the numbers were read.
	UpdatedLabel string `json:"updated_label"`
	// Scheduled is true when the desktop also has Cursor as a provider
	// account. Its monthly pools then gate Cursor jobs (shown on the
	// provider's own card), and this card is the rest: Grok Bot, on-demand
	// and credits, none of which ever schedule work. False means the whole
	// card is for reference only.
	Scheduled bool `json:"scheduled"`
	// Note is the one line saying what this card is and is not used for,
	// worded for Scheduled so it is never wrong about it.
	Note        string             `json:"note"`
	Errors      []string           `json:"errors,omitempty"`
	NotReported []string           `json:"not_reported,omitempty"`
	Groups      []CursorGroupView  `json:"groups,omitempty"`
	Balances    []CursorBalanceRow `json:"balances,omitempty"`
}

// CursorGroupView is one billing group: monthly pools, the weekly Grok Bot
// allowance, or on-demand spend.
type CursorGroupView struct {
	Key     string           `json:"key"`
	Heading string           `json:"heading"`
	Meters  []CursorMeterRow `json:"meters"`
}

// CursorMeterRow is one meter.
type CursorMeterRow struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// ValueLabel is the number as it should read: "58% left", "Used up",
	// "$1.20 spent", "7 of 10 left". What is left, like every provider
	// meter, so a fuller bar always means more room.
	ValueLabel string `json:"value_label"`
	// HasLimit is false when the provider gave no denominator. The screen
	// must then show the spend only, never a bar or a percentage.
	HasLimit bool `json:"has_limit"`
	// RemainingPercent is 0-100 and meaningful only with HasLimit.
	RemainingPercent int  `json:"remaining_percent"`
	Exhausted        bool `json:"exhausted"`
	// ResetsAt is empty when the reset was not reported; never invented.
	ResetsAt        string `json:"resets_at,omitempty"`
	ResetsInSeconds int64  `json:"resets_in_seconds"`
}

// CursorBalanceRow is an amount available to spend, such as credits.
type CursorBalanceRow struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	ValueLabel string `json:"value_label"`
	// NextExpiresAt is the soonest future expiry, or empty.
	NextExpiresAt string `json:"next_expires_at,omitempty"`
}

var cursorGroupHeadings = []struct{ key, heading string }{
	{cursorGroupMonthly, "Monthly"},
	{cursorGroupWeekly, "Weekly"},
	{cursorGroupOnDemand, "On-demand"},
}

// renderCursorUsage builds the card, or nil when the desktop sent none
// (Cursor usage off, or a desktop from before it existed).
func renderCursorUsage(report *cursorReport, scheduled bool, now time.Time) *CursorUsageView {
	if report == nil {
		return nil
	}
	// Freshness is judged on this clock with the desktop's own grace, not
	// read from the flag the desktop computed when it sent the frame.
	current := *report
	stale := current.stale(now)
	view := &CursorUsageView{
		Plan:      current.Plan,
		Stale:     stale,
		Scheduled: scheduled,
		Errors:    append([]string(nil), current.Errors...),
	}
	if current.RefreshError != "" {
		view.Errors = append(view.Errors, current.RefreshError)
	}
	if len(view.Errors) > 2 {
		view.Errors = view.Errors[:2]
	}
	hasData := len(current.Meters) > 0 || len(current.Balances) > 0
	switch {
	case !hasData:
		view.State, view.Tone = "No data", "paused"
	case stale:
		view.State, view.Tone = "Out of date", "near"
	case len(current.Errors) > 0:
		view.State, view.Tone = "Partial", "near"
	default:
		view.State, view.Tone = "Up to date", "healthy"
	}
	if current.FetchedAt == nil {
		view.UpdatedLabel = "Not read yet"
	} else {
		view.UpdatedLabel = "Updated " + relativeLabel(*current.FetchedAt, now)
		switch current.Source {
		case cursorSourceOpen:
			view.UpdatedLabel += " · from OpenUsage"
		case cursorSourceNative:
			view.UpdatedLabel += " · from Cursor"
		}
	}
	if scheduled {
		view.Note = "Monthly pools schedule Cursor jobs. Grok Bot, on-demand and credits never do."
	} else {
		view.Note = "Usage only · not used for scheduling"
	}
	if hasData {
		view.NotReported = append([]string(nil), current.NotReported...)
	}
	for _, group := range cursorGroupHeadings {
		var rows []CursorMeterRow
		for _, meter := range current.Meters {
			if meter.Group == group.key {
				rows = append(rows, cursorMeterRow(meter, now))
			}
		}
		if len(rows) > 0 {
			view.Groups = append(view.Groups, CursorGroupView{Key: group.key, Heading: group.heading, Meters: rows})
		}
	}
	for _, balance := range current.Balances {
		row := CursorBalanceRow{
			Key: balance.Key, Label: balance.Label,
			ValueLabel: cursorAmount(balance.Available, balance.Unit) + " available",
		}
		var next time.Time
		for _, expiry := range balance.ExpiresAt {
			if expiry.After(now) && (next.IsZero() || expiry.Before(next)) {
				next = expiry
			}
		}
		if !next.IsZero() {
			row.NextExpiresAt = next.UTC().Format(time.RFC3339)
		}
		view.Balances = append(view.Balances, row)
	}
	return view
}

func cursorMeterRow(meter cursorMeter, now time.Time) CursorMeterRow {
	row := CursorMeterRow{Key: meter.Key, Label: meter.Label}
	if meter.ResetsAt != nil {
		row.ResetsAt = meter.ResetsAt.UTC().Format(time.RFC3339)
		row.ResetsInSeconds = secondsUntil(*meter.ResetsAt, now)
	}
	if meter.Limit == nil || *meter.Limit <= 0 {
		if meter.Unit == "usd" {
			row.ValueLabel = cursorAmount(meter.Used, meter.Unit) + " spent"
		} else {
			row.ValueLabel = cursorAmount(meter.Used, meter.Unit) + " used"
		}
		return row
	}
	limit := *meter.Limit
	left := math.Max(0, math.Min(100, (limit-meter.Used)/limit*100))
	row.HasLimit = true
	row.RemainingPercent = int(math.Round(left))
	row.Exhausted = meter.Used >= limit
	switch {
	case row.Exhausted:
		row.ValueLabel = "Used up"
	case meter.Unit == "percent" && limit == 100:
		row.ValueLabel = fmt.Sprintf("%d%% left", row.RemainingPercent)
	default:
		row.ValueLabel = cursorAmount(math.Max(0, limit-meter.Used), meter.Unit) + " of " + cursorAmount(limit, meter.Unit) + " left"
	}
	return row
}

func cursorAmount(value float64, unit string) string {
	switch unit {
	case "usd":
		return fmt.Sprintf("$%.2f", value)
	case "percent":
		return fmt.Sprintf("%d%%", int(math.Round(value)))
	}
	number := fmt.Sprintf("%.1f", value)
	if value == math.Trunc(value) {
		number = fmt.Sprintf("%d", int64(value))
	}
	return strings.TrimSpace(number + " " + unit)
}

// isMonthlyWindow reports a provider whose account window is a month rather
// than a week (Cursor), so the screen can name it correctly.
func isMonthlyWindow(period time.Duration) bool {
	return period > 8*24*time.Hour
}
