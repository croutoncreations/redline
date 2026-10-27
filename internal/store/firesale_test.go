package store_test

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func TestLatestAutomaticFiresaleSkipsUnevaluatedAndManualDecisions(t *testing.T) {
	db := openTaskDB(t)
	if _, err := db.LatestAutomaticFiresale(t.Context(), "p"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing state: %v", err)
	}
	for _, record := range []struct{ provider, json string }{
		{"p", `{"trigger":"automatic","provider_firesale":true}`},
		{"p", `{"trigger":"manual","provider_firesale":false}`},
		{"p", `{"trigger":"automatic","result":{"firesale":false}}`},
		{"p", `{"trigger":"automatic","provider_firesale":false,"result":{"decision":"UNKNOWN"}}`},
		{"other", `{"trigger":"automatic","provider_firesale":false}`},
	} {
		if _, err := db.RecordSchedulerDecision(t.Context(), domain.SchedulerDecision{ProviderAccountID: record.provider, DecisionJSON: []byte(record.json)}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := db.LatestAutomaticFiresale(t.Context(), "p"); err != nil || !got {
		t.Fatalf("state=%v err=%v", got, err)
	}
	if _, err := db.RecordSchedulerDecision(t.Context(), domain.SchedulerDecision{ProviderAccountID: "p", DecisionJSON: []byte(`{"trigger":"automatic","provider_firesale":false}`)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err := db.LatestAutomaticFiresale(t.Context(), "p"); err != nil || got {
		t.Fatalf("state=%v err=%v", got, err)
	}
}

func TestTaskFiresaleIntervalRoundTrip(t *testing.T) {
	db := openTaskDB(t)
	now := time.Now().UTC()
	profile := domain.ExecutionProfile{ID: "p", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "job", Name: "Job", ExecutionProfileID: profile.ID, Type: domain.Recurring, MinInterval: time.Hour, FiresaleMinInterval: time.Minute, DispatchTier: domain.DispatchFiresale}
	if err := db.CreateTask(t.Context(), task, now); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetTask(t.Context(), task.ID)
	if err != nil || got.FiresaleMinInterval != time.Minute || got.DispatchTier != domain.DispatchFiresale {
		t.Fatalf("task=%+v err=%v", got, err)
	}
	task.FiresaleMinInterval = 2 * time.Hour
	if err := db.UpdateTask(t.Context(), task, now); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetTask(t.Context(), task.ID)
	if err != nil || got.FiresaleMinInterval != 2*time.Hour {
		t.Fatalf("task=%+v err=%v", got, err)
	}
	task.FiresaleMinInterval = -time.Minute
	if err := db.UpdateTask(t.Context(), task, now); err == nil || err.Error() != "firesale minimum interval cannot be negative" {
		t.Fatalf("negative interval error=%v", err)
	}
}

func TestMigration34RepairsSiblingSchemaVersions(t *testing.T) {
	for _, stamp := range []int{30, 31, 34} {
		t.Run(fmt.Sprint(stamp), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sibling.db")
			db, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = raw.Exec(`ALTER TABLE usage_snapshots DROP COLUMN banked_reset_details;
DROP TABLE banked_reset_notices;
ALTER TABLE tasks DROP COLUMN firesale_min_interval_ns;
ALTER TABLE tasks ADD COLUMN old_tier TEXT NOT NULL DEFAULT 'behind' CHECK(old_tier IN ('behind', 'well_behind', 'expiring'));
ALTER TABLE tasks DROP COLUMN dispatch_tier;
ALTER TABLE tasks RENAME COLUMN old_tier TO dispatch_tier;
ALTER TABLE tasks ADD COLUMN dispatch_tier_v30 TEXT NOT NULL DEFAULT 'behind' CHECK(dispatch_tier_v30 IN ('behind', 'well_behind', 'expiring', 'firesale'));
DELETE FROM schema_migrations WHERE version > 27;`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, stamp); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			for range 2 {
				db, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			raw, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			for _, query := range []string{
				`SELECT banked_reset_details FROM usage_snapshots`,
				`SELECT firesale_min_interval_ns FROM tasks`,
				`SELECT * FROM banked_reset_notices`,
			} {
				rows, err := raw.Query(query)
				if err != nil {
					t.Fatal(err)
				}
				rows.Close()
			}
			var version int
			if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 39 {
				t.Fatalf("version=%d err=%v", version, err)
			}
			db, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Now().UTC()
			if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "p", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory"}, now); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateTask(t.Context(), domain.Task{ID: "job", Name: "Job", ExecutionProfileID: "p", Type: domain.Recurring, DispatchTier: domain.DispatchFiresale, FiresaleMinInterval: time.Minute}, now); err != nil {
				t.Fatal(err)
			}
			var before, after int
			if err := raw.QueryRow(`PRAGMA schema_version`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			db.Close()
			db, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := raw.QueryRow(`PRAGMA schema_version`).Scan(&after); err != nil || before != after {
				t.Fatalf("reopen changed schema: %d -> %d, err=%v", before, after, err)
			}
			task, err := db.GetTask(t.Context(), "job")
			if err != nil || task.DispatchTier != domain.DispatchFiresale || task.FiresaleMinInterval != time.Minute {
				t.Fatalf("reopen changed task: %+v, err=%v", task, err)
			}
		})
	}
}

func TestMigration34PreservesV29TasksAndReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "p", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "job", Name: "Existing", ExecutionProfileID: "p", Type: domain.Recurring, DispatchTier: domain.DispatchExpiring, MinInterval: time.Hour}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(t.Context(), "run", "job", "codex-main", "revision", now); err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Restore the exact v29 column definition while retaining a task and its run.
	_, err = raw.Exec(`ALTER TABLE tasks DROP COLUMN firesale_min_interval_ns;
 ALTER TABLE tasks ADD COLUMN old_tier TEXT NOT NULL DEFAULT 'behind' CHECK(old_tier IN ('behind', 'well_behind', 'expiring'));
 UPDATE tasks SET old_tier = dispatch_tier;
 ALTER TABLE tasks DROP COLUMN dispatch_tier;
 ALTER TABLE tasks RENAME COLUMN old_tier TO dispatch_tier;
 DELETE FROM schema_migrations WHERE version >= 30;
 INSERT OR IGNORE INTO schema_migrations(version) VALUES (29);`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task, err := db.GetTask(t.Context(), "job")
	if err != nil || task.Name != "Existing" || task.MinInterval != time.Hour || task.FiresaleMinInterval != 0 || task.DispatchTier != domain.DispatchExpiring || task.State != domain.Running {
		t.Fatalf("migrated=%+v err=%v", task, err)
	}
	if _, err := db.GetRun(t.Context(), "run"); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "new", Name: "New", ExecutionProfileID: "p", Type: domain.OneOff, DispatchTier: domain.DispatchFiresale}, now); err != nil {
		t.Fatal(err)
	}
}
