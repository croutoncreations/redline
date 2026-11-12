package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func TestRuntimeConnectionAndAgentContextRoundTrip(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// TestRuntimeConnectionValidationRejectsInvalidInputs exercises the branches
// of validateRuntimeConnection that were not reached by the existing tests.
// The bug class: a caller could persist a connection with an unsupported
// runtime or transport, or omit the credential ref for file/environment
// sources.  All of these would produce a silent misconfiguration that only
// surfaces at dispatch time.
func TestRuntimeConnectionValidationRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	db, err := store.Open(t.TempDir() + "/redline.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	base := domain.RuntimeConnection{
		ID: "ok", Runtime: "hermes", Transport: "local",
	}

	cases := []struct {
		name string
		item domain.RuntimeConnection
	}{
		{
			name: "missing id",
			item: domain.RuntimeConnection{Runtime: "hermes", Transport: "local"},
		},
		{
			name: "unsupported runtime",
			item: domain.RuntimeConnection{ID: "x", Runtime: "unknown", Transport: "local"},
		},
		{
			name: "unsupported transport",
			item: domain.RuntimeConnection{ID: "x", Runtime: "hermes", Transport: "tcp"},
		},
		{
			name: "negative max_concurrent_runs",
			item: domain.RuntimeConnection{ID: "x", Runtime: "hermes", Transport: "local", MaxConcurrentRuns: -1},
		},
		{
			name: "environment credential without ref",
			item: domain.RuntimeConnection{
				ID: "x", Runtime: "hermes", Transport: "gateway",
				URL: "http://gw.test", CredentialSource: "environment",
			},
		},
		{
			name: "file credential without ref",
			item: domain.RuntimeConnection{
				ID: "x", Runtime: "hermes", Transport: "gateway",
				URL: "http://gw.test", CredentialSource: "file",
			},
		},
		{
			name: "unsupported credential source",
			item: domain.RuntimeConnection{
				ID: "x", Runtime: "hermes", Transport: "local", CredentialSource: "vault",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.CreateRuntimeConnection(t.Context(), tc.item, time.Now()); err == nil {
				t.Fatalf("expected validation error for %q, got nil", tc.name)
			}
		})
	}

	// Sanity check: the base valid connection must be accepted.
	if err := db.CreateRuntimeConnection(t.Context(), base, time.Now()); err != nil {
		t.Fatalf("valid connection rejected: %v", err)
	}
}

func TestRuntimeConnectionAndAgentContextUpdateAndDelete(t *testing.T) {
	t.Parallel()
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

func TestUpdateRunExternalOnlyMutatesActiveRuns(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	original := domain.ExternalRun{
		RuntimeConnectionID: "hermes-primary", RunID: "external-original", SessionID: "session-original",
	}
	late := domain.ExternalRun{
		RuntimeConnectionID: "hermes-late", RunID: "external-late", SessionID: "session-late",
	}

	for _, tc := range []struct {
		name   string
		state  domain.RunState
		active bool
	}{
		{name: "preparing", state: domain.RunPreparing, active: true},
		{name: "running", state: domain.RunRunning, active: true},
		{name: "completed", state: domain.RunCompleted},
		{name: "failed", state: domain.RunFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTaskDB(t)
			profile := domain.ExecutionProfile{
				ID: "profile", ProviderAccountID: "codex-main",
				HarnessType: "codex-cli", WorkspaceProvider: "existing-directory",
			}
			if err := db.CreateProfile(t.Context(), profile, now); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateTask(t.Context(), domain.Task{
				ID: "task", Name: "external identity", ExecutionProfileID: profile.ID, Type: domain.OneOff,
			}, now); err != nil {
				t.Fatal(err)
			}
			if _, err := db.AdmitTask(t.Context(), "run", "task", "codex-main", "", now); err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateRunExternal(t.Context(), "run", original); err != nil {
				t.Fatal(err)
			}
			if tc.state != domain.RunPreparing {
				if err := db.MarkRunRunning(t.Context(), "run", domain.Workspace{Directory: "/repo"}); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.active {
				if err := db.CompleteRun(t.Context(), "run", domain.RunCompletion{
					State: tc.state, FinalizeState: "completed",
				}, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			}

			err := db.UpdateRunExternal(t.Context(), "run", late)
			if tc.active && err != nil {
				t.Fatalf("UpdateRunExternal() error = %v", err)
			}
			if !tc.active && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("UpdateRunExternal() error = %v, want ErrNotFound", err)
			}

			got, err := db.GetRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			want := original
			if tc.active {
				want = late
			}
			if got.External != want {
				t.Fatalf("external identity = %#v, want %#v", got.External, want)
			}
		})
	}

	t.Run("missing", func(t *testing.T) {
		db := openTaskDB(t)
		if err := db.UpdateRunExternal(t.Context(), "missing", late); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("UpdateRunExternal() error = %v, want ErrNotFound", err)
		}
	})
}
