package store_test

import (
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
