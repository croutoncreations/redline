package demo_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/demo"
)

func TestScenariosAreStableAndDocumented(t *testing.T) {
	want := []string{"overview", "running", "attention", "empty", "decision-wait", "decision-run", "decision-run-near-expiry", "decision-unknown"}
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

func TestDecisionScenariosUseProductionEvaluationForEachProvider(t *testing.T) {
	now := time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		scenario string
		provider string
		want     decision.Decision
		mode     decision.Mode
		short    bool
	}{
		{"decision-wait", "claude-main", decision.Wait, decision.ModeSlots, true},
		{"decision-run", "claude-main", decision.Admit, decision.ModeSlots, true},
		{"decision-run-near-expiry", "claude-main", decision.Admit, decision.ModeSlots, true},
		{"decision-unknown", "claude-main", decision.Unknown, "", true},
		{"decision-wait", "codex-main", decision.Wait, decision.ModePace, false},
		{"decision-run", "codex-main", decision.Admit, decision.ModePace, false},
		{"decision-run-near-expiry", "codex-main", decision.Admit, decision.ModePace, false},
		{"decision-unknown", "codex-main", decision.Unknown, "", false},
	} {
		t.Run(tc.scenario+"/"+tc.provider, func(t *testing.T) {
			env, err := demo.CreateForProvider(t.Context(), tc.scenario, tc.provider, t.TempDir(), now)
			if err != nil {
				t.Fatal(err)
			}
			defer env.Close()

			snapshot := env.Snapshots[tc.provider]
			if (snapshot.Short != nil) != tc.short {
				t.Fatalf("short window present = %v, want %v", snapshot.Short != nil, tc.short)
			}
			decisions, err := env.Database.ListSchedulerDecisions(t.Context(), tc.provider, 1)
			if err != nil || len(decisions) != 1 {
				t.Fatalf("decisions=%#v err=%v", decisions, err)
			}
			var payload struct {
				Result decision.Result `json:"result"`
			}
			if err := json.Unmarshal(decisions[0].DecisionJSON, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Result.Decision != tc.want || payload.Result.Mode != tc.mode {
				t.Fatalf("result=%#v want decision=%s mode=%s", payload.Result, tc.want, tc.mode)
			}
			tasks, err := env.Database.ListTasks(t.Context())
			if err != nil || len(tasks) != 1 || tasks[0].ID != "demo-decision-task" || tasks[0].Name != "Find and fix one real bug" {
				t.Fatalf("tasks=%#v err=%v", tasks, err)
			}
		})
	}
}

func TestDecisionScenarioRejectsUnknownProvider(t *testing.T) {
	_, err := demo.CreateForProvider(t.Context(), "decision-run", "other", t.TempDir(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "unknown demo provider") {
		t.Fatalf("error = %v", err)
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
	completedOutputs := 0
	for _, run := range runs {
		encoded := run.Summary + run.OutputFile + run.Workspace.Directory
		if strings.Contains(encoded, "/Users/jfox") || strings.Contains(encoded, "croutoncreations") {
			t.Fatalf("demo leaked owner-specific data: %q", encoded)
		}
		if run.State == "completed" {
			completedOutputs++
			if run.OutputFile == "" {
				t.Fatalf("completed demo run %s has no output artifact", run.ID)
			}
			relative, err := filepath.Rel(env.Config.RunArtifactsDir, run.OutputFile)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				t.Fatalf("output artifact escaped demo root: %q", run.OutputFile)
			}
			content, err := os.ReadFile(run.OutputFile)
			if err != nil || !strings.Contains(string(content), `"type":"result"`) {
				t.Fatalf("output artifact %q is not readable formatted demo output: %q err=%v", run.OutputFile, content, err)
			}
		}
	}
	if completedOutputs < 2 {
		t.Fatalf("completed demo outputs = %d, want at least 2", completedOutputs)
	}
	for _, provider := range []string{"claude-main", "codex-main"} {
		decisions, err := env.Database.ListSchedulerDecisions(context.Background(), provider, 10)
		if err != nil || len(decisions) != 1 {
			t.Fatalf("%s decisions=%#v err=%v", provider, decisions, err)
		}
		if strings.Contains(strings.ToLower(string(decisions[0].DecisionJSON)), "behind pace") || strings.Contains(strings.ToLower(string(decisions[0].DecisionJSON)), "behind the target") {
			t.Fatalf("demo decision uses inverted pace wording: %s", decisions[0].DecisionJSON)
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
