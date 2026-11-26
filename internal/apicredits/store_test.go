package apicredits

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// The platform store keeps secrets in the Keychain on macOS and in private
// files elsewhere. On macOS the test uses a unique synthetic service so the
// real items are never read or changed.
func TestPlatformSecretStoreLifecycle(t *testing.T) {
	service := fmt.Sprintf("ai.redline.test.api-credits.%d.%d", os.Getpid(), time.Now().UnixNano())
	store := newPlatformSecretStore(t.TempDir(), service)
	ctx := context.Background()
	t.Cleanup(func() {
		_ = store.Clear(ctx, "acct", SecretSessionKey)
		_ = store.Clear(ctx, "acct", SecretAPIKey)
	})
	if _, err := store.Load(ctx, "acct", SecretSessionKey); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("initial = %v", err)
	}
	if err := store.Replace(ctx, "acct", SecretSessionKey, fakeSessionKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, "acct", SecretAPIKey, "sk-proj-FAKE"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(ctx, "acct", SecretSessionKey); err != nil || got != fakeSessionKey {
		t.Fatalf("session = %q, %v", got, err)
	}
	if got, err := store.Load(ctx, "acct", SecretAPIKey); err != nil || got != "sk-proj-FAKE" {
		t.Fatalf("api = %q, %v", got, err)
	}
	if err := store.Clear(ctx, "acct", SecretSessionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "acct", SecretSessionKey); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("after clear = %v", err)
	}
	if runtime.GOOS == "darwin" {
		if _, ok := store.(keychainSecretStore); !ok {
			t.Logf("store = %T (no Keychain in this build)", store)
		}
	}
}
