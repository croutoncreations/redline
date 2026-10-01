package primer_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/primer"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func scheduled(times []string, days []string) domain.PrimerSettings {
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Times, settings.Days, settings.Timezone = true, times, days, "America/Chicago"
	return settings
}

func TestNormalizeCanonicalizesTimesDaysAndDefaults(t *testing.T) {
	settings, err := primer.Normalize(domain.PrimerSettings{
		Mode: domain.PrimerSchedule, Enabled: true, Times: []string{"11:00", "6:00am", "06:00", " "},
		Days: []string{"weekdays"}, Prompt: "  ", Model: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := settings.Times; len(got) != 2 || got[0] != "06:00" || got[1] != "11:00" {
		t.Fatalf("times=%v", got)
	}
	if got := settings.Days; len(got) != 5 || got[0] != "mon" || got[4] != "fri" {
		t.Fatalf("days=%v", got)
	}
	if settings.Prompt != primer.DefaultPrompt || settings.Model != primer.DefaultModel {
		t.Fatalf("defaults not applied: %#v", settings)
	}
	all, err := primer.NormalizeDays([]string{"mon,tue", "wednesday", "thu", "fri", "weekends"})
	if err != nil || len(all) != 0 {
		t.Fatalf("every day must store as empty, got %v err=%v", all, err)
	}
}

func TestNormalizeRejectsInvalidSettings(t *testing.T) {
	for name, settings := range map[string]domain.PrimerSettings{
		"mode":          {Mode: "sometimes"},
		"time":          {Mode: domain.PrimerSchedule, Times: []string{"25:00"}},
		"no times":      {Mode: domain.PrimerSchedule, Enabled: true},
		"day":           {Mode: domain.PrimerSchedule, Days: []string{"funday"}},
		"timezone":      {Mode: domain.PrimerReset, Timezone: "Mars/Olympus"},
		"model":         {Mode: domain.PrimerReset, Model: "haiku; rm -rf /"},
		"catch up":      {Mode: domain.PrimerReset, CatchUpSeconds: int64(7 * time.Hour / time.Second)},
		"negative wait": {Mode: domain.PrimerReset, CatchUpSeconds: -1},
		"overflow":      {Mode: domain.PrimerReset, CatchUpSeconds: 9223372037},
	} {
		if _, err := primer.Normalize(settings); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := primer.Normalize(domain.PrimerSettings{Mode: domain.PrimerReset, Enabled: true}); err != nil {
		t.Fatalf("reset mode needs no times: %v", err)
	}
}

func TestScheduledSlotsHonorTimezoneDaysAndDaylightSaving(t *testing.T) {
	location := chicago(t)
	settings := scheduled([]string{"06:00"}, []string{"sat", "sun", "mon"})
	// US daylight saving ends Sunday 2026-11-01.
	from := time.Date(2026, 10, 30, 0, 0, 0, 0, location)
	slots := primer.ScheduledSlots(settings, from, from.AddDate(0, 0, 4))
	if len(slots) != 3 {
		t.Fatalf("slots=%v", slots)
	}
	wantUTC := []string{"2026-10-31T11:00:00Z", "2026-11-01T12:00:00Z", "2026-11-02T12:00:00Z"}
	for index, slot := range slots {
		if got := slot.Target.Format(time.RFC3339); got != wantUTC[index] {
			t.Errorf("slot %d target=%s want %s", index, got, wantUTC[index])
		}
		if slot.Target.In(location).Hour() != 6 {
			t.Errorf("slot %d is not 06:00 local: %s", index, slot.Target.In(location))
		}
		if slot.FireAt.Sub(slot.Target) != primer.FireDelay {
			t.Errorf("slot %d fires %s after target", index, slot.FireAt.Sub(slot.Target))
		}
	}
}

func TestScheduledSlotsSkipTimesThatDoNotExistOnSpringForward(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	// Clocks jump from 02:00 to 03:00 on 2026-03-08; 02:30 never happens.
	settings := scheduled([]string{"02:30", "06:00"}, nil)
	settings.Timezone = "America/New_York"
	from := time.Date(2026, 3, 7, 0, 0, 0, 0, location)
	slots := primer.ScheduledSlots(settings, from, from.AddDate(0, 0, 3))
	var got []string
	for _, slot := range slots {
		got = append(got, slot.Target.In(location).Format("01-02 15:04"))
	}
	want := []string{"03-07 02:30", "03-07 06:00", "03-08 06:00", "03-09 02:30", "03-09 06:00"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("slots=%v want %v", got, want)
	}
}

func TestDueScheduledFiresWithinCatchUpAndReportsMissedSlots(t *testing.T) {
	location := chicago(t)
	settings := scheduled([]string{"06:00", "11:00"}, nil)
	target := time.Date(2026, 9, 29, 6, 0, 0, 0, location)

	if due, _ := primer.DueScheduled(settings, target.Add(30*time.Second), target.Add(-time.Minute)); due != nil {
		t.Fatalf("a slot must not fire before its fire delay: %v", due)
	}
	due, missed := primer.DueScheduled(settings, target.Add(time.Minute), target.Add(30*time.Second))
	if due == nil || !due.Target.Equal(target) || len(missed) != 0 {
		t.Fatalf("due=%v missed=%v", due, missed)
	}
	// Waking 40 minutes late is inside the default 45-minute catch-up.
	if due, _ := primer.DueScheduled(settings, target.Add(40*time.Minute), target.Add(-8*time.Hour)); due == nil {
		t.Fatal("catch-up after sleep should still fire")
	}
	// Waking two hours late is not; the slot is reported missed instead.
	due, missed = primer.DueScheduled(settings, target.Add(2*time.Hour), target.Add(-8*time.Hour))
	if due != nil || len(missed) != 1 || !missed[0].Target.Equal(target) {
		t.Fatalf("due=%v missed=%v", due, missed)
	}
	// With no previous check (fresh start) nothing is reported missed.
	if _, missed := primer.DueScheduled(settings, target.Add(2*time.Hour), time.Time{}); len(missed) != 0 {
		t.Fatalf("fresh start should not invent missed slots: %v", missed)
	}
	// A time saved after it passed waits for tomorrow; firing now would
	// open the window at the wrong time.
	settings.UpdatedAt = target.Add(10 * time.Minute)
	if due, _ := primer.DueScheduled(settings, target.Add(12*time.Minute), target.Add(11*time.Minute)); due != nil {
		t.Fatalf("a slot older than the settings must not fire: %v", due)
	}
	settings.UpdatedAt = target.Add(-10 * time.Minute)
	if due, _ := primer.DueScheduled(settings, target.Add(12*time.Minute), target.Add(11*time.Minute)); due == nil {
		t.Fatal("a slot newer than the settings must still fire")
	}
}

func TestConsecutiveFailuresIgnoresSkipsAndInterruptions(t *testing.T) {
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	failed := func(minutes int) domain.PrimerAttempt {
		return domain.PrimerAttempt{Outcome: domain.PrimerFailed, Reason: "signed out", CompletedAt: at.Add(time.Duration(minutes) * time.Minute)}
	}
	recent := []domain.PrimerAttempt{
		{Outcome: domain.PrimerSkipped},
		{Outcome: domain.PrimerFailed, Reason: domain.PrimerInterruptedReason},
		failed(2), failed(1),
		{Outcome: domain.PrimerFired},
		failed(0),
	}
	run := primer.ConsecutiveFailures(recent)
	if run.Count != 2 || !run.Hard || !run.Last.Equal(at.Add(2*time.Minute)) {
		t.Fatalf("run=%+v", run)
	}
	if run := primer.ConsecutiveFailures([]domain.PrimerAttempt{{Outcome: domain.PrimerSkipped}}); run.Count != 0 {
		t.Fatalf("skips alone are not failures: %+v", run)
	}
}

func TestFailureRunCountsPingsThatOpenedNoWindow(t *testing.T) {
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	verifiedAt := at.Add(5 * time.Minute)
	noWindow := domain.PrimerAttempt{Outcome: domain.PrimerFired, Verification: domain.PrimerVerifyUnverified,
		CompletedAt: at, VerifiedAt: &verifiedAt}
	pending := domain.PrimerAttempt{Outcome: domain.PrimerFired, Verification: domain.PrimerVerifyPending}
	unknown := domain.PrimerAttempt{Outcome: domain.PrimerFired, Verification: domain.PrimerVerifyUnknown}
	inOpenWindow := noWindow
	reset := at.Add(2 * time.Hour)
	inOpenWindow.WindowResetsAt = &reset

	// Backoff counts from the verdict, not the ping; one such ping is not
	// yet alarming, two are.
	run := primer.ConsecutiveFailures([]domain.PrimerAttempt{pending, noWindow})
	if run.Count != 1 || run.Hard || run.Alarming() || !run.Last.Equal(verifiedAt) {
		t.Fatalf("run=%+v", run)
	}
	if run := primer.ConsecutiveFailures([]domain.PrimerAttempt{noWindow, noWindow}); run.Count != 2 || !run.Alarming() {
		t.Fatalf("run=%+v", run)
	}
	// A ping that landed in an open window, or could not be checked, ends
	// the run.
	for _, ender := range []domain.PrimerAttempt{inOpenWindow, unknown, {Outcome: domain.PrimerFired, Verification: domain.PrimerVerified}} {
		if run := primer.ConsecutiveFailures([]domain.PrimerAttempt{ender, noWindow}); run.Count != 0 {
			t.Fatalf("%+v must end the run: %+v", ender, run)
		}
	}
}

func TestNormalizeRejectsModelsThatLookLikeFlags(t *testing.T) {
	settings := primer.Defaults("claude-main")
	settings.Model = "--dangerously-skip-permissions"
	if _, err := primer.Normalize(settings); !errors.Is(err, primer.ErrInvalid) {
		t.Fatalf("err=%v, want ErrInvalid", err)
	}
	settings.Model = "claude-haiku-4-5"
	if _, err := primer.Normalize(settings); err != nil {
		t.Fatal(err)
	}
}

func TestNextScheduledSkipsExcludedDays(t *testing.T) {
	location := chicago(t)
	settings := scheduled([]string{"06:00"}, []string{"mon"})
	friday := time.Date(2026, 10, 2, 12, 0, 0, 0, location)
	next := primer.NextScheduled(settings, friday, nil)
	if next == nil || next.Target.In(location).Weekday() != time.Monday || next.Target.In(location).Day() != 5 {
		t.Fatalf("next=%v", next)
	}
}

func TestResetSlotFollowsKnownResetAndRetriesWhenIdle(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 9, 29, 13, 10, 0, 351_000_000, time.UTC)
	slot := primer.ResetSlot(&reset, now)
	if slot.Target.Format(time.RFC3339) != "2026-09-29T13:10:00Z" || slot.FireAt.Sub(slot.Target) != primer.FireDelay {
		t.Fatalf("slot=%#v", slot)
	}
	idle := primer.ResetSlot(nil, now.Add(7*time.Minute))
	if !idle.FireAt.Equal(now.Add(7*time.Minute)) || idle.Key != primer.ResetSlot(nil, now.Add(14*time.Minute)).Key {
		t.Fatalf("idle retries must share a key within the retry bucket: %#v", idle)
	}
	jitterA, jitterB := time.Date(2026, 9, 29, 13, 9, 59, 900_000_000, time.UTC), time.Date(2026, 9, 29, 13, 10, 0, 300_000_000, time.UTC)
	if primer.ResetSlot(&jitterA, now).Key != primer.ResetSlot(&jitterB, now).Key {
		t.Fatal("samples of one reset must share a slot key")
	}
	stale := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	if got := primer.ResetSlot(&stale, now); got.Kind != primer.SlotIdle {
		t.Fatalf("a long-past reset must fall back to idle retries: %#v", got)
	}
}

func TestForecastScheduledSkipsTargetsInsideAnEarlierWindow(t *testing.T) {
	location := chicago(t)
	settings := scheduled([]string{"06:00", "09:00", "11:00", "16:00"}, nil)
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, location)
	windows := primer.ForecastScheduled(settings, now, nil)
	if len(windows) != 3 {
		t.Fatalf("windows=%v", windows)
	}
	if windows[1].Start.In(location).Hour() != 11 || windows[2].Start.In(location).Hour() != 16 {
		t.Fatalf("09:00 falls inside the 06:00 window and should be skipped: %v", windows)
	}
}
