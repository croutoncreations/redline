package artifacts_test

import (
	"os"
	"path/filepath"
	"testing"

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
