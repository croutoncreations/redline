package config

import (
	"fmt"
	"math"
	"net"
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
	API             API                 `yaml:"api"`
	UsageMonitor    UsageMonitor        `yaml:"usage_monitor"`
	Notifications   Notifications       `yaml:"notifications"`
	Providers       map[string]Provider `yaml:"providers"`
	Policies        map[string]Policy   `yaml:"policies"`
	APIToken        string              `yaml:"-"`
	// DemoScenario is set only by the isolated demo launcher. It is never loaded
	// from user configuration and lets clients clearly label synthetic data.
	DemoScenario string `yaml:"-"`
}

type Notifications struct {
	Enabled bool     `yaml:"enabled"`
	Command string   `yaml:"command"`
	Timeout string   `yaml:"timeout"`
	Events  []string `yaml:"events"`
}

var knownNotificationEvents = map[string]bool{
	"run.started": true, "run.completed": true, "run.failed": true, "scheduler.error": true,
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
		events = []string{"run.started", "run.completed", "run.failed", "scheduler.error"}
	}
	result := make(map[string]bool, len(events))
	for _, event := range events {
		result[event] = true
	}
	return result
}

type API struct {
	TrustedHosts []string `yaml:"trusted_hosts"`
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
	return positiveDuration("usage_monitor poll_interval", c.UsageMonitor.PollInterval)
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
	return positiveDuration("scheduler poll_interval", c.Scheduler.PollInterval)
}

type Provider struct {
	Provider          string                `yaml:"provider"`
	UsageSource       string                `yaml:"usage_source"`
	OpenUsageURL      string                `yaml:"openusage_url"`
	WindowWeeklyCost  float64               `yaml:"window_weekly_cost"`
	Policy            string                `yaml:"policy"`
	MaxConcurrentRuns int                   `yaml:"max_concurrent_runs"`
	PoolConcurrency   map[string]int        `yaml:"pool_concurrency"`
	ModelGroups       map[string]ModelGroup `yaml:"model_groups"`
}

func (p Provider) EffectiveMaxConcurrentRuns() int {
	if p.MaxConcurrentRuns <= 0 {
		return 1
	}
	return p.MaxConcurrentRuns
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
	PaceGapTrigger *float64        `yaml:"pace_gap_trigger,omitempty" json:"pace_gap_trigger,omitempty"`
	PaceThresholds []PaceThreshold `yaml:"pace_thresholds" json:"pace_thresholds"`
}

type PaceThreshold struct {
	TimeRemaining      string  `yaml:"time_remaining" json:"time_remaining"`
	MinWeeklyRemaining float64 `yaml:"min_weekly_remaining" json:"min_weekly_remaining"`
}

func validTrustedHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host {
		return false
	}
	if net.ParseIP(host) != nil || !strings.HasSuffix(strings.ToLower(host), ".ts.net") {
		return false
	}
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
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
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

