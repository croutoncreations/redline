package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/config"
)

func TestSchedulerIntervalDefaultsToFiveMinutes(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	interval, err := cfg.SchedulerInterval()
	if err != nil {
		t.Fatal(err)
	}
	if interval != 5*time.Minute || cfg.Scheduler.Enabled {
		t.Fatalf("scheduler = %#v interval=%s", cfg.Scheduler, interval)
	}
}

func TestLoadParsesAutomaticScheduler(t *testing.T) {
	configured := strings.Replace(validConfig, "active_policy: standard", `active_policy: standard
scheduler:
  enabled: true
  poll_interval: 90s`, 1)
	cfg, err := config.Load(writeConfig(t, configured))
	if err != nil {
		t.Fatal(err)
	}
	interval, err := cfg.SchedulerInterval()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Scheduler.Enabled || interval != 90*time.Second {
		t.Fatalf("scheduler = %#v interval=%s", cfg.Scheduler, interval)
	}
}

func TestLoadRejectsInvalidSchedulerInterval(t *testing.T) {
	configured := strings.Replace(validConfig, "active_policy: standard", `active_policy: standard
scheduler:
  enabled: true
  poll_interval: immediately`, 1)
	if _, err := config.Load(writeConfig(t, configured)); err == nil {
		t.Fatal("expected invalid scheduler interval")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, strings.Replace(validConfig, "rolling_reserve", "rolling_resrve", 1))
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestLoadRejectsInvalidFractions(t *testing.T) {
	path := writeConfig(t, strings.Replace(validConfig, "window_weekly_cost: 0.10", "window_weekly_cost: 1.10", 1))
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected invalid-fraction error")
	}
}

func TestSnapshotAgeDefaultsToFifteenMinutes(t *testing.T) {
	path := writeConfig(t, strings.Replace(validConfig, "max_snapshot_age: 15m\n", "", 1))
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.SnapshotAge()
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "15m0s" {
		t.Fatalf("age = %s", got)
	}
}

func TestLoadParsesPaceThresholds(t *testing.T) {
	withThreshold := strings.Replace(validConfig, "    rolling_reserve: 0.25", `    rolling_reserve: 0.25
    pace_thresholds:
      - time_remaining: 72h
        min_weekly_remaining: 0.50`, 1)
	cfg, err := config.Load(writeConfig(t, withThreshold))
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.Policies["standard"].DecisionThresholds()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TimeRemaining.Hours() != 72 || got[0].MinWeeklyRemaining != 0.50 {
		t.Fatalf("thresholds = %#v", got)
	}
}

func TestLoadRejectsInvalidPaceThreshold(t *testing.T) {
	withThreshold := strings.Replace(validConfig, "    rolling_reserve: 0.25", `    rolling_reserve: 0.25
    pace_thresholds:
      - time_remaining: never
        min_weekly_remaining: 0.50`, 1)
	if _, err := config.Load(writeConfig(t, withThreshold)); err == nil {
		t.Fatal("expected invalid pace threshold error")
	}
}

const validConfig = `
database: redline.db
active_policy: standard
max_snapshot_age: 15m
providers:
  codex-main:
    provider: codex
    openusage_url: http://127.0.0.1:6736
    window_weekly_cost: 0.10
policies:
  standard:
    trigger_margin: 0.02
    rolling_reserve: 0.25
`

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
