package config_test

import (
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

func TestResetSignalFiresaleConfiguration(t *testing.T) {
	for _, tc := range []struct {
		enabled, mode string
		valid         bool
		want          string
	}{
		{"true", "", true, "notify"}, {"false", "", true, "off"},
		{"true", "off", true, "off"}, {"true", "notify", true, "notify"}, {"true", "auto", true, "auto"},
		{"false", "auto", false, ""}, {"false", "notify", false, ""}, {"true", "invalid", false, ""},
	} {
		t.Run(tc.enabled+"/"+tc.mode, func(t *testing.T) {
			raw := validConfig + "\nreset_signals:\n  enabled: " + tc.enabled + "\n"
			if tc.mode != "" {
				raw += "  firesale: " + tc.mode + "\n"
			}
			cfg, err := config.Load(writeConfig(t, raw))
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid firesale config accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.ResetSignalsFiresale(); got != tc.want {
				t.Fatalf("mode=%q want=%q", got, tc.want)
			}
		})
	}
}
