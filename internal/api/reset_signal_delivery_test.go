package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/resetsignal"
)

type failingResetNotifier struct{ calls int }

func (n *failingResetNotifier) Notify(context.Context, domain.NotificationEvent) error {
	n.calls++
	return errors.New("hook failed")
}

func TestResetSignalDeliveryFailureClaimsBeforeSending(t *testing.T) {
	s, _, _ := redemptionServer(t, "codex")
	enableResetSignalNotifications(s)
	n := &failingResetNotifier{}
	s.notifier = n
	pub := s.now().Add(-time.Hour)
	signal := resetsignal.Signal{Kind: resetsignal.KindPromised, Freshness: resetsignal.Fresh, AnnouncementID: "123", PublishedAt: &pub}
	installResetSignalFixture(t, s, &signal)
	for _, kind := range []resetsignal.Kind{resetsignal.KindPromised, resetsignal.KindCompleted} {
		signal.Kind = kind
		if err := s.pollResetSignals(t.Context(), ""); err == nil {
			t.Fatal("hook failure hidden")
		}
		if len(s.ResetSignalState().Errors) == 0 {
			t.Fatal("hook failure missing from cached state")
		}
		before := n.calls
		if err := s.pollResetSignals(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
		if n.calls != before {
			t.Fatal("failed notification retried")
		}
	}
	signal.Withdrawn = true
	if err := s.pollResetSignals(t.Context(), ""); err == nil {
		t.Fatal("retraction hook failure hidden")
	}
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if n.calls != 3 {
		t.Fatalf("calls=%d", n.calls)
	}
}
