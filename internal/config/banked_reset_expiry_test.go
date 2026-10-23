package config_test

import (
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

func TestBankedResetRedeemRequiresMilestone5(t *testing.T) {
	raw := strings.Replace(validConfig, "provider: codex", "provider: codex\n    banked_resets:\n      on_expiry: redeem", 1)
	_, err := config.Load(writeConfig(t, raw))
	if err == nil || !strings.Contains(err.Error(), "off or notify") || !strings.Contains(err.Error(), "milestone 5") {
		t.Fatalf("error=%v", err)
	}
}
