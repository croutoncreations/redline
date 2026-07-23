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
	Database        string              `yaml:"database"`
	RunArtifactsDir string              `yaml:"run_artifacts_dir"`
	ActivePolicy    string              `yaml:"active_policy"`
	MaxSnapshotAge  string              `yaml:"max_snapshot_age"`
	Scheduler       Scheduler           `yaml:"scheduler"`
	UsageMonitor    UsageMonitor        `yaml:"usage_monitor"`
	Notifications   Notifications       `yaml:"notifications"`
	Providers       map[string]Provider `yaml:"providers"`
	Policies        map[string]Policy   `yaml:"policies"`
}

type Notifications struct {
	Enabled bool     `yaml:"enabled"`
	Command string   `yaml:"command"`
	Timeout string   `yaml:"timeout"`
	Events  []string `yaml:"events"`
}

var knownNotificationEvents = map[string]bool{
	"run.completed": true, "run.failed": true, "scheduler.error": true,
}

func (c Config) NotificationTimeout() time.Duration {
	if c.Notifications.Timeout == "" {
		return 30 * time.Second
	}
	timeout, _ := time.ParseDuration(c.Notifications.Timeout)
	return timeout
}

func (c Config) NotificationEvents() map[string]bool {
	events := c.Notifications.Events
	if len(events) == 0 {
		events = []string{"run.completed", "run.failed", "scheduler.error"}
	}
	result := make(map[string]bool, len(events))
	for _, event := range events {
		result[event] = true
	}
	return result
}

type Scheduler struct {
	Enabled      bool   `yaml:"enabled"`
	PollInterval string `yaml:"poll_interval"`
}

type UsageMonitor struct {
	Enabled          bool   `yaml:"enabled"`
	PollInterval     string `yaml:"poll_interval"`
	GatepostDatabase string `yaml:"gatepost_database"`
}

func (c Config) UsageMonitorInterval() (time.Duration, error) {
	if c.UsageMonitor.PollInterval == "" {
		return 5 * time.Minute, nil
	}
	interval, err := time.ParseDuration(c.UsageMonitor.PollInterval)
	if err != nil || interval <= 0 {
		return 0, fmt.Errorf("usage_monitor poll_interval must be a positive duration")
	}
	return interval, nil
}

func (c Config) ArtifactsDirectory() string {
	if c.RunArtifactsDir == "" {
		return ".redline/runs"
	}
	return c.RunArtifactsDir
}

func (c Config) SchedulerInterval() (time.Duration, error) {
	if c.Scheduler.PollInterval == "" {
		return 5 * time.Minute, nil
	}
	interval, err := time.ParseDuration(c.Scheduler.PollInterval)
	if err != nil || interval <= 0 {
		return 0, fmt.Errorf("scheduler poll_interval must be a positive duration")
	}
	return interval, nil
}

type Provider struct {
	Provider         string                `yaml:"provider"`
	UsageSource      string                `yaml:"usage_source"`
	OpenUsageURL     string                `yaml:"openusage_url"`
	WindowWeeklyCost float64               `yaml:"window_weekly_cost"`
	Policy           string                `yaml:"policy"`
	ModelGroups      map[string]ModelGroup `yaml:"model_groups"`
}

func (p Provider) EffectiveUsageSource() string {
	if source := strings.ToLower(strings.TrimSpace(p.UsageSource)); source != "" {
		return source
	}
	return "auto"
}

type ModelGroup struct {
	Aliases []string `yaml:"aliases"`
}

func (p Provider) EffectiveModelGroups() map[string]ModelGroup {
	groups := make(map[string]ModelGroup, len(p.ModelGroups)+1)
	for name, group := range p.ModelGroups {
		groups[strings.ToLower(strings.TrimSpace(name))] = group
	}
	if strings.EqualFold(p.Provider, "claude") {
		if _, ok := groups["fable"]; !ok {
			groups["fable"] = ModelGroup{Aliases: []string{"fable", "claude-fable-5", "claude-fable-latest"}}
		}
	}
	return groups
}

