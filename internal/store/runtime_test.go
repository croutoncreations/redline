package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/store"
)

func TestRuntimeConnectionAndAgentContextRoundTrip(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/redline.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	connection := domain.RuntimeConnection{
		ID: "hermes-pi", Runtime: "hermes", Transport: "gateway",
		URL: "http://gateway.test:9119", CredentialSource: "hermes_desktop",
		DesktopConfigPath: "/tmp/connection.json", MaxConcurrentRuns: 2,
	}
	if err := db.CreateRuntimeConnection(t.Context(), connection, now); err != nil {
		t.Fatal(err)
	}
	context := domain.AgentContext{
		ID: "hermes-default", RuntimeConnectionID: connection.ID, Profile: "default",
		Project: "redline", WorkingDirectory: "/srv/redline", SessionMode: "isolated",
	}
	if err := db.CreateAgentContext(t.Context(), context, now); err != nil {
		t.Fatal(err)
	}
	gotConnection, err := db.GetRuntimeConnection(t.Context(), connection.ID)
	if err != nil || gotConnection.URL != connection.URL || gotConnection.MaxConcurrentRuns != 2 {
		t.Fatalf("connection=%#v err=%v", gotConnection, err)
	}
	gotContext, err := db.GetAgentContext(t.Context(), context.ID)
	if err != nil || gotContext.Profile != "default" || gotContext.WorkingDirectory != "/srv/redline" {
		t.Fatalf("context=%#v err=%v", gotContext, err)
	}
}

func TestRuntimeConnectionValidationRejectsUnreachableGatewayDefinition(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/redline.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.CreateRuntimeConnection(t.Context(), domain.RuntimeConnection{
		ID: "bad", Runtime: "hermes", Transport: "gateway",
	}, time.Now())
	if err == nil {
		t.Fatal("expected gateway URL validation error")
	}
}

func TestRuntimeConnectionAndAgentContextUpdateAndDelete(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/redline.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	connection := domain.RuntimeConnection{
		ID: "hermes-remote", Runtime: "hermes", Transport: "gateway",
		URL: "https://old.example", CredentialSource: "environment",
		CredentialRef: "HERMES_OLD", MaxConcurrentRuns: 1,
	}
	if err := db.CreateRuntimeConnection(t.Context(), connection, now); err != nil {
		t.Fatal(err)
	}
	context := domain.AgentContext{
		ID: "hermes-default", RuntimeConnectionID: connection.ID,
		Profile: "default", WorkingDirectory: "/srv/old", SessionMode: "isolated",
	}
	if err := db.CreateAgentContext(t.Context(), context, now); err != nil {
		t.Fatal(err)
	}

	connection.URL = "https://new.example"
	connection.CredentialRef = "HERMES_NEW"
	connection.MaxConcurrentRuns = 3
	if err := db.UpdateRuntimeConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetRuntimeConnection(t.Context(), connection.ID)
	if err != nil || got.URL != connection.URL || got.CredentialRef != "HERMES_NEW" || got.MaxConcurrentRuns != 3 {
		t.Fatalf("connection=%#v err=%v", got, err)
	}

	if err := db.DeleteRuntimeConnection(t.Context(), connection.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("delete referenced connection error = %v", err)
	}
	if err := db.DeleteAgentContext(t.Context(), context.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRuntimeConnection(t.Context(), connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetRuntimeConnection(t.Context(), connection.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted connection error = %v", err)
	}
}
