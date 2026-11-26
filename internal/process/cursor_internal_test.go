package process

import (
	"runtime"
	"slices"
	"testing"
)

// Cursor credentials can authenticate the CLI as a different account or send
// its token to another endpoint. Every spelling of these variables must be
// removed before Redline starts the CLI, including mixed case on Windows.
func TestWithoutCursorCredentialsDropsEveryCredentialVariable(t *testing.T) {
	inherited := []string{
		"CURSOR_API_KEY=key",
		"CURSOR_AUTH_TOKEN=token",
		"CURSOR_API_ENDPOINT=https://cursor.example",
		"Cursor_Api_Key=mixed-case-key",
		"CURSOR_API_KEY_BACKUP=keep",
		"PATH=/usr/bin:/bin",
	}
	wantWindows := []string{"CURSOR_API_KEY_BACKUP=keep", "PATH=/usr/bin:/bin"}
	wantCurrent := []string{"Cursor_Api_Key=mixed-case-key", "CURSOR_API_KEY_BACKUP=keep", "PATH=/usr/bin:/bin"}
	if runtime.GOOS == "windows" {
		wantCurrent = wantWindows
	}
	if got := WithoutCursorCredentials(inherited); !slices.Equal(got, wantCurrent) {
		t.Fatalf("current platform credential filtering = %v, want %v", got, wantCurrent)
	}
	if got := withoutCursorCredentials("windows", inherited); !slices.Equal(got, wantWindows) {
		t.Fatalf("windows kept credential variables: %v", got)
	}
	wantDarwin := []string{"Cursor_Api_Key=mixed-case-key", "CURSOR_API_KEY_BACKUP=keep", "PATH=/usr/bin:/bin"}
	if got := withoutCursorCredentials("darwin", inherited); !slices.Equal(got, wantDarwin) {
		t.Fatalf("darwin credential filtering = %v, want %v", got, wantDarwin)
	}
}
