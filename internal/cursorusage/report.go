// Package cursorusage tracks Cursor subscription meters, including the
// separate Grok Bot weekly allowance, for display only.
//
// Nothing here feeds the scheduler. Cursor's monthly model pools, Grok Bot's
// weekly allowance, on-demand spend and credit balances do not fit Redline's
// scheduling snapshot (which models Claude and Codex five-hour and weekly
// windows), and none of them gate Claude or Codex work. Reports are kept in
// memory and surfaced on the dashboard alongside, never inside, provider
// accounts.
package cursorusage

import (
	"regexp"
	"strings"
	"time"
)

// Source names where a report came from.
const (
	SourceOpenUsage = "openusage"
	SourceNative    = "cursor"
)

// Meter groups. Monthly pools share Cursor's billing cycle; Grok Bot has its
// own weekly allowance; on-demand is metered spend, never included quota.
const (
	GroupMonthly  = "monthly"
	GroupWeekly   = "weekly"
	GroupOnDemand = "on_demand"
)

// Report is one observation of a Cursor account. Absent meters are listed in
// NotReported rather than invented as zero.
type Report struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Plan  string `json:"plan,omitempty"`
	// Source is "openusage" or "cursor" (Redline's built-in collector). Empty
	// until the first successful observation.
	Source    string     `json:"source,omitempty"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
	// ExpiresAt is when this observation stops being current. OpenUsage
	// supplies its own; native reports derive it from the poll interval.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Stale is evaluated against the server clock when the report is served.
	// Clients should re-check ExpiresAt against their own clock.
	Stale bool `json:"stale"`
	// Errors are partial failures that accompanied this observation; meters
	// that did arrive are still valid.
	Errors []string `json:"errors,omitempty"`
	// RefreshError explains why the most recent refresh failed while an
	// older observation is still being shown.
	RefreshError string    `json:"refresh_error,omitempty"`
	Meters       []Meter   `json:"meters"`
	Balances     []Balance `json:"balances"`
	NotReported  []string  `json:"not_reported,omitempty"`
}

// Meter is consumption against an allowance. Limit is nil when the provider
// gave no denominator; consumers must not invent one.
type Meter struct {
	Key           string     `json:"key"`
	Label         string     `json:"label"`
	Group         string     `json:"group"`
	Unit          string     `json:"unit"`
	Used          float64    `json:"used"`
	Limit         *float64   `json:"limit,omitempty"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	WindowSeconds *int64     `json:"window_seconds,omitempty"`
}

// Balance is an amount available to spend, such as credits.
type Balance struct {
	Key       string      `json:"key"`
	Label     string      `json:"label"`
	Unit      string      `json:"unit"`
	Available float64     `json:"available"`
	ExpiresAt []time.Time `json:"expires_at,omitempty"`
}

// resource describes one public Cursor resource and how it is presented.
type resource struct {
	key, label, group string
	balance           bool
}

// resources is the display order. Keys match OpenUsage's public Cursor
// resources so both collectors produce the same report.
var resources = []resource{
	{key: "autoUsage", label: "Cursor Models", group: GroupMonthly},
	{key: "apiUsage", label: "Other Models", group: GroupMonthly},
	{key: "totalUsage", label: "Total usage", group: GroupMonthly},
	{key: "requests", label: "Requests", group: GroupMonthly},
	{key: "grokBot", label: "Grok Bot", group: GroupWeekly},
	{key: "onDemand", label: "On-demand", group: GroupOnDemand},
	{key: "credits", label: "Credits", balance: true},
}

// expected lists meters whose absence is worth telling the user about. The
// rest (requests, total, on-demand) are plan-dependent and simply omitted.
var expected = []string{"autoUsage", "apiUsage", "grokBot"}

func resourceFor(key string) (resource, bool) {
	for _, candidate := range resources {
		if candidate.key == key {
			return candidate, true
		}
	}
	return resource{}, false
}

func newReport() Report {
	return Report{ID: "cursor", Label: "Cursor", Meters: []Meter{}, Balances: []Balance{}}
}

// finish orders meters and fills NotReported.
func (r *Report) finish() {
	ordered := make([]Meter, 0, len(r.Meters))
	for _, candidate := range resources {
		for _, meter := range r.Meters {
			if meter.Key == candidate.key {
				ordered = append(ordered, meter)
			}
		}
	}
	r.Meters = ordered
	r.NotReported = nil
	for _, key := range expected {
		if !r.hasMeter(key) {
			definition, _ := resourceFor(key)
			r.NotReported = append(r.NotReported, definition.label)
		}
	}
	for i := range r.Errors {
		r.Errors[i] = sanitizeMessage(r.Errors[i])
	}
}

func (r Report) hasMeter(key string) bool {
	for _, meter := range r.Meters {
		if meter.Key == key {
			return true
		}
	}
	return false
}

// HasData reports whether any meter or balance was observed.
func (r Report) HasData() bool { return len(r.Meters) > 0 || len(r.Balances) > 0 }

// At returns a copy with Stale evaluated at now. A report without an expiry
// is never presented as current.
// StaleGrace is how long past expires_at a report stays current. OpenUsage
// writes its next Cursor card a few seconds after the previous one expires,
// so a report only counts as out of date once the source has missed its
// refresh, not during every normal rotation. Clients use the same grace.
const StaleGrace = 2 * time.Minute

func (r Report) At(now time.Time) Report {
	copied := r
	copied.Meters = append([]Meter{}, r.Meters...)
	copied.Balances = append([]Balance{}, r.Balances...)
	copied.Errors = append([]string(nil), r.Errors...)
	copied.NotReported = append([]string(nil), r.NotReported...)
	copied.Stale = r.FetchedAt != nil && (r.ExpiresAt == nil || !now.Before(r.ExpiresAt.Add(StaleGrace)))
	return copied
}

var (
	jwtLike     = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`)
	bearerLike  = regexp.MustCompile(`(?i)(bearer|token|cookie)[=: ]+\S+`)
	homePathish = regexp.MustCompile(`(/Users|/home)/[^\s"']+`)
)

// sanitizeMessage keeps provider error text short and free of anything that
// looks like a credential or a local path. Errors are shown on a phone.
func sanitizeMessage(message string) string {
	message = jwtLike.ReplaceAllString(message, "[redacted]")
	message = bearerLike.ReplaceAllString(message, "$1 [redacted]")
	message = homePathish.ReplaceAllString(message, "[path]")
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 200 {
		message = strings.TrimSpace(message[:197]) + "..."
	}
	return message
}

func timePointer(value time.Time) *time.Time { return &value }
func floatPointer(value float64) *float64    { return &value }
func intPointer(value int64) *int64          { return &value }
