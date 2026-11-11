package config_test

import (
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
)

func TestLoadResetSignalsConfiguration(t *testing.T) {
	for _, interval := range []string{"4m59s", "5m", "10m", "invalid"} {
		t.Run(interval, func(t *testing.T) {
			raw := validConfig + "\nreset_signals:\n  enabled: true\n  poll_interval: " + interval + "\n  notify: false\nnotifications:\n  events: [reset.announced]\n"
			cfg, err := config.Load(writeConfig(t, raw))
			if interval == "4m59s" || interval == "invalid" {
				if err == nil {
					t.Fatal("invalid reset poll interval accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := cfg.ResetSignalsInterval()
			want, _ := time.ParseDuration(interval)
			if err != nil || got != want || !cfg.ResetSignals.Enabled || cfg.ResetSignalsNotify() || len(cfg.Warnings) > 0 || !cfg.NotificationEvents()["reset.announced"] {
				t.Fatalf("cfg=%+v interval=%v err=%v", cfg, got, err)
			}
		})
	}
}
