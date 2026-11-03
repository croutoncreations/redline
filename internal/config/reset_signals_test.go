package config

import (
	"testing"
	"time"
)

func TestResetSignalsConfiguration(t *testing.T) {
	cfg := Config{}
	interval, err := cfg.ResetSignalsInterval()
	if err != nil || interval != 10*time.Minute || cfg.ResetSignals.Enabled || cfg.ResetSignalsNotify() {
		t.Fatalf("unexpected defaults: %v %v %+v", interval, err, cfg.ResetSignals)
	}
	cfg.ResetSignals.Enabled = true
	if !cfg.ResetSignalsNotify() {
		t.Fatal("enabled monitoring defaults to notify")
	}
	no := false
	cfg.ResetSignals.Notify = &no
	if cfg.ResetSignalsNotify() {
		t.Fatal("explicit notify false ignored")
	}
	for _, value := range []string{"4m59s", "0", "-1m", "bad"} {
		cfg.ResetSignals.PollInterval = value
		if _, err := cfg.ResetSignalsInterval(); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	cfg.ResetSignals.PollInterval = "5m"
	if _, err := cfg.ResetSignalsInterval(); err != nil {
		t.Fatal(err)
	}
	if !knownNotificationEvents["reset.announced"] || (Config{}).NotificationEvents()["reset.announced"] {
		t.Fatal("announcement event must be known but opt-in")
	}
}
