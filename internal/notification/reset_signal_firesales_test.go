package notification_test

import (
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
)

func TestResetAnnouncementFiresaleMessages(t *testing.T) {
	event := domain.NotificationEvent{Type: domain.EventFiresaleStarted, Data: map[string]string{"trigger": "reset_announcement", "deadline": "2026-10-25T08:15:00Z", "account": "thsottiaux"}}
	got := notification.WithMessage(event, "codex")
	if got.Message != "Tibo announced a reset by Sun 08:15 UTC. Running queued work before it lands." {
		t.Fatal(got.Message)
	}
	event.Type = domain.EventFiresaleEnded
	for reason, want := range map[string]string{"deadline_passed": "The announced reset deadline passed.", "retracted": "The reset announcement was retracted.", "completed": "The reset was reported complete.", "stopped": "Stopped by you.", "disabled": "Reset announcement firesales are disabled."} {
		event.Data["reason"] = reason
		got = notification.WithMessage(event, "codex")
		if got.Message != want+" Back to normal pacing." {
			t.Fatalf("%s: %q", reason, got.Message)
		}
	}
}
