package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jfox/redline/internal/config"
)

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
