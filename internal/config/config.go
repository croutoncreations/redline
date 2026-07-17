package config

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/jfox/redline/internal/decision"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Database       string              `yaml:"database"`
	ActivePolicy   string              `yaml:"active_policy"`
	MaxSnapshotAge string              `yaml:"max_snapshot_age"`
	Providers      map[string]Provider `yaml:"providers"`
	Policies       map[string]Policy   `yaml:"policies"`
}

type Provider struct {
	Provider         string  `yaml:"provider"`
	OpenUsageURL     string  `yaml:"openusage_url"`
	WindowWeeklyCost float64 `yaml:"window_weekly_cost"`
}

type Policy struct {
	TriggerMargin  float64         `yaml:"trigger_margin"`
	RollingReserve float64         `yaml:"rolling_reserve"`
	PaceThresholds []PaceThreshold `yaml:"pace_thresholds"`
}

type PaceThreshold struct {
	TimeRemaining      string  `yaml:"time_remaining"`
	MinWeeklyRemaining float64 `yaml:"min_weekly_remaining"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Database == "" {
		return Config{}, fmt.Errorf("config database is required")
	}
	if cfg.ActivePolicy == "" {
		return Config{}, fmt.Errorf("config active_policy is required")
	}
	if _, ok := cfg.Policies[cfg.ActivePolicy]; !ok {
		return Config{}, fmt.Errorf("active policy %q is not defined", cfg.ActivePolicy)
	}
	if len(cfg.Providers) == 0 {
		return Config{}, fmt.Errorf("at least one provider is required")
	}
	for name, provider := range cfg.Providers {
		if provider.Provider == "" || provider.OpenUsageURL == "" {
			return Config{}, fmt.Errorf("provider %q requires provider and openusage_url", name)
		}
		if err := fraction("window_weekly_cost", provider.WindowWeeklyCost); err != nil {
			return Config{}, fmt.Errorf("provider %q: %w", name, err)
		}
		if provider.WindowWeeklyCost == 0 {
			return Config{}, fmt.Errorf("provider %q: window_weekly_cost must be greater than zero", name)
		}
	}
	for name, policy := range cfg.Policies {
		if err := fraction("trigger_margin", policy.TriggerMargin); err != nil {
			return Config{}, fmt.Errorf("policy %q: %w", name, err)
		}
		if err := fraction("rolling_reserve", policy.RollingReserve); err != nil {
			return Config{}, fmt.Errorf("policy %q: %w", name, err)
		}
		if _, err := policy.DecisionThresholds(); err != nil {
			return Config{}, fmt.Errorf("policy %q: %w", name, err)
		}
	}
	if _, err := cfg.SnapshotAge(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (p Policy) DecisionThresholds() ([]decision.PaceThreshold, error) {
	thresholds := make([]decision.PaceThreshold, 0, len(p.PaceThresholds))
	for _, configured := range p.PaceThresholds {
		duration, err := time.ParseDuration(configured.TimeRemaining)
		if err != nil || duration <= 0 {
			return nil, fmt.Errorf("pace threshold time_remaining must be a positive duration")
		}
		if err := fraction("pace threshold min_weekly_remaining", configured.MinWeeklyRemaining); err != nil {
			return nil, err
		}
		thresholds = append(thresholds, decision.PaceThreshold{
			TimeRemaining:      duration,
			MinWeeklyRemaining: configured.MinWeeklyRemaining,
		})
	}
	return thresholds, nil
}

func (c Config) SnapshotAge() (time.Duration, error) {
	if c.MaxSnapshotAge == "" {
		return 15 * time.Minute, nil
	}
	duration, err := time.ParseDuration(c.MaxSnapshotAge)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("max_snapshot_age must be a positive duration")
	}
	return duration, nil
}

func fraction(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return fmt.Errorf("%s must be between 0 and 1", name)
	}
	return nil
}
