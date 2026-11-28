//go:build !windows

package apicredits

import (
	"context"
	"github.com/croutoncreations/redline/internal/config"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A key file that is a FIFO with no writer is refused at once rather than
// blocking the fetch until someone writes to it.
func TestResolveSecretRefusesFIFOKeyFileWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "key")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ResolveSecret(context.Background(), nil, config.APICreditsVendorOpenAI, "acct", SecretSessionKey, fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read a FIFO key file")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening a FIFO key file blocked")
	}
}
