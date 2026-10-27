package notification_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
)

func TestResetAnnouncedManualSample(t *testing.T) {
	now := time.Now().UTC()
	for _, account := range []string{"", "claude-main"} {
		e, err := notification.Sample(domain.EventResetAnnounced, account, "claude", now)
		if err != nil {
			t.Fatal(err)
		}
		if account == "" {
			if e.Data["kind"] != "COMPLETED" || e.Data["manual_account"] != "" {
				t.Fatalf("event=%+v", e)
			}
		} else {
			if e.Data["kind"] != "PROMISED" || e.Data["manual_account"] != account || e.Data["manual_for"] != "15m" {
				t.Fatalf("event=%+v", e)
			}
			end, err := time.Parse(time.RFC3339Nano, e.Data["window_end"])
			if err != nil || !end.Equal(now.Add(2*time.Hour)) {
				t.Fatalf("window_end=%s err=%v", e.Data["window_end"], err)
			}
		}
	}
}

func TestManualFiresaleMessages(t *testing.T) {
	for _, by := range []string{"owner", "device:My phone"} {
		e := notification.WithMessage(domain.NotificationEvent{Type: domain.EventFiresaleStarted, Data: map[string]string{"trigger": "manual", "started_by": by, "deadline": "2026-10-26T15:04:00Z"}}, "Claude main")
		who := "you"
		if by != "owner" {
			who = "My phone"
		}
		if e.Message != "Claude main firesale started by "+who+" until Mon 15:04 UTC. Running queued work." {
			t.Fatal(e.Message)
		}
	}
	e := notification.WithMessage(domain.NotificationEvent{Type: domain.EventFiresaleEnded, Data: map[string]string{"trigger": "manual", "reason": "completed"}}, "Claude")
	if e.Message != "Firesale ended: the weekly window reset. Back to normal pacing." {
		t.Fatal(e.Message)
	}
}

func TestSamplesUseReadableBuilders(t *testing.T) {
	now := time.Date(2026, 7, 15, 19, 19, 0, 0, time.UTC)
	for _, kind := range notification.EventTypes() {
		t.Run(kind, func(t *testing.T) {
			e, err := notification.Sample(kind, "codex-main", "codex", now)
			if err != nil || e.Type != kind || e.Data["test"] != "true" || !strings.HasPrefix(e.Title, "Test: ") || e.Message == "" {
				t.Fatalf("event=%+v err=%v", e, err)
			}
			var keys []string
			switch kind {
			case domain.EventResetExpiring:
				keys = []string{"reset_id", "expires_at", "lead", "action", "count"}
			case domain.EventResetRedeemed, domain.EventResetRedeemFailed:
				keys = []string{"reset_id", "request_id", "outcome", "code", "reason", "verified"}
			case domain.EventFiresaleStarted, domain.EventFiresaleEnded:
				// announcement_key lets a phone show its Stop action on a test
				// alert; the sample key never matches a real sale.
				keys = []string{"pace_gap", "overflow", "effective_reset_at", "unlocked_tier", "announcement_key", "trigger"}
			case domain.EventResetAnnounced:
				keys = []string{"key", "kind", "url", "quote", "quote_source", "feeds", "attribution", "attribution_urls", "direct_source_verified"}
				if e.ProviderAccountID != "" || e.Data["url"] == "" || !strings.Contains(e.Message, "Data: ") {
					t.Fatalf("announcement sample=%+v", e)
				}
			}
			for _, key := range keys {
				if _, ok := e.Data[key]; !ok {
					t.Errorf("sample missing %s: %+v", key, e)
				}
			}
			if kind != domain.EventResetAnnounced && (strings.HasPrefix(kind, "reset.") || strings.HasPrefix(kind, "firesale.")) {
				want := notification.WithMessage(domain.NotificationEvent{Type: kind, ProviderAccountID: e.ProviderAccountID, OccurredAt: now, Data: e.Data}, "codex")
				if e.Title != "Test: "+want.Title || e.Message != want.Message {
					t.Fatalf("sample=%+v want=%+v", e, want)
				}
			}
		})
	}
	if !slices.Contains(notification.EventTypes(), domain.EventResetAnnounced) {
		t.Fatal("reset.announced has no test sample")
	}
	if _, err := notification.Sample("oops", "codex-main", "codex", now); err == nil {
		t.Fatal("unknown event accepted")
	}
}
