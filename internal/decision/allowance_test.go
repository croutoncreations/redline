package decision_test

import (
	"testing"
	"time"

	"github.com/jfox/redline/internal/decision"
)

// weeklyReset is a non-zero reset time used across allowance tests.
var weeklyReset = time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)

// shortReset is a non-zero short-window reset used across allowance tests.
var shortReset = time.Date(2026, 7, 16, 22, 0, 0, 0, time.UTC)

// snapshotWithShort builds a minimal valid UsageSnapshot that has a Short window.
func snapshotWithShort(remaining float64) decision.UsageSnapshot {
	return decision.UsageSnapshot{
		Provider:   "claude",
		ObservedAt: time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC),
		Short:      &decision.UsageWindow{Remaining: remaining, ResetsAt: shortReset},
		Weekly:     decision.UsageWindow{Remaining: 0.50, ResetsAt: weeklyReset},
		Source:     "test",
	}
}

// snapshotWithoutShort builds a minimal valid UsageSnapshot that has no Short window.
func snapshotWithoutShort() decision.UsageSnapshot {
	return decision.UsageSnapshot{
		Provider:   "codex",
		ObservedAt: time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC),
		Weekly:     decision.UsageWindow{Remaining: 0.60, ResetsAt: weeklyReset},
		Source:     "test",
	}
}

// TestAllowanceLookupSessionFromShortWindow verifies that Allowance("session")
// synthesizes the correct window fields from Short when no explicit allowance
// with key "session" is present. A wrong mapping here would feed incorrect
// remaining/reset data into pool eligibility checks in api/server.go.
func TestAllowanceLookupSessionFromShortWindow(t *testing.T) {
	s := snapshotWithShort(0.72)

	got, ok := s.Allowance("session")

	if !ok {
		t.Fatal("Allowance(\"session\") returned ok=false, want ok=true")
	}
	if got.Key != "session" {
		t.Errorf("Key = %q, want %q", got.Key, "session")
	}
	if got.Role != "short" {
		t.Errorf("Role = %q, want %q", got.Role, "short")
	}
	if got.Scope != "account" {
		t.Errorf("Scope = %q, want %q", got.Scope, "account")
	}
	if got.Remaining != 0.72 {
		t.Errorf("Remaining = %v, want 0.72", got.Remaining)
	}
	if !got.ResetsAt.Equal(shortReset) {
		t.Errorf("ResetsAt = %v, want %v", got.ResetsAt, shortReset)
	}
	wantPeriod := int64(decision.ShortWindowDuration / time.Second)
	if got.PeriodDurationSeconds != wantPeriod {
		t.Errorf("PeriodDurationSeconds = %d, want %d", got.PeriodDurationSeconds, wantPeriod)
	}
}

// TestAllowanceLookupSessionAbsentWhenShortIsNil verifies that Allowance("session")
// returns false when there is no Short window and no explicit "session" allowance.
// This protects against callers treating a missing short window as a full session.
func TestAllowanceLookupSessionAbsentWhenShortIsNil(t *testing.T) {
	s := snapshotWithoutShort()

	_, ok := s.Allowance("session")

	if ok {
		t.Fatal("Allowance(\"session\") returned ok=true for snapshot without Short window")
	}
}

// TestAllowanceLookupWeeklyWindow verifies that Allowance("weekly") synthesizes
// the correct window when no explicit "weekly" allowance is present. The period
// must be exactly 7 days in seconds so callers can compute utilisation rates.
func TestAllowanceLookupWeeklyWindow(t *testing.T) {
	s := snapshotWithoutShort()
	s.Weekly.Remaining = 0.35

	got, ok := s.Allowance("weekly")

	if !ok {
		t.Fatal("Allowance(\"weekly\") returned ok=false")
	}
	if got.Key != "weekly" {
		t.Errorf("Key = %q, want %q", got.Key, "weekly")
	}
	if got.Role != "weekly" {
		t.Errorf("Role = %q, want %q", got.Role, "weekly")
	}
	if got.Remaining != 0.35 {
		t.Errorf("Remaining = %v, want 0.35", got.Remaining)
	}
	wantPeriod := int64(7 * 24 * time.Hour / time.Second)
	if got.PeriodDurationSeconds != wantPeriod {
		t.Errorf("PeriodDurationSeconds = %d, want %d (7 days)", got.PeriodDurationSeconds, wantPeriod)
	}
}

// TestAllowanceExplicitKeyTakesPrecedence verifies that an explicit AllowanceWindow
// in Allowances beats the synthesized fallback for both "session" and "weekly".
// This matters because nativeusage populates explicit "session"/"weekly" entries
// with provider-reported values that must not be overwritten by local synthesis.
func TestAllowanceExplicitKeyTakesPrecedence(t *testing.T) {
	explicitSession := decision.AllowanceWindow{
		Key: "session", SourceLabel: "Provider Session", Scope: "account", Role: "short",
		Remaining: 0.99, ResetsAt: time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
		PeriodDurationSeconds: 3600,
	}
	s := snapshotWithShort(0.50) // Short.Remaining differs from explicitSession
	s.Allowances = []decision.AllowanceWindow{explicitSession}

	got, ok := s.Allowance("session")

	if !ok {
		t.Fatal("Allowance(\"session\") returned ok=false")
	}
	if got.Remaining != 0.99 {
		t.Errorf("Remaining = %v, want 0.99 (explicit wins over synthesized 0.50)", got.Remaining)
	}
	if got.SourceLabel != "Provider Session" {
		t.Errorf("SourceLabel = %q, want %q", got.SourceLabel, "Provider Session")
	}
}

