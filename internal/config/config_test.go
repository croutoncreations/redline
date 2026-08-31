package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/domain"
)

func TestLoadParsesTrustedAPIHosts(t *testing.T) {
	configured := strings.Replace(validConfig, "active_policy: standard", `active_policy: standard
api:
  trusted_hosts:
    - macbook.example.ts.net
    - 100.101.102.103`, 1)
	cfg, err := config.Load(writeConfig(t, configured))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.API.TrustedHosts) != 2 || cfg.API.TrustedHosts[0] != "macbook.example.ts.net" ||
		cfg.API.TrustedHosts[1] != "100.101.102.103" {
		t.Fatalf("trusted hosts = %#v", cfg.API.TrustedHosts)
	}
}

func TestLoadRejectsInvalidTrustedAPIHosts(t *testing.T) {
	for _, host := range []string{
		"https://macbook.example.ts.net", "*.example.ts.net", "macbook.example.ts.net:443", "",
		"mac book.example.ts.net", "user@example.ts.net", `macbook\\name.example.ts.net`,
		".example.ts.net", "macbook..example.ts.net", "example.ts.net.", "-macbook.example.ts.net",
	} {
		t.Run(host, func(t *testing.T) {
			configured := strings.Replace(validConfig, "active_policy: standard", `active_policy: standard
api:
  trusted_hosts:
    - "`+host+`"`, 1)
			if _, err := config.Load(writeConfig(t, configured)); err == nil {
				t.Fatalf("expected trusted host %q to be rejected", host)
			}
		})
	}
}

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

func TestUsageMonitorDefaultsAndValidatesInterval(t *testing.T) {
	cfg := config.Config{UsageMonitor: config.UsageMonitor{}}
	if got, err := cfg.UsageMonitorInterval(); err != nil || got != 5*time.Minute {
		t.Fatalf("default interval=%s err=%v", got, err)
	}
	cfg.UsageMonitor.PollInterval = "nope"
	if _, err := cfg.UsageMonitorInterval(); err == nil {
		t.Fatal("expected invalid usage monitor interval")
	}
}

func TestNotificationsAreDisabledByDefault(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notifications.Enabled || cfg.NotificationTimeout() != 30*time.Second {
		t.Fatalf("notifications = %#v timeout=%s", cfg.Notifications, cfg.NotificationTimeout())
	}
	if !cfg.NotificationEvents()[domain.EventRunStarted] {
		t.Fatalf("default events = %#v", cfg.NotificationEvents())
	}
}

func TestLoadParsesNotificationCommandAndEvents(t *testing.T) {
	configured := strings.Replace(validConfig, "active_policy: standard", `active_policy: standard
notifications:
  enabled: true
  command: ./scripts/notify
  timeout: 10s
  events: [run.completed, scheduler.error]`, 1)
	cfg, err := config.Load(writeConfig(t, configured))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Notifications.Enabled || cfg.Notifications.Command != "./scripts/notify" ||
		cfg.NotificationTimeout() != 10*time.Second || len(cfg.Notifications.Events) != 2 {
		t.Fatalf("notifications = %#v", cfg.Notifications)
	}
}

