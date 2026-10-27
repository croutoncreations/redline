package notification

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

// WithMessage renders reset and firesale events without changing their data.
func WithMessage(event domain.NotificationEvent, provider string) domain.NotificationEvent {
	name := event.ProviderAccountID
	switch strings.ToLower(provider) {
	case "codex":
		name = "Codex"
	case "claude":
		name = "Claude"
	}
	d := event.Data
	switch event.Type {
	case domain.EventResetExpiring:
		event.Title = name + " banked reset expiring"
		count, _ := strconv.Atoi(d["count"])
		if count < 1 {
			count = 1
		}
		noun, verb := "reset", "expires"
		if count > 1 {
			noun, verb = "resets", "expire"
		}
		expires, _ := time.Parse(time.RFC3339, d["expires_at"])
		duration := expires.Sub(event.OccurredAt)
		relative := fmt.Sprintf("%dh", int(math.Ceil(duration.Hours())))
		if duration < time.Hour {
			relative = fmt.Sprintf("%dm", max(0, int(math.Ceil(duration.Minutes()))))
		}
		event.Message = fmt.Sprintf("%d %s %s in %s (%s).", count, noun, verb, relative, expires.UTC().Format("Mon 15:04 UTC"))
		if d["action"] == "redeem_now" {
			event.Message += " Redeem it now: redline reset redeem --provider " + event.ProviderAccountID
		}
	case domain.EventResetRedeemed:
		event.Title = name + " reset redeemed"
		event.Message = "Redeemed; the refill isn't visible yet. Check again in a few minutes."
		if d["verified"] == "true" {
			label, window := "Allowance", ""
			cleared := strings.Split(d["cleared"], ",")
			switch {
			case slices.Contains(cleared, "weekly"):
				label, window = "Weekly allowance", "weekly_remaining"
			case slices.Contains(cleared, "five_hour"):
				label, window = "5-hour window", "five_hour_remaining"
			}
			event.Message = label + " refilled."
			if remaining, err := strconv.ParseFloat(d[window], 64); window != "" && err == nil {
				event.Message = fmt.Sprintf("%s refilled (now %.0f%% remaining).", label, remaining*100)
			}
		}
	case domain.EventResetRedeemFailed:
		event.Title = name + " reset not redeemed"
		event.Message = d["reason"]
		switch event.Message {
		case "expired unused":
			event.Message = "Expired unused"
		case "redemption request outcome unknown at expiry; check the provider":
			event.Message = "Outcome unknown at expiry — check the provider"
		case "":
			event.Message = "Reset could not be redeemed. Check the provider."
		}
	case domain.EventFiresaleStarted:
		event.Title = name + " firesale started"
		if d["trigger"] == "reset_announcement" {
			deadline, _ := time.Parse(time.RFC3339Nano, d["deadline"])
			who := "Public trackers"
			if d["account"] == "thsottiaux" {
				who = "Tibo"
			}
			event.Message = who + " announced a reset by " + deadline.UTC().Format("Mon 15:04 UTC") + ". Running queued work before it lands."
			break
		}
		reset, _ := time.Parse(time.RFC3339, d["effective_reset_at"])
		deadline := reset.UTC().Format("Mon 15:04 UTC")
		if source := d["effective_reset_source"]; source != "" && source != "weekly" {
			label := strings.ReplaceAll(source, "_", " ")
			if source == "predicted_global" {
				label = "predicted global reset"
			}
			deadline = "the " + label + " at " + deadline
		}
		overflow, err := strconv.ParseFloat(d["overflow"], 64)
		if err == nil && d["overflow_available"] != "false" {
			event.Message = fmt.Sprintf("%.0f%% of weekly capacity would go unused before %s.", overflow*100, deadline)
		} else {
			gap, _ := strconv.ParseFloat(d["pace_gap"], 64)
			event.Message = fmt.Sprintf("%.0f%% behind weekly pace before %s.", gap*100, deadline)
		}
		event.Message += " All job tiers unlocked."
	case domain.EventFiresaleEnded:
		event.Title = name + " firesale ended"
		event.Message = "Back to normal pacing."
		if d["trigger"] == "reset_announcement" {
			reason := map[string]string{
				"deadline_passed": "The announced reset deadline passed.",
				"retracted":       "The reset announcement was retracted.",
				"completed":       "The reset was reported complete.",
				"stopped":         "Stopped by you.",
				"disabled":        "Reset announcement firesales are disabled.",
			}[d["reason"]]
			if d["reason"] == "completed" && d["completion_source"] == "usage_refill" {
				reason = "Your weekly allowance refilled, so the reset has landed."
			}
			event.Message = reason + " " + event.Message
		}
	}
	return event
}
