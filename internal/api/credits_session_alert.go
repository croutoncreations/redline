package api

import (
	"context"
	"log"
	"sync"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
	"github.com/croutoncreations/redline/internal/usage"
)

// creditsSessionAlerts remembers which API credits accounts were already
// told their session key is needed, so one expiry sends one alert. Like the
// Claude profile sign-in alert it is not persisted: a restart repeats it once.
type creditsSessionAlerts struct {
	mu       sync.Mutex
	notified map[string]bool
}

// noteCreditsSession sends account.session_key_needed once when an API
// credits account's balance can't be read because its session key is missing
// or rejected, and forgets it once a read succeeds. Other failures (network,
// rate limits, bad data) are not a session problem and change nothing.
func (s *Server) noteCreditsSession(ctx context.Context, providerID string, configured config.Provider, fetchErr error) {
	if !configured.IsAPICredits() {
		return
	}
	alerts := &s.creditsSessions
	alerts.mu.Lock()
	if alerts.notified == nil {
		alerts.notified = map[string]bool{}
	}
	switch {
	case fetchErr == nil:
		delete(alerts.notified, providerID)
		alerts.mu.Unlock()
		return
	case usage.FailureReason(fetchErr) != usage.ReasonSignedOut || alerts.notified[providerID]:
		alerts.mu.Unlock()
		return
	}
	// Set before delivery so concurrent checks don't both send; a failed
	// delivery clears it below.
	alerts.notified[providerID] = true
	alerts.mu.Unlock()
	if s.notifier == nil {
		return
	}
	label := configured.Label
	if label == "" {
		label = providerID
	}
	event := notification.WithMessage(domain.NotificationEvent{
		Version: 1, Type: domain.EventAccountSessionKeyNeeded, OccurredAt: s.now().UTC(), ProviderAccountID: providerID,
		Data: map[string]string{"label": label},
	}, configured.Provider)
	if err := s.notifier.Notify(ctx, event); err != nil {
		alerts.mu.Lock()
		delete(alerts.notified, providerID)
		alerts.mu.Unlock()
		log.Printf("redline %s session-key notification failed: %v", providerID, err)
	}
}
