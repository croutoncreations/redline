package resetsignal

import (
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
)

func TestViableAnnouncement(t *testing.T) {
	now := time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC)
	published, end := now.Add(-time.Hour), now.Add(time.Hour)
	base := Signal{Feed: "resetbeacon", Kind: KindPromised, Freshness: Fresh, AnnouncementID: "123", ClaimedOriginalURL: "https://x.com/thsottiaux/status/123", OriginalAccount: "thsottiaux", Quote: "Global reset", PublishedAt: &published, Window: Window{End: &end, Certainty: WindowMirrorInterpretation}}
	for _, tc := range []struct {
		name   string
		change func(*Signal)
		want   Kind
	}{
		{"promise", func(*Signal) {}, KindPromised},
		{"scheduled ambiguous", func(s *Signal) { s.Kind = KindScheduled }, KindPromised},
		{"scheduled explicit", func(s *Signal) { s.Kind = KindScheduled; s.Window.Certainty = WindowExplicitPrimary }, KindScheduled},
		{"completed", func(s *Signal) { s.Kind = KindCompleted }, KindCompleted},
		{"odds only", func(s *Signal) { s.Kind = KindPrediction; p := 100.; s.Prediction24Percent = &p }, ""},
		{"intent", func(s *Signal) { s.Kind = KindIntent }, ""},
		{"banked", func(s *Signal) { s.Kind = KindBanked }, ""},
		{"unknown", func(s *Signal) { s.Kind = KindUnknown }, ""},
		{"unlinked completion", func(s *Signal) { s.Kind = KindCompleted; s.ClaimedOriginalURL = ""; s.AnnouncementID = "" }, ""},
		{"id only", func(s *Signal) { s.ClaimedOriginalURL = "" }, KindPromised},
		{"withdrawn", func(s *Signal) { s.Withdrawn = true }, ""},
		{"corrected", func(s *Signal) { s.Corrected = true }, ""},
		{"stale", func(s *Signal) { s.Freshness = Stale }, ""},
		{"missing ttl", func(s *Signal) { s.Freshness = MissingTTL }, ""},
		{"old", func(s *Signal) { v := now.Add(-73 * time.Hour); s.PublishedAt = &v }, ""},
		{"undated", func(s *Signal) { s.PublishedAt = nil }, ""},
		{"future", func(s *Signal) { v := now.Add(time.Minute); s.PublishedAt = &v }, ""},
		{"expired window", func(s *Signal) { s.Window.End = &published }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.change(&s)
			a := ViableAnnouncement(BuildReport(now, []SourceResult{{Signal: &s}}))
			if tc.want == "" {
				if a != nil {
					t.Fatalf("unexpected announcement: %+v", a)
				}
				return
			}
			if a == nil || a.Kind != tc.want || a.DirectSourceVerified {
				t.Fatalf("announcement=%+v want %s", a, tc.want)
			}
			if tc.name == "id only" && a.Key != "announcement:123" {
				t.Fatalf("key=%q", a.Key)
			}
		})
	}
	t.Run("selected unlinked completion blocks older linked promise", func(t *testing.T) {
		completed := Signal{Feed: "codex-reset", Kind: KindCompleted, Freshness: Fresh, PublishedAt: &now}
		if a := ViableAnnouncement(BuildReport(now, []SourceResult{{Signal: &base}, {Signal: &completed}})); a != nil {
			t.Fatalf("announcement=%+v", a)
		}
	})
	t.Run("mirrors and matching carried retraction", func(t *testing.T) {
		mirror := base
		mirror.Feed = "codex-reset"
		mirror.ClaimedOriginalURL = ""
		mirror.OriginalAccount = ""
		r := BuildReport(now, []SourceResult{{Signal: &base}, {Signal: &mirror}})
		a := ViableAnnouncement(r)
		if a == nil || strings.Join(a.Feeds, ",") != "codex-reset,resetbeacon" || a.Account != "thsottiaux" || a.URL != base.ClaimedOriginalURL {
			t.Fatalf("announcement=%+v", a)
		}
		mirror.RetractedAnnouncement = &AnnouncementRef{ID: "123"}
		mirror.AnnouncementID = ""
		mirror.ClaimedOriginalURL = ""
		mirror.Kind = KindBanked
		r = BuildReport(now, []SourceResult{{Signal: &base}, {Signal: &mirror}})
		if ViableAnnouncement(r) != nil || !AnnouncementRetracted(r, *a) {
			t.Fatal("matching carried retraction ignored")
		}
		mirror.RetractedAnnouncement.ID = "other"
		if AnnouncementRetracted(BuildReport(now, []SourceResult{{Signal: &mirror}}), *a) {
			t.Fatal("unrelated retraction applied")
		}
		mirror.RetractedAnnouncement.ID = "123"
		mirror.Freshness = Stale
		if AnnouncementRetracted(BuildReport(now, []SourceResult{{Signal: &mirror}}), *a) {
			t.Fatal("stale retraction applied")
		}
	})
}

func TestAnnouncementMessagesAndCandidate(t *testing.T) {
	end := time.Date(2026, 10, 13, 7, 59, 0, 0, time.UTC)
	a := Announcement{Key: "key", Kind: KindPromised, Account: "thsottiaux", Quote: "Global\n reset\x00   landing", QuoteSource: "original_post", URL: "https://x.com/thsottiaux/status/123", WindowEnd: &end, WindowCertainty: WindowMirrorInterpretation, Feeds: []string{"codex-reset", "resetbeacon"}}
	e := a.Notification(end, false)
	want := "Tibo (@thsottiaux): \"Global reset landing\". Global Codex reset promised by Tue 07:59 UTC (tracker's reading). Seen by codex-reset, resetbeacon."
	if e.Message != want || e.ProviderAccountID != "" || e.Type != "reset.announced" || e.Data["direct_source_verified"] != "false" || e.Data["quote"] != "Global reset landing" {
		t.Fatalf("event=%+v", e)
	}
	a.Kind = KindCompleted
	if !strings.HasPrefix(a.Notification(end, false).Message, "Tibo (@thsottiaux): \"Global reset landing\". Global Codex reset reported complete.") {
		t.Fatal("missing completion attribution")
	}
	if got := a.Notification(end, true); got.Message != "Retracted: the earlier Codex reset announcement from @thsottiaux was withdrawn or corrected." || got.Data["retracted"] != "true" {
		t.Fatalf("retraction=%+v", got)
	}
	a.Quote = strings.Repeat("世", 300)
	if len([]rune(a.Notification(end, false).Data["quote"])) > 280 {
		t.Fatal("quote not capped")
	}
	c := a.ResetEventCandidate()
	if c.Source != decision.ResetPredictedGlobal || !c.At.Equal(end) || strings.Join(c.Clears, ",") != "weekly" || c.Confidence != "unverified" || c.Detail != a.URL {
		t.Fatalf("candidate=%+v", c)
	}
	a.WindowEnd = nil
	if !a.ResetEventCandidate().At.IsZero() {
		t.Fatal("invented deadline")
	}
}
