package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
)

func TestBankedResetConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, block string
		leads       []time.Duration
		action      string
		lead        time.Duration
	}{
		{"disabled", "remind_before: []\n      on_expiry: off", []time.Duration{}, "off", time.Hour},
		{"automatic", "on_expiry: redeem", []time.Duration{24 * time.Hour}, "redeem", time.Hour},
		{"custom", "remind_before: [48h, 30m]\n      on_expiry: notify\n      redeem_lead: 15m", []time.Duration{48 * time.Hour, 30 * time.Minute}, "notify", 15 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(validConfig, "provider: codex", "provider: codex\n    banked_resets:\n      "+tc.block, 1) + "\nusage_monitor:\n  enabled: true\n"
			cfg, err := config.Load(writeConfig(t, raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Warnings) != 0 {
				t.Fatalf("warnings: %v", cfg.Warnings)
			}
			p := cfg.Providers["codex-main"]
			got := p.BankedResetRemindBefore()
			if len(got) != len(tc.leads) || p.BankedResetOnExpiry() != tc.action || p.BankedResetRedeemLead() != tc.lead {
				t.Fatalf("provider: %+v leads=%v", p, got)
			}
			for i := range got {
				if got[i] != tc.leads[i] {
					t.Fatalf("leads=%v", got)
				}
			}
		})
	}
}

func TestBankedResetRejectsInvalidConfiguration(t *testing.T) {
	for _, block := range []string{"remind_before: [0s]", "remind_before: [-1h]", "remind_before: [bad]", "redeem_lead: 0s", "redeem_lead: -1h", "redeem_lead: bad", "on_expiry: automatic"} {
		t.Run(block, func(t *testing.T) {
			raw := strings.Replace(validConfig, "provider: codex", "provider: codex\n    banked_resets:\n      "+block, 1)
			_, err := config.Load(writeConfig(t, raw))
			if err == nil || !strings.Contains(err.Error(), "banked_resets") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

// Redeem mode suppresses the redeem-now notification, so it is only accepted
// where automatic redemption can actually run: the usage monitor must be on
// and the provider must have itemized (native) reset telemetry.
func TestBankedResetRedeemRequiresRunnableAutomaticRedemption(t *testing.T) {
	monitor := "\nusage_monitor:\n  enabled: true\n"
	for _, tc := range []struct {
		name, provider, extra, want string
	}{
		{"monitor disabled", "on_expiry: redeem", "", "usage_monitor"},
		{"pinned openusage", "on_expiry: redeem\n    usage_source: openusage", monitor, "openusage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(validConfig, "provider: codex", "provider: codex\n    banked_resets:\n      "+tc.provider, 1) + tc.extra
			_, err := config.Load(writeConfig(t, raw))
			if err == nil || !strings.Contains(err.Error(), "on_expiry") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	t.Run("monitor enabled", func(t *testing.T) {
		raw := strings.Replace(validConfig, "provider: codex", "provider: codex\n    banked_resets:\n      on_expiry: redeem", 1) + monitor
		if _, err := config.Load(writeConfig(t, raw)); err != nil {
			t.Fatal(err)
		}
	})
}

// Upgrading must not start sending new event types to existing hooks: the
// default list stays the pre-firesale set, and new events are opt-in.
func TestResetNotificationEvents(t *testing.T) {
	defaults := config.Config{}.NotificationEvents()
	legacy := []string{"run.started", "run.completed", "run.failed", "scheduler.error"}
	if len(defaults) != len(legacy) {
		t.Fatalf("defaults=%v, want only %v", defaults, legacy)
	}
	for _, event := range legacy {
		if !defaults[event] {
			t.Fatalf("defaults=%v missing %s", defaults, event)
		}
	}
	for _, event := range []string{"reset.expiring", "reset.redeemed", "reset.redeem_failed", "firesale.started", "firesale.ended"} {
		cfg, err := config.Load(writeConfig(t, validConfig+"\nnotifications:\n  events: ["+event+"]\n"))
		if defaults[event] {
			t.Fatalf("new event %s is enabled by default", event)
		}
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.NotificationEvents()[event] {
			t.Fatalf("events=%v", cfg.NotificationEvents())
		}
	}
}

func TestBankedResetDefaults(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["codex-main"]
	leads := p.BankedResetRemindBefore()
	if len(leads) != 1 || leads[0] != 24*time.Hour || p.BankedResetOnExpiry() != "notify" || p.BankedResetRedeemLead() != time.Hour {
		t.Fatalf("banked reset defaults: leads=%v action=%s redeem lead=%s", leads, p.BankedResetOnExpiry(), p.BankedResetRedeemLead())
	}
}
