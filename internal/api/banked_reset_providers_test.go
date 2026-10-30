package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
)

func TestBankedResetLookupsSkipOnlyAmbiguousProviderType(t *testing.T) {
	for _, tc := range []struct {
		name          string
		claude, codex int
	}{
		{"one each", 1, 1}, {"multiple Claude", 2, 1},
		{"multiple Codex", 1, 2}, {"multiple both", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Now().UTC()
			cfg := config.Config{Providers: map[string]config.Provider{
				"claude-main": {Provider: "Claude", UsageSource: "auto"},
				"codex-main":  {Provider: "Codex", UsageSource: "auto"},
			}}
			if tc.claude > 1 {
				cfg.Providers["claude-other"] = config.Provider{Provider: "claude"}
			}
			if tc.codex > 1 {
				cfg.Providers["codex-other"] = config.Provider{Provider: "codex"}
			}
			server := newServer(cfg, db, func() time.Time { return now }, nil, nil, nil)
			source := &bankedResetProviderSource{now: now, lookups: make(map[string]int)}
			server.usageSources.OpenUsage = source
			server.usageSources.BankedResets = source
			for _, provider := range []string{"claude", "codex"} {
				cfgProvider := cfg.Providers[provider+"-main"]
				if _, _, err := server.usageSources.Fetch(t.Context(), provider+"-main", cfgProvider); err != nil {
					t.Fatal(err)
				}
				server.usageSources.WaitForBankedResetLookups()
			}
			for provider, accounts := range map[string]int{"claude": tc.claude, "codex": tc.codex} {
				want := 0
				if accounts == 1 {
					want = 1
				}
				if got := source.lookups[provider]; got != want {
					t.Errorf("%s lookups = %d, want %d", provider, got, want)
				}
			}
		})
	}
}

type bankedResetProviderSource struct {
	now     time.Time
	lookups map[string]int
}

func (*bankedResetProviderSource) Name() string { return "openusage" }

func (s *bankedResetProviderSource) Fetch(_ context.Context, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	name := strings.ToLower(provider.Provider)
	snapshot := decision.UsageSnapshot{Provider: name, ObservedAt: s.now, Source: "openusage",
		Weekly: decision.UsageWindow{Remaining: .5, ResetsAt: s.now.Add(24 * time.Hour)}}
	if name == "codex" {
		snapshot.ApplyBankedResets(&decision.BankedResets{Available: 1})
	}
	return snapshot, nil, nil
}

func (s *bankedResetProviderSource) BankedResets(_ context.Context, provider config.Provider) (*decision.BankedResets, error) {
	s.lookups[strings.ToLower(provider.Provider)]++
	return &decision.BankedResets{Available: 1}, nil
}
