package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

func TestWeeklyPolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		invalid     bool
	}{
		{"default", "", false},
		{"session only default", "    weekly_policy: session_only\n", false},
		{"explicit zero", "    weekly_policy: session_only\n    session_reserve: 0\n", false},
		{"custom reserve", "    weekly_policy: session_only\n    session_reserve: 0.75\n", false},
		{"unknown policy", "    weekly_policy: other\n", true},
		{"negative", "    weekly_policy: session_only\n    session_reserve: -0.1\n", true},
		{"one", "    weekly_policy: session_only\n    session_reserve: 1\n", true},
		{"nan", "    weekly_policy: session_only\n    session_reserve: .nan\n", true},
		{"reserve without policy", "    session_reserve: 0\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Replace(claudeConfig, "    window_weekly_cost: 0.08\n", "    window_weekly_cost: 0.08\n"+tc.extra, 1)
			cfg, err := config.Load(writeConfig(t, text))
			if (err != nil) != tc.invalid {
				t.Fatalf("err=%v", err)
			}
			if err == nil && tc.name == "session only default" && cfg.Providers["claude-main"].EffectiveSessionReserve() != .5 {
				t.Fatal("missing conservative default")
			}
			if err == nil && tc.name == "explicit zero" && cfg.Providers["claude-main"].EffectiveSessionReserve() != 0 {
				t.Fatal("zero reserve was defaulted")
			}
		})
	}
	text := strings.Replace(claudeConfig, "provider: claude", "provider: codex", 1)
	text = strings.Replace(text, "    window_weekly_cost: 0.08\n", "    window_weekly_cost: 0.08\n    weekly_policy: session_only\n", 1)
	if _, err := config.Load(writeConfig(t, text)); err == nil {
		t.Fatal("session_only accepted for codex")
	}
}

func TestManagedSessionOnlyRoundTrip(t *testing.T) {
	path := writeConfig(t, claudeConfig)
	reserve := .6
	accounts := []config.ManagedAccount{{ID: "team", Provider: "claude", OpenUsageCard: "claude@abcd1234", WeeklyPolicy: "session_only", SessionReserve: &reserve}}
	if err := config.WriteManagedAccounts(filepath.Join(filepath.Dir(path), "accounts.yaml"), accounts); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["team"]
	if p.WeeklyPolicy != "session_only" || p.EffectiveSessionReserve() != .6 {
		t.Fatalf("merged provider=%+v", p)
	}
}
