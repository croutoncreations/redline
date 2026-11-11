package config_test

import (
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

func TestLoadFiresaleConfiguration(t *testing.T) {
	for _, value := range []string{"0", "0.45", "1", "-0.1", "1.1", ".nan"} {
		t.Run(value, func(t *testing.T) {
			raw := strings.Replace(validConfig, "    rolling_reserve: 0.25", "    rolling_reserve: 0.25\n    firesale_trigger: "+value, 1)
			cfg, err := config.Load(writeConfig(t, raw))
			invalid := value == "-0.1" || value == "1.1" || value == ".nan"
			if invalid {
				if err == nil {
					t.Fatal("invalid trigger accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Policies["standard"].FiresaleTrigger == nil {
				t.Fatal("trigger omitted")
			}
		})
	}
	raw := strings.Replace(validConfig, "    window_weekly_cost: 0.10", "    window_weekly_cost: 0.10\n    firesale_max_concurrent_runs: 3", 1)
	cfg, err := config.Load(writeConfig(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["codex-main"].FiresaleMaxConcurrentRuns != 3 {
		t.Fatal("concurrency not loaded")
	}
	if cfg.NotificationEvents()["firesale.started"] || cfg.NotificationEvents()["firesale.ended"] {
		t.Fatal("firesale events must be opt-in")
	}
	raw = strings.Replace(raw, "firesale_max_concurrent_runs: 3", "firesale_max_concurrent_runs: -1", 1)
	if _, err := config.Load(writeConfig(t, raw)); err == nil {
		t.Fatal("negative concurrency accepted")
	}
}