func (cfg *Config) validate() error {
	if cfg.Database == "" {
		return fmt.Errorf("database is required")
	}
	if cfg.ActivePolicy == "" {
		return fmt.Errorf("active_policy is required")
	}
	if _, ok := cfg.Policies[cfg.ActivePolicy]; !ok {
		return fmt.Errorf("active_policy %q is not defined under policies", cfg.ActivePolicy)
	}
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("at least one provider is required")
	}
	for index, host := range cfg.API.TrustedHosts {
		if !validTrustedHost(host) {
			return fmt.Errorf("api trusted_hosts[%d] %q must be a fully qualified Tailscale MagicDNS name ending in .ts.net", index, host)
		}
		cfg.API.TrustedHosts[index] = strings.ToLower(host)
	}
	for name, provider := range cfg.Providers {
		if provider.Provider == "" {
			return fmt.Errorf("provider %q requires provider", name)
		}
		source := provider.EffectiveUsageSource()
		if source != "auto" && source != "openusage" && source != "native" {
			return fmt.Errorf("provider %q usage_source %q must be auto, openusage, or native", name, provider.UsageSource)
		}
		if source == "openusage" && strings.TrimSpace(provider.OpenUsageURL) == "" {
			return fmt.Errorf("provider %q usage_source is openusage but openusage_url is empty", name)
		}
		if err := fraction("window_weekly_cost", provider.WindowWeeklyCost); err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
		if provider.WindowWeeklyCost == 0 {
			return fmt.Errorf("provider %q: window_weekly_cost must be greater than zero, got 0", name)
		}
		if provider.Policy != "" {
			if _, ok := cfg.Policies[provider.Policy]; !ok {
				return fmt.Errorf("provider %q: policy %q is not defined under policies", name, provider.Policy)
			}
		}
		if provider.MaxConcurrentRuns < 0 {
			return fmt.Errorf("provider %q: max_concurrent_runs must not be negative, got %d", name, provider.MaxConcurrentRuns)
		}
		for pool, limit := range provider.PoolConcurrency {
			if strings.TrimSpace(pool) == "" {
				return fmt.Errorf("provider %q: pool_concurrency has an empty key", name)
			}
			if limit <= 0 {
				return fmt.Errorf("provider %q: pool_concurrency %q must be greater than zero, got %d", name, pool, limit)
			}
		}
		aliases := make(map[string]string)
		for groupName, group := range provider.EffectiveModelGroups() {
			if groupName == "" {
				return fmt.Errorf("provider %q: model_groups has an empty group name", name)
			}
			for _, alias := range group.Aliases {
				normalized := strings.ToLower(strings.TrimSpace(alias))
				if normalized == "" {
					return fmt.Errorf("provider %q model group %q: has an empty alias", name, groupName)
				}
				if existing, ok := aliases[normalized]; ok && existing != groupName {
					return fmt.Errorf("provider %q: model alias %q belongs to both %q and %q", name, alias, existing, groupName)
				}
				aliases[normalized] = groupName
			}
		}
	}
	for name, policy := range cfg.Policies {
		if err := fraction("trigger_margin", policy.TriggerMargin); err != nil {
			return fmt.Errorf("policy %q: %w", name, err)
		}
		if err := fraction("rolling_reserve", policy.RollingReserve); err != nil {
			return fmt.Errorf("policy %q: %w", name, err)
		}
		if policy.PaceGapTrigger != nil {
			if err := fraction("pace_gap_trigger", *policy.PaceGapTrigger); err != nil {
				return fmt.Errorf("policy %q: %w", name, err)
			}
		}
		if _, err := policy.DecisionThresholds(); err != nil {
			return fmt.Errorf("policy %q: %w", name, err)
		}
	}
	if _, err := cfg.SnapshotAge(); err != nil {
		return err
	}
	if _, err := cfg.SchedulerInterval(); err != nil {
		return err
	}
	if _, err := cfg.UsageMonitorInterval(); err != nil {
		return err
	}
	if cfg.UsageMonitor.Enabled && strings.TrimSpace(cfg.UsageMonitor.GatepostDatabase) == "" {
		return fmt.Errorf("usage_monitor is enabled but gatepost_database is empty")
	}
	if cfg.Notifications.Timeout != "" {
		if _, err := positiveDuration("notifications timeout", cfg.Notifications.Timeout); err != nil {
			return err
		}
	}
	if cfg.Notifications.Enabled && strings.TrimSpace(cfg.Notifications.Command) == "" {
		return fmt.Errorf("notifications are enabled but command is empty")
	}
	for _, event := range cfg.Notifications.Events {
		if !knownNotificationEvents[event] {
			return fmt.Errorf("notifications event %q is not recognized (want one of run.started, run.completed, run.failed, scheduler.error)", event)
		}
	}
	return nil
}

func (p Policy) DecisionThresholds() ([]decision.PaceThreshold, error) {
	thresholds := make([]decision.PaceThreshold, 0, len(p.PaceThresholds))
	for index, configured := range p.PaceThresholds {
		duration, err := positiveDuration(fmt.Sprintf("pace_thresholds[%d] time_remaining", index), configured.TimeRemaining)
		if err != nil {
			return nil, err
		}
		if err := fraction(fmt.Sprintf("pace_thresholds[%d] min_weekly_remaining", index), configured.MinWeeklyRemaining); err != nil {
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
	return positiveDuration("max_snapshot_age", c.MaxSnapshotAge)
}

// positiveDuration parses raw as a time.Duration for the named config field,
// distinguishing an unparseable value from a parsed-but-non-positive one so
// the error always shows what was actually configured.
func positiveDuration(name, raw string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a valid duration: %w", name, raw, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s %q must be positive", name, raw)
	}
	return duration, nil
}

func fraction(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return fmt.Errorf("%s must be between 0 and 1, got %v", name, value)
	}
	return nil
}
