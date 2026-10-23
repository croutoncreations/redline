package config_test

import (
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

func TestBankedResetExpiryValidationListsActions(t *testing.T) {
	raw := strings.Replace(validConfig, "provider: codex", "provider: codex\n    banked_resets:\n      on_expiry: automatic", 1)
	_, err := config.Load(writeConfig(t, raw))
	if err == nil || !strings.Contains(err.Error(), "off, notify, or redeem") {
		t.Fatalf("error=%v", err)
	}
}
