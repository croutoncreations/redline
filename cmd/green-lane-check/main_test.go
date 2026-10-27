package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/croutoncreations/redline/internal/greenlane"
)

// #182 review: the docs/deps caps had yaml tags only, so the optional JSON
// override ignored "docs_deps" and kept the defaults. loadConfig is the only
// config loader, and it reads JSON.
func TestLoadConfigReadsDocsDepsCapsFromJSON(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	body := `{"max_files": 4, "docs_deps": {"max_changed_lines": 40, "max_lockfile_changes": 60, "max_files": 2}}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	want := greenlane.SizeLimits{MaxChangedLines: 40, MaxLockfileChanges: 60, MaxFiles: 2}
	if got.DocsDeps != want {
		t.Errorf("DocsDeps = %+v, want %+v", got.DocsDeps, want)
	}
	if got.MaxFiles != 4 {
		t.Errorf("MaxFiles = %d, want 4", got.MaxFiles)
	}
	// Keys the override omits keep their defaults.
	def := greenlane.DefaultConfig()
	if got.MaxChangedLines != def.MaxChangedLines || len(got.DeniedPaths) != len(def.DeniedPaths) {
		t.Errorf("omitted keys lost their defaults: %+v", got)
	}
}

func TestLoadConfigDefaultsWithoutFile(t *testing.T) {
	got, err := loadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if got.DocsDeps != greenlane.DefaultConfig().DocsDeps {
		t.Errorf("DocsDeps = %+v", got.DocsDeps)
	}
}
