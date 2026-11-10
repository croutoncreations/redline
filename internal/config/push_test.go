package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

func TestPushConfigValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth")
	if err := os.WriteFile(path, []byte("shared-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		url   string
		valid bool
	}{
		{"", true}, {"https://relay.example/base", true}, {"http://localhost:1234", true}, {"http://127.0.0.1:1234", true}, {"http://[::1]:1234", true},
		{"http://relay.example", false}, {"ftp://127.0.0.1", false}, {"/relative", false}, {"https://user:password@relay.example", false}, {"https://relay.example?q=secret", false}, {"https://relay.example#fragment", false},
	} {
		p := config.Push{RelayURL: tt.url, AuthTokenFile: path}
		if err := p.Validate(); (err == nil) != tt.valid {
			t.Errorf("url=%s valid=%v err=%v", tt.url, tt.valid, err)
		}
	}
	for _, mode := range []os.FileMode{0644, 0400, 0700} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := (config.Push{RelayURL: "https://relay.example", AuthTokenFile: path}).Validate(); err == nil {
			t.Errorf("mode %o accepted", mode)
		}
	}
	if err := (config.Push{RelayURL: "https://relay.example", AuthTokenFile: path + "-missing"}).Validate(); err == nil {
		t.Fatal("missing file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	configured := validConfig + "\npush:\n  relay_url: https://relay.example\n  auth_token_file: " + path + "\n"
	cfg, err := config.Load(writeConfig(t, configured))
	if err != nil {
		t.Fatal(err)
	}
	token, err := cfg.Push.ReadAuthToken()
	if err != nil || token != "shared-secret" {
		t.Fatal("could not read startup credential")
	}
	if len(cfg.Warnings) != 0 {
		t.Fatal("push config was not recognized")
	}
	_, err = config.Load(writeConfig(t, strings.Replace(configured, "https://relay.example", "http://relay.example", 1)))
	if err == nil {
		t.Fatal("Load skipped push validation")
	}
}
