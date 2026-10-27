package notification_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
)

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
				keys = []string{"pace_gap", "overflow", "effective_reset_at", "unlocked_tier"}
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
