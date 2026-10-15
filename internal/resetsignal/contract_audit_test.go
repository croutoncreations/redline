package resetsignal

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These tests pin the codex-reset.com/developers and resetbeacon.com/api/docs
// contracts against the whole-module audit of PR #172 (findings F1-F4).

func auditTime(offset time.Duration) string {
	return fixtureNow.Add(offset).Format(time.RFC3339)
}

func auditCodexHeader() http.Header {
	return http.Header{"X-Published-Expires-At": {auditTime(2 * time.Minute)}}
}

// keepsBankedState reports whether the record's JSON still carries the
// supplemental banked fact.
func keepsBankedState(t *testing.T, signal Signal) bool {
	t.Helper()
	encoded, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(encoded), `"banked_state":"available"`)
}

func auditCodexReport(t *testing.T, signal Signal) Report {
	t.Helper()
	signal.Feed = "codex-reset"
	return BuildReport(fixtureNow, []SourceResult{{Signal: &signal}})
}

// F1. codex-reset: "A reset is real when ... /api/forecast last_reset_at moves
// forward" and "Banked resets are a separate fact ... Treat it as
// supplemental." A banked state must not hide a documented completion, nor a
// confirmed reset alert.
func TestCodexResetBankedStateIsSupplementalToACompletion(t *testing.T) {
	body := `{"mode":"model","updated_at":"` + auditTime(-time.Minute) + `","banked_state":"available","last_reset_at":"` + auditTime(-2*time.Hour) + `",
		"probabilities":{"rounded_24h":18,"rounded_48h":33},"official_signal":null}`
	signal, err := normalizeCodexReset([]byte(body), auditCodexHeader(), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if signal.Kind != KindCompleted || signal.PublishedAt == nil || !signal.PublishedAt.Equal(fixtureNow.Add(-2*time.Hour)) ||
		!keepsBankedState(t, signal) || !contains(signal.Warnings, `banked_state "available" is a separate, supplemental fact`) {
		t.Fatalf("last_reset_at beside banked_state: signal = %+v", signal)
	}
	if report := auditCodexReport(t, signal); report.State != KindCompleted {
		t.Fatalf("last_reset_at beside banked_state: report = %+v", report)
	}

	confirmed := `{"mode":"model","updated_at":"` + auditTime(-time.Minute) + `","banked_state":"available",
		"probabilities":{"rounded_24h":18,"rounded_48h":33},"official_signal":null,
		"latest_alert":{"id":"9","kind":"reset","state":"confirmed","source_at":"` + auditTime(-time.Hour) + `","url":"https://x.com/a/status/9"}}`
	alerted, err := normalizeCodexReset([]byte(confirmed), auditCodexHeader(), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if alerted.Kind != KindCompleted || alerted.Withdrawn || !keepsBankedState(t, alerted) {
		t.Fatalf("confirmed reset alert beside banked_state: signal = %+v", alerted)
	}
	if report := auditCodexReport(t, alerted); report.State != KindCompleted {
		t.Fatalf("confirmed reset alert beside banked_state: report = %+v", report)
	}
}

// F2. codex-reset: "Depend only on documented fields ... Anything else in a
// payload may move or disappear." The documented last_reset_at decides the
// completion; the undocumented latest_alert never relabels or retracts it,
// whatever the ordering. A retracting alert retracts only the announcement it
// names, carried as RetractedAnnouncement.
func TestCodexResetLastResetAtOutranksLatestAlert(t *testing.T) {
	lastReset := -2 * time.Hour
	for name, test := range map[string]struct {
		state      string
		sourceAt   time.Duration
		retracts   bool
		linkedToID string
	}{
		"withdrawn older":  {"withdrawn", -30 * time.Hour, true, ""},
		"withdrawn equal":  {"withdrawn", lastReset, true, ""},
		"withdrawn newer":  {"withdrawn", -time.Hour, true, ""},
		"mystery newer":    {"mystery", -time.Hour, true, ""},
		"active older":     {"active", -30 * time.Hour, false, ""},
		"active equal":     {"active", lastReset, false, ""},
		"active newer":     {"active", -time.Hour, false, ""},
		"confirmed newer":  {"confirmed", -time.Hour, false, "111"},
		"confirmed equal":  {"confirmed", lastReset, false, "111"},
		"confirmed older":  {"confirmed", -30 * time.Hour, false, "111"},
		"withdrawn banked": {"withdrawn", -time.Hour, true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			banked := "null"
			if strings.HasSuffix(name, "banked") {
				banked = `"available"`
			}
			body := `{"mode":"model","updated_at":"` + auditTime(-time.Minute) + `","banked_state":` + banked + `,"last_reset_at":"` + auditTime(lastReset) + `",
				"probabilities":{"rounded_24h":18,"rounded_48h":33},"official_signal":null,
				"latest_alert":{"id":"111","kind":"reset","state":"` + test.state + `","source_at":"` + auditTime(test.sourceAt) + `","url":"https://x.com/thsottiaux/status/111"}}`
			signal, err := normalizeCodexReset([]byte(body), auditCodexHeader(), fixtureNow)
			if err != nil {
				t.Fatal(err)
			}
			if signal.Kind != KindCompleted || signal.Withdrawn || signal.Corrected || signal.AnnouncementID != test.linkedToID {
				t.Fatalf("signal = %+v", signal)
			}
			if test.retracts != (signal.RetractedAnnouncement != nil) ||
				(test.retracts && *signal.RetractedAnnouncement != AnnouncementRef{ID: "111", URL: "https://x.com/thsottiaux/status/111"}) {
				t.Fatalf("retraction carried as %+v on %+v", signal.RetractedAnnouncement, signal)
			}
			if report := auditCodexReport(t, signal); report.State != KindCompleted {
				t.Fatalf("report = %+v", report)
			}
		})
	}

	// The carried retraction still retracts another tracker's record of
	// announcement 111, and never the documented completion itself.
	body := `{"mode":"model","updated_at":"` + auditTime(-time.Minute) + `","last_reset_at":"` + auditTime(lastReset) + `",
		"probabilities":{"rounded_24h":18,"rounded_48h":33},"official_signal":null,
		"latest_alert":{"id":"111","kind":"reset","state":"withdrawn","source_at":"` + auditTime(-time.Hour) + `","url":"https://x.com/thsottiaux/status/111"}}`
	codex, err := normalizeCodexReset([]byte(body), auditCodexHeader(), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	codex.Feed = "codex-reset"
	announced := fixtureNow.Add(-30 * time.Minute)
	promise := Signal{Feed: "resetbeacon", Kind: KindPromised, AnnouncementID: "111", PublishedAt: &announced, Freshness: Fresh}
	if retracted := retractedByAnotherRecord(promise, []SourceResult{{Signal: &codex}}, fixtureNow); !retracted {
		t.Fatalf("announcement 111 not retracted by %+v", codex)
	}
	if report := BuildReport(fixtureNow, []SourceResult{{Signal: &codex}, {Signal: &promise}}); report.State != KindUnknown || !strings.Contains(report.Summary, "retracted") {
		t.Fatalf("report with the retracted promise = %+v", report)
	}
	other := Signal{Feed: "resetbeacon", Kind: KindPromised, AnnouncementID: "112", PublishedAt: &announced, Freshness: Fresh}
	if report := BuildReport(fixtureNow, []SourceResult{{Signal: &codex}, {Signal: &other}}); report.State != KindPromised {
		t.Fatalf("an unrelated newer promise was retracted: %+v", report)
	}

	// Without a fresh last_reset_at the existing fail-closed behaviour stays:
	// a recent retracting alert names and withdraws the fallback record.
	for name, lastResetAt := range map[string]string{"absent": `null`, "old": `"` + auditTime(-80*time.Hour) + `"`} {
		t.Run("last_reset_at "+name, func(t *testing.T) {
			body := `{"mode":"model","updated_at":"` + auditTime(-time.Minute) + `","last_reset_at":` + lastResetAt + `,
				"probabilities":{"rounded_24h":18,"rounded_48h":33},"official_signal":null,
				"latest_alert":{"id":"111","kind":"reset","state":"withdrawn","source_at":"` + auditTime(-30*time.Hour) + `","url":"https://x.com/thsottiaux/status/111"}}`
			signal, err := normalizeCodexReset([]byte(body), auditCodexHeader(), fixtureNow)
			if err != nil {
				t.Fatal(err)
			}
			if signal.Kind != KindPrediction || !signal.Withdrawn || signal.AnnouncementID != "111" {
				t.Fatalf("signal = %+v", signal)
			}
			if report := auditCodexReport(t, signal); report.State != KindUnknown {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

// F3. codex-reset: /api/forecast carries "Reset probability for the next 24h
// and 48h". The probabilities are current for as long as the snapshot is
// fresh; an official signal that is old or past its window ages separately
// and must not file them as historical.
func TestCodexResetFreshProbabilitiesStayCurrentBesideAgedOfficialSignal(t *testing.T) {
	for name, official := range map[string]string{
		"older than 72 hours": `{"tweet_id":"222","summary":"x","at":"` + auditTime(-80*time.Hour) + `","url":"https://x.com/thsottiaux/status/222","signal_type":"promised"}`,
		"window ended": `{"tweet_id":"222","summary":"x","at":"` + auditTime(-3*time.Hour) + `","url":"https://x.com/thsottiaux/status/222","signal_type":"prediction",
			"window":{"start":"` + auditTime(-150*time.Minute) + `","end":"` + auditTime(-time.Hour) + `","text":"within two hours"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"mode":"model","updated_at":"` + auditTime(-time.Minute) + `","probabilities":{"rounded_24h":18,"rounded_48h":33},"official_signal":` + official + `}`
			signal, err := normalizeCodexReset([]byte(body), auditCodexHeader(), fixtureNow)
			if err != nil {
				t.Fatal(err)
			}
			if signal.Freshness != Fresh || isCurrent(signal, fixtureNow) {
				t.Fatalf("the aged official claim is current: signal = %+v", signal)
			}
			report := auditCodexReport(t, signal)
			if len(report.CurrentPredictions) != 1 || len(report.HistoricalPredictions) != 0 ||
				value(report.CurrentPredictions[0].Percent24) != 18 || value(report.CurrentPredictions[0].Percent48) != 33 || !report.CurrentPredictions[0].Current {
				t.Fatalf("predictions current=%+v historical=%+v", report.CurrentPredictions, report.HistoricalPredictions)
			}
			if report.State != KindPrediction || len(report.Evidence) != 1 || report.Evidence[0].AnnouncementID != "222" {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

// F4. Reset Beacon: "Possible, announced and confirmed events are distinct",
// the likely topic is "a possible reset backed by a public source", and "Do
// not reconstruct a percentage". A source-backed possible record publishes no
// probability, so the summary must not describe tracker probabilities.
func TestResetBeaconSourceBackedPossibleIsNotSummarizedAsProbabilities(t *testing.T) {
	for _, state := range []string{"possible", "likely"} {
		t.Run(state, func(t *testing.T) {
			body := `{"calculatedAt":"` + auditTime(-time.Minute) + `","validUntil":"` + auditTime(40*time.Minute) + `","staleSince":null,"publicationState":"` + state + `",
				"answer":{"state":"` + state + `","deadline":null,"posts":[{"postId":"333","sourceUrl":"https://x.com/thsottiaux/status/333","quote":"q","publishedAt":"` + auditTime(-time.Hour) + `","authorHandle":"thsottiaux"}]}}`
			signal, err := normalizeResetBeacon([]byte(body), nil, fixtureNow)
			if err != nil {
				t.Fatal(err)
			}
			signal.Feed = "resetbeacon"
			report := BuildReport(fixtureNow, []SourceResult{{Signal: &signal}})
			if report.State != KindPrediction || len(report.CurrentPredictions) != 0 ||
				strings.Contains(report.Summary, "probabilities are shown") ||
				report.Summary != "A fresh tracker reports a possible, source-backed event; no probability is published, and this is not Redline confidence." {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}
