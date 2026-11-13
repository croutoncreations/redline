package api_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
)

// Previews say what dispatch does: an account on a non-default OpenUsage
// card is held with mode login_unknown and no task is selected, even when
// its numbers would admit work.
func TestPreviewsOfAnOpenUsageCardAccountSayDispatchIsHeld(t *testing.T) {
	fetched := apiNow.Format(time.RFC3339Nano)
	payload := fmt.Sprintf(`{"schema":"openusage.limits.v1","generatedAt":%q,"providers":{
	  "claude@f48a43e1":{"plan":"Team","fetchedAt":%q,"stale":false,"resources":{
	    "session":{"used":10,"limit":100,"resetsAt":%q},
	    "weekly":{"used":30,"limit":100,"resetsAt":%q}}}},"errors":[]}`,
		fetched, fetched, apiNow.Add(2*time.Hour).Format(time.RFC3339Nano), apiNow.Add(48*time.Hour).Format(time.RFC3339Nano))
	server, db := newAPIServerConfigured(t, payload, func(cfg *config.Config) {
		provider := cfg.Providers["claude-main"]
		provider.UsageSource, provider.OpenUsageCard = "openusage", "claude@f48a43e1"
		cfg.Providers["claude-main"] = provider
	})
	createClaudeCandidate(t, db, "opus-profile", "opus", "", "backlog", 50)
	for _, path := range []string{"/v1/scheduler/evaluate", "/v1/providers/claude-main/decision"} {
		result := postJSON[schedulerResponseForTest](t, server.URL+path, map[string]any{"provider_account_id": "claude-main"})
		if result.Result.Decision != decision.Wait || result.Result.Mode != decision.ModeLoginUnknown || result.SelectedTask != nil {
			t.Fatalf("%s: %+v selected=%v", path, result.Result, result.SelectedTask)
		}
	}
}