func (p Provider) ResolveModelGroup(model, explicit string) (group, routing string, err error) {
	groups := p.EffectiveModelGroups()
	if configured := strings.ToLower(strings.TrimSpace(explicit)); configured != "" {
		if _, ok := groups[configured]; !ok {
			return "", "", fmt.Errorf("budget model group %q is not configured", explicit)
		}
		return configured, "explicit", nil
	}
	normalizedModel := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndex(normalizedModel, "/"); slash >= 0 {
		normalizedModel = normalizedModel[slash+1:]
	}
	for name, configured := range groups {
		for _, alias := range configured.Aliases {
			if normalizedModel == strings.ToLower(strings.TrimSpace(alias)) {
				return name, "alias", nil
			}
		}
	}
	return "", "account_only_unmatched", nil
}

type Policy struct {
	TriggerMargin  float64         `yaml:"trigger_margin" json:"trigger_margin"`
	RollingReserve float64         `yaml:"rolling_reserve" json:"rolling_reserve"`
	PaceThresholds []PaceThreshold `yaml:"pace_thresholds" json:"pace_thresholds"`
}

type PaceThreshold struct {
	TimeRemaining      string  `yaml:"time_remaining" json:"time_remaining"`
	MinWeeklyRemaining float64 `yaml:"min_weekly_remaining" json:"min_weekly_remaining"`
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
		if provider.Provider == "" {
			return Config{}, fmt.Errorf("provider %q requires provider", name)
		}
		source := provider.EffectiveUsageSource()
		if source != "auto" && source != "openusage" && source != "native" {
			return Config{}, fmt.Errorf("provider %q usage_source must be auto, openusage, or native", name)
		}
		if source == "openusage" && strings.TrimSpace(provider.OpenUsageURL) == "" {
			return Config{}, fmt.Errorf("provider %q openusage source requires openusage_url", name)
		}
		if err := fraction("window_weekly_cost", provider.WindowWeeklyCost); err != nil {
			return Config{}, fmt.Errorf("provider %q: %w", name, err)
		}
		if provider.WindowWeeklyCost == 0 {
			return Config{}, fmt.Errorf("provider %q: window_weekly_cost must be greater than zero", name)
		}
		if provider.Policy != "" {
			if _, ok := cfg.Policies[provider.Policy]; !ok {
				return Config{}, fmt.Errorf("provider %q: policy %q is not defined", name, provider.Policy)
			}
		}
		aliases := make(map[string]string)
		for groupName, group := range provider.EffectiveModelGroups() {
			if groupName == "" {
				return Config{}, fmt.Errorf("provider %q: model group name is required", name)
			}
			for _, alias := range group.Aliases {
				normalized := strings.ToLower(strings.TrimSpace(alias))
				if normalized == "" {
					return Config{}, fmt.Errorf("provider %q model group %q: alias is required", name, groupName)
				}
				if existing, ok := aliases[normalized]; ok && existing != groupName {
					return Config{}, fmt.Errorf("provider %q: model alias %q belongs to both %q and %q", name, alias, existing, groupName)
				}
				aliases[normalized] = groupName
			}
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
	if _, err := cfg.SchedulerInterval(); err != nil {
		return Config{}, err
	}
	if _, err := cfg.UsageMonitorInterval(); err != nil {
		return Config{}, err
	}
	if cfg.UsageMonitor.Enabled && strings.TrimSpace(cfg.UsageMonitor.GatepostDatabase) == "" {
		return Config{}, fmt.Errorf("enabled usage_monitor requires gatepost_database")
	}
	if cfg.Notifications.Timeout != "" {
		timeout, err := time.ParseDuration(cfg.Notifications.Timeout)
		if err != nil || timeout <= 0 {
			return Config{}, fmt.Errorf("notifications timeout must be a positive duration")
		}
	}
	if cfg.Notifications.Enabled && strings.TrimSpace(cfg.Notifications.Command) == "" {
		return Config{}, fmt.Errorf("enabled notifications require command")
	}
	for _, event := range cfg.Notifications.Events {
		if !knownNotificationEvents[event] {
			return Config{}, fmt.Errorf("unknown notification event %q", event)
		}
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
