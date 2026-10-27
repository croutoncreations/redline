package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPushRegistrationsMigrationAndActiveSessions(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	if _, err := db.db.Exec(`DROP TABLE push_registrations; INSERT OR IGNORE INTO schema_migrations(version) VALUES(42)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var version, stamps int
	if err := db.db.QueryRow(`SELECT MAX(version), COUNT(CASE WHEN version=42 THEN 1 END) FROM schema_migrations`).Scan(&version, &stamps); err != nil || version != 42 || stamps != 1 {
		t.Fatalf("version=%d stamps=%d err=%v", version, stamps, err)
	}
	at := time.Now().UTC()
	for _, id := range []string{"active", "revoked", "rotated"} {
		issuer := "current"
		if id == "rotated" {
			issuer = "old"
		}
		if err := db.CreateDeviceSession(ctx, DeviceSession{ID: id, Name: id, Kind: "mobile", CreatedAt: at, LastUsedAt: at}, id, issuer); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertPushRegistration(ctx, PushRegistration{DeviceSessionID: id, Platform: "android", Provider: "example", Token: "original-token"}, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RevokeDeviceSession(ctx, "revoked", at); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPushRegistration(ctx, PushRegistration{DeviceSessionID: "active", Platform: "ios", Provider: "example", Token: "replacement-token"}, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	registrations, err := db.ActivePushRegistrations(ctx, "current")
	if err != nil || len(registrations) != 1 || registrations[0].Token != "replacement-token" {
		t.Fatalf("active count=%d err=%v", len(registrations), err)
	}
	// The send-time lookup returns the device's current token with the same filters.
	for _, c := range []struct {
		id, token string
		want      bool
	}{
		{"active", "replacement-token", true}, // follows a refresh made while queued
		{"revoked", "", false},                // session revoked
		{"rotated", "", false},                // issued under an old master token
		{"missing", "", false},
	} {
		got, found, err := db.ActivePushRegistration(ctx, "current", c.id)
		if err != nil || found != c.want || got.Token != c.token || (found && (got.Platform != "ios" || got.Provider != "example")) {
			t.Fatalf("%s: got=%+v found=%v err=%v", c.id, got, found, err)
		}
	}
	if err := db.DeletePushRegistrationToken(ctx, "active", "original-token"); err != nil {
		t.Fatal(err)
	}
	registrations, err = db.ActivePushRegistrations(ctx, "current")
	if err != nil || len(registrations) != 1 {
		t.Fatal("stale rejection deleted a replacement registration")
	}
	if err := db.DeletePushRegistration(ctx, "active"); err != nil {
		t.Fatal(err)
	}
	registrations, err = db.ActivePushRegistrations(ctx, "current")
	if err != nil || len(registrations) != 0 {
		t.Fatalf("remaining=%d err=%v", len(registrations), err)
	}
}
