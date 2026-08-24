package artifacts_test

import (
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/jfox/redline/internal/artifacts"
)

func TestReaderReturnsBoundedTail(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "run.stdout.jsonl")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (artifacts.Reader{Root: root, MaxBytes: 8}).ReadTail(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "6789" || !result.Truncated || result.SizeBytes != 10 {
		t.Fatalf("result = %#v", result)
	}
}

func TestReaderRejectsPathOutsideArtifactRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (artifacts.Reader{Root: root}).ReadTail(outside, 10); err == nil {
		t.Fatal("expected containment error")
	}
}

func TestReaderRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "run.log")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (artifacts.Reader{Root: root}).ReadTail(link, 10); err == nil {
		t.Fatal("expected symlink containment error")
	}
}

func TestReaderClampsRequestedBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "run.log")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (artifacts.Reader{Root: root, MaxBytes: 3}).ReadTail(path, 100)
	if err != nil || result.Content != "789" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestReaderDoesNotSplitMultiByteRuneAtTailStart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "run.log")
	content := "abc世界xyz" // 'abc' (3 bytes) + '世' + '界' (3 bytes each) + 'xyz' (3 bytes) = 12 bytes
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// requestedBytes=8 puts start at byte 4, landing mid-rune inside '世' (bytes 3-6).
	result, err := (artifacts.Reader{Root: root, MaxBytes: 8}).ReadTail(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(result.Content) {
		t.Fatalf("ReadTail produced invalid UTF-8: %q (bytes %v)", result.Content, []byte(result.Content))
	}
	if result.Content != "界xyz" {
		t.Fatalf("Content = %q, want %q", result.Content, "界xyz")
	}
}
