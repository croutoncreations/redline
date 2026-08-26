package demo_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/demo"
)

func TestScenariosAreStableAndDocumented(t *testing.T) {
	want := []string{"overview", "running", "attention", "empty"}
	got := demo.Scenarios()
	if len(got) != len(want) {
		t.Fatalf("scenarios = %#v", got)
	}
	for i, name := range want {
		if got[i].Name != name || got[i].Description == "" {
			t.Fatalf("scenario[%d] = %#v", i, got[i])
		}
	}
}

func TestOverviewCreatesIsolatedSyntheticState(t *testing.T) {
	now := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	env, err := demo.Create(context.Background(), "overview", t.TempDir(), now)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()

	if env.Config.DemoScenario != "overview" || len(env.Config.Providers) != 2 {
		t.Fatalf("config = %#v", env.Config)
	}
	tasks, err := env.Database.ListTasks(context.Background())
	if err != nil || len(tasks) < 3 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	runs, err := env.Database.ListRuns(context.Background(), 20)
	if err != nil || len(runs) < 2 {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
	for _, run := range runs {
		encoded := run.Summary + run.OutputFile + run.Workspace.Directory
		if strings.Contains(encoded, "/Users/jfox") || strings.Contains(encoded, "croutoncreations") {
			t.Fatalf("demo leaked owner-specific data: %q", encoded)
		}
	}
}

func TestScenarioRunStates(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		count       int
	}{
		{"running", "running", 1},
		{"attention", "failed", 1},
		{"empty", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := demo.Create(context.Background(), tc.name, t.TempDir(), time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer env.Close()
			runs, err := env.Database.ListRuns(context.Background(), 20)
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, run := range runs {
				if string(run.State) == tc.state {
					found++
				}
			}
			if found != tc.count {
				t.Fatalf("%s runs = %d, want %d; all=%#v", tc.state, found, tc.count, runs)
			}
		})
	}
}

func TestUnknownScenarioIsRejected(t *testing.T) {
	if _, err := demo.Create(context.Background(), "surprise", t.TempDir(), time.Now()); err == nil {
		t.Fatal("expected unknown scenario error")
	}
}