func TestEnabledNotificationsRequireCommandAndKnownEvents(t *testing.T) {
	missingCommand := strings.Replace(validConfig, "active_policy: standard", `active_policy: standard
notifications:
  enabled: true
  events: [run.completed]`, 1)
	if _, err := config.Load(writeConfig(t, missingCommand)); err == nil {
		t.Fatal("expected missing command error")
	}
	unknownEvent := strings.Replace(validConfig, "active_policy: standard", `active_policy: standard
notifications:
  enabled: true
  command: notify
  events: [task.exploded]`, 1)
	if _, err := config.Load(writeConfig(t, unknownEvent)); err == nil {
		t.Fatal("expected unknown event error")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, strings.Replace(validConfig, "rolling_reserve", "rolling_resrve", 1))
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestUsageSourceDefaultsToAutoAndAllowsNativeWithoutOpenUsageURL(t *testing.T) {
	configured := strings.Replace(validConfig, "    openusage_url: http://127.0.0.1:6736\n", "    usage_source: native\n", 1)
	cfg, err := config.Load(writeConfig(t, configured))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["codex-main"].EffectiveUsageSource() != "native" ||
		(config.Provider{}).EffectiveUsageSource() != "auto" {
		t.Fatalf("providers=%#v", cfg.Providers)
	}
}

func TestExplicitOpenUsageSourceRequiresURL(t *testing.T) {
	configured := strings.Replace(validConfig, "    openusage_url: http://127.0.0.1:6736\n", "    usage_source: openusage\n", 1)
	if _, err := config.Load(writeConfig(t, configured)); err == nil {
		t.Fatal("expected explicit OpenUsage URL error")
	}
}

func TestLoadRejectsUnknownUsageSource(t *testing.T) {
	configured := strings.Replace(validConfig, "    openusage_url: http://127.0.0.1:6736", "    usage_source: random\n    openusage_url: http://127.0.0.1:6736", 1)
	if _, err := config.Load(writeConfig(t, configured)); err == nil {
		t.Fatal("expected invalid usage source error")
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

func TestLoadParsesPolicyPaceGapTrigger(t *testing.T) {
	withTrigger := strings.Replace(validConfig, "    rolling_reserve: 0.25", `    rolling_reserve: 0.25
    pace_gap_trigger: 0.30`, 1)
	cfg, err := config.Load(writeConfig(t, withTrigger))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Policies["standard"].PaceGapTrigger
	if got == nil || *got != 0.30 {
		t.Fatalf("pace gap trigger = %#v", got)
	}
}

func TestLoadRejectsInvalidPolicyPaceGapTrigger(t *testing.T) {
	withTrigger := strings.Replace(validConfig, "    rolling_reserve: 0.25", `    rolling_reserve: 0.25
    pace_gap_trigger: 1.10`, 1)
	if _, err := config.Load(writeConfig(t, withTrigger)); err == nil {
		t.Fatal("expected invalid pace gap trigger error")
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

func TestClaudeModelRoutingDistinguishesFableFromAccountOnlyModels(t *testing.T) {
	provider := config.Provider{Provider: "claude"}
	for _, model := range []string{"fable", "claude-fable-5", "claude-fable-latest"} {
		group, routing, err := provider.ResolveModelGroup(model, "")
		if err != nil || group != "fable" || routing != "alias" {
			t.Fatalf("model %q group=%q routing=%q err=%v", model, group, routing, err)
		}
	}
	for _, model := range []string{"haiku", "sonnet", "opus", "claude-opus-4-8"} {
		group, routing, err := provider.ResolveModelGroup(model, "")
		if err != nil || group != "" || routing != "account_only_unmatched" {
			t.Fatalf("model %q group=%q routing=%q err=%v", model, group, routing, err)
		}
	}
}

func TestClaudeModelRoutingAcceptsProviderQualifiedPiModel(t *testing.T) {
	provider := config.Provider{Provider: "claude"}
	group, routing, err := provider.ResolveModelGroup("anthropic-cli/claude-fable-5", "")
	if err != nil {
		t.Fatal(err)
	}
	if group != "fable" || routing != "alias" {
		t.Fatalf("group=%q routing=%q", group, routing)
	}
}

func TestExplicitBudgetModelGroupMustExist(t *testing.T) {
	provider := config.Provider{Provider: "claude"}
	if _, _, err := provider.ResolveModelGroup("custom", "missing"); err == nil {
		t.Fatal("expected unknown explicit model group error")
	}
}

func TestLoadRejectsAliasesSharedAcrossModelGroups(t *testing.T) {
	configured := strings.Replace(validConfig, "    window_weekly_cost: 0.10", `    window_weekly_cost: 0.10
    model_groups:
      first: { aliases: [same] }
      second: { aliases: [SAME] }`, 1)
	if _, err := config.Load(writeConfig(t, configured)); err == nil {
		t.Fatal("expected duplicate model alias error")
	}
}

func TestLoadAcceptsProviderSpecificPolicy(t *testing.T) {
	configured := strings.Replace(validConfig, "    window_weekly_cost: 0.10", `    window_weekly_cost: 0.10
    policy: early`, 1)
	configured = strings.Replace(configured, "policies:\n  standard:", `policies:
  early:
    trigger_margin: 0.01
    rolling_reserve: 0.10
  standard:`, 1)
	cfg, err := config.Load(writeConfig(t, configured))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["codex-main"].Policy != "early" {
		t.Fatalf("provider = %#v", cfg.Providers["codex-main"])
	}
}

func TestLoadRejectsUnknownProviderSpecificPolicy(t *testing.T) {
	configured := strings.Replace(validConfig, "    window_weekly_cost: 0.10", `    window_weekly_cost: 0.10
    policy: missing`, 1)
	if _, err := config.Load(writeConfig(t, configured)); err == nil ||
		!strings.Contains(err.Error(), `policy "missing" is not defined`) {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderConcurrencyDefaultsAndValidation(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	provider := cfg.Providers["codex-main"]
	if provider.EffectiveMaxConcurrentRuns() != 1 || len(provider.PoolConcurrency) != 0 {
		t.Fatalf("provider = %#v", provider)
	}

	configured := strings.Replace(validConfig, "    window_weekly_cost: 0.10", `    window_weekly_cost: 0.10
    max_concurrent_runs: 3
    pool_concurrency:
      weekly: 2
      model:fable:weekly: 1`, 1)
	cfg, err = config.Load(writeConfig(t, configured))
	if err != nil {
		t.Fatal(err)
	}
	provider = cfg.Providers["codex-main"]
	if provider.EffectiveMaxConcurrentRuns() != 3 || provider.PoolConcurrency["weekly"] != 2 ||
		provider.PoolConcurrency["model:fable:weekly"] != 1 {
		t.Fatalf("provider = %#v", provider)
	}

	for _, invalid := range []string{
		"    max_concurrent_runs: -1\n",
		"    pool_concurrency: {weekly: 0}\n",
		"    pool_concurrency: {'': 1}\n",
	} {
		candidate := strings.Replace(validConfig, "    window_weekly_cost: 0.10\n",
			"    window_weekly_cost: 0.10\n"+invalid, 1)
		if _, err := config.Load(writeConfig(t, candidate)); err == nil {
			t.Fatalf("expected invalid concurrency error for %q", invalid)
		}
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
