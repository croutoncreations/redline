// Package appdir resolves Redline's platform-default data directory: the
// location the bundled macOS app, the CLI, and the service all treat as the
// standard place for a config file, database, and API token when the caller
// has not been given an explicit path.
package appdir

import (
	"os"
	"path/filepath"
	"runtime"
)

// Default returns Redline's platform-default data directory.
//
// On darwin this is always "~/Library/Application Support/Redline",
// unchanged from Redline's original macOS-only behavior, so existing
// installs (including the bundled macOS app) keep working without
// migration. On other platforms it is os.UserConfigDir()+"/redline" (for
// example "~/.config/redline" on Linux, "%AppData%\redline" on Windows). If
// os.UserConfigDir fails (no HOME/APPDATA/XDG_CONFIG_HOME available), it
// falls back to the user's home directory joined with ".redline".
func Default() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Redline"), nil
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(configDir, "redline"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".redline"), nil
}
