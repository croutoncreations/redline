package appdir_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/croutoncreations/redline/internal/appdir"
)

func TestDefaultMatchesPlatformConvention(t *testing.T) {
	got, err := appdir.Default()
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("Default() returned an empty path")
	}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, "Library", "Application Support", "Redline")
		if got != want {
			t.Fatalf("Default() = %q, want %q", got, want)
		}
	default:
		configDir, err := os.UserConfigDir()
		if err != nil {
			home, homeErr := os.UserHomeDir()
			if homeErr != nil {
				t.Fatal(homeErr)
			}
			want := filepath.Join(home, ".redline")
			if got != want {
				t.Fatalf("Default() = %q, want fallback %q", got, want)
			}
			return
		}
		want := filepath.Join(configDir, "redline")
		if got != want {
			t.Fatalf("Default() = %q, want %q", got, want)
		}
	}
}
