package artifacts_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/croutoncreations/redline/internal/artifacts"
)

func TestReadFileTailReturnsTrailingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.stdout")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	for limit, want := range map[int64]string{4: "6789", 10: "0123456789", 100: "0123456789"} {
		got, err := artifacts.ReadFileTail(path, limit)
		if err != nil || string(got) != want {
			t.Fatalf("limit %d: got %q, err %v, want %q", limit, got, err, want)
		}
	}
}

func TestReadFileTailSurfacesReadErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := artifacts.ReadFileTail(filepath.Join(dir, "missing"), 10); err == nil {
		t.Fatal("expected error for missing file")
	}
	// A directory opens and stats fine but cannot be read; the caller must see
	// the error rather than a zero-filled buffer.
	if _, err := artifacts.ReadFileTail(dir, 10); err == nil {
		t.Fatal("expected error for directory")
	}
}
