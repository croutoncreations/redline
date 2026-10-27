package store

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestDeviceSessionsMigrationAndUsage(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	// Simulate a stamped schema: migration 41 recorded but the table missing.
	if _, err := db.db.Exec(`DROP TABLE device_sessions; INSERT OR IGNORE INTO schema_migrations(version) VALUES(41)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("device-token")))
	if err := db.CreateDeviceSession(ctx, DeviceSession{ID: "device", Name: "Browser", Kind: "browser", CreatedAt: at, LastUsedAt: at}, hash, "issuer-a"); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AuthenticateDeviceSession(ctx, hash, "issuer-b", at); err != ErrNotFound {
		t.Fatalf("session accepted under another master token: err=%v", err)
	}
	for _, seconds := range []int{30, 60, 90} {
		if _, err := db.AuthenticateDeviceSession(ctx, hash, "issuer-a", at.Add(time.Duration(seconds)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	devices, err := db.DeviceSessions(ctx)
	if err != nil || len(devices) != 1 || !devices[0].LastUsedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("devices=%+v err=%v", devices, err)
	}
	if err := db.RevokeDeviceSession(ctx, "device", at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AuthenticateDeviceSession(ctx, hash, "issuer-a", at.Add(3*time.Minute)); err != ErrNotFound {
		t.Fatalf("revoked err=%v", err)
	}
}