// TestAllowanceLookupUnknownKeyReturnsFalse confirms that an arbitrary key
// that is neither an explicit allowance nor "session"/"weekly" returns false.
func TestAllowanceLookupUnknownKeyReturnsFalse(t *testing.T) {
	s := snapshotWithShort(0.50)

	_, ok := s.Allowance("fable")

	if ok {
		t.Fatal("Allowance(\"fable\") returned ok=true but no fable allowance exists")
	}
}

// TestAllowanceLookupExplicitCustomKey confirms that an explicit allowance with a
// non-standard key (e.g. "fable") is found by exact match.
func TestAllowanceLookupExplicitCustomKey(t *testing.T) {
	fable := decision.AllowanceWindow{
		Key: "fable", SourceLabel: "Fable", Scope: "model", Role: "weekly",
		Remaining: 0.80, ResetsAt: weeklyReset, PeriodDurationSeconds: 604800,
	}
	s := snapshotWithShort(0.50)
	s.Allowances = []decision.AllowanceWindow{fable}

	got, ok := s.Allowance("fable")

	if !ok {
		t.Fatal("Allowance(\"fable\") returned ok=false")
	}
	if got.Key != "fable" || got.Remaining != 0.80 {
		t.Errorf("got %+v", got)
	}
}

// TestAllAllowancesIncludesSynthesizedWindowsWhenNoExplicitAllowances verifies
// that AllAllowances synthesizes both "session" and "weekly" from the snapshot
// fields when the Allowances slice is empty. This is the path exercised when a
// provider populates only Short/Weekly and the DB stores all allowances via
// AllAllowances() — missing entries here would silently drop budget tracking.
func TestAllAllowancesIncludesSynthesizedWindowsWhenNoExplicitAllowances(t *testing.T) {
	s := snapshotWithShort(0.70)

	all := s.AllAllowances()

	found := make(map[string]decision.AllowanceWindow)
	for _, a := range all {
		found[a.Key] = a
	}
	session, hasSession := found["session"]
	if !hasSession {
		t.Error("AllAllowances() missing synthesized \"session\" entry")
	} else if session.Remaining != 0.70 {
		t.Errorf("session.Remaining = %v, want 0.70", session.Remaining)
	}
	if _, hasWeekly := found["weekly"]; !hasWeekly {
		t.Error("AllAllowances() missing synthesized \"weekly\" entry")
	}
}

// TestAllAllowancesDoesNotDuplicateExplicitSessionKey verifies that when the
// Allowances slice already contains "session", AllAllowances does NOT append a
// second synthesized one. Duplicates would cause double-writes to the snapshot
// store and incorrect budget display in the dashboard.
func TestAllAllowancesDoesNotDuplicateExplicitSessionKey(t *testing.T) {
	explicitSession := decision.AllowanceWindow{
		Key: "session", SourceLabel: "Provider Session", Scope: "account", Role: "short",
		Remaining: 0.88, ResetsAt: shortReset, PeriodDurationSeconds: 18000,
	}
	s := snapshotWithShort(0.50)
	s.Allowances = []decision.AllowanceWindow{explicitSession}

	all := s.AllAllowances()

	count := 0
	for _, a := range all {
		if a.Key == "session" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("\"session\" appears %d times in AllAllowances(), want exactly 1", count)
	}
}

// TestAllAllowancesDoesNotDuplicateExplicitWeeklyKey mirrors the session test
// for "weekly", as nativeusage may inject explicit weekly allowances.
func TestAllAllowancesDoesNotDuplicateExplicitWeeklyKey(t *testing.T) {
	explicitWeekly := decision.AllowanceWindow{
		Key: "weekly", SourceLabel: "Provider Weekly", Scope: "account", Role: "weekly",
		Remaining: 0.42, ResetsAt: weeklyReset, PeriodDurationSeconds: 604800,
	}
	s := snapshotWithShort(0.60)
	s.Allowances = []decision.AllowanceWindow{explicitWeekly}

	all := s.AllAllowances()

	count := 0
	for _, a := range all {
		if a.Key == "weekly" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("\"weekly\" appears %d times in AllAllowances(), want exactly 1", count)
	}
}

// TestAllAllowancesPreservesExplicitCustomKeys verifies that model-scoped custom
// keys (e.g. "fable") pass through AllAllowances unchanged alongside the
// synthesized session/weekly entries.
func TestAllAllowancesPreservesExplicitCustomKeys(t *testing.T) {
	fable := decision.AllowanceWindow{
		Key: "fable", SourceLabel: "Fable", Scope: "model", Role: "weekly",
		Remaining: 0.75, ResetsAt: weeklyReset, PeriodDurationSeconds: 604800,
	}
	s := snapshotWithShort(0.60)
	s.Allowances = []decision.AllowanceWindow{fable}

	all := s.AllAllowances()

	found := make(map[string]bool)
	for _, a := range all {
		found[a.Key] = true
	}
	if !found["fable"] {
		t.Error("AllAllowances() dropped explicit \"fable\" custom allowance")
	}
	if !found["session"] {
		t.Error("AllAllowances() did not add synthesized \"session\" alongside custom key")
	}
	if !found["weekly"] {
		t.Error("AllAllowances() did not add synthesized \"weekly\" alongside custom key")
	}
}

// TestAllAllowancesOmitsSessionWhenNoShortWindow confirms that AllAllowances
// does not inject a synthesized "session" entry when Short is nil. This prevents
// the store from writing a zero-valued session allowance row that would corrupt
// budget reads.
func TestAllAllowancesOmitsSessionWhenNoShortWindow(t *testing.T) {
	s := snapshotWithoutShort()

	all := s.AllAllowances()

	for _, a := range all {
		if a.Key == "session" {
			t.Errorf("AllAllowances() included \"session\" entry even though Short is nil: %+v", a)
		}
	}
}
