package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type envStepWorkflow struct {
	Jobs map[string]struct {
		Steps []struct {
			ID  string            `yaml:"id"`
			Env map[string]string `yaml:"env"`
			Run string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// reproEnvScript returns the run script of the evaluate job's repro_env step,
// which copies repro outputs (text produced by code on the PR head) into
// $GITHUB_ENV for the privileged evaluate job.
func reproEnvScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(shadowWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	var workflow envStepWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.ID == "repro_env" {
				return step.Run
			}
		}
	}
	t.Fatal("no step with id repro_env in the shadow workflow")
	return ""
}

// parseEnvFile reads a $GITHUB_ENV file the way the Actions runner does:
// NAME=value lines, and NAME<<DELIMITER blocks closed by a line equal to the
// delimiter.
func parseEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if name, delimiter, ok := strings.Cut(line, "<<"); ok && !strings.Contains(name, "=") {
			var value []string
			closed := false
			for scanner.Scan() {
				if scanner.Text() == delimiter {
					closed = true
					break
				}
				value = append(value, scanner.Text())
			}
			if !closed {
				t.Fatalf("env block %s is never closed by %q", name, delimiter)
			}
			result[name] = strings.Join(value, "\n")
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed env line %q", line)
		}
		result[name] = value
	}
	return result
}

func runReproEnv(t *testing.T, env map[string]string) (map[string]string, error) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not available")
	}
	envFile := filepath.Join(t.TempDir(), "github_env")
	if err := os.WriteFile(envFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", reproEnvScript(t))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_ENV=" + envFile, "GITHUB_RUN_ID=12345"}
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("script output: %s", output)
	}
	return parseEnvFile(t, envFile), err
}

func keys(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

// The repro outputs are produced by code on the PR head. Written into
// $GITHUB_ENV of the evaluate job (checks: write), a crafted value must not
// be able to define variables of its own, such as NODE_OPTIONS for the later
// github-script steps. The old step closed its heredoc with
// __REPRO_EOF_${GITHUB_RUN_ID}__, which the PR can predict.
func TestReproEnvStepCannotBeMadeToSetOtherVariables(t *testing.T) {
	predictable := "__REPRO_EOF_12345__"
	hostile := "looks fine\n" + predictable + "\nNODE_OPTIONS=--require=/tmp/evil.js\nREPRO_MESSAGE<<X\nsecond"
	for name, env := range map[string]map[string]string{
		"message, tests found":  {"HAS_TESTS": "true", "REPRO_PASS_OUT": "true", "REPRO_MESSAGE_OUT": hostile, "REPRO_TEST_IDS_OUT": "TestA\n" + predictable + "\nEVIL=1"},
		"message, inconclusive": {"HAS_TESTS": "true", "REPRO_INCONCLUSIVE_OUT": "true", "REPRO_MESSAGE_OUT": hostile},
		"no-tests reason":       {"HAS_TESTS": "false", "NO_TESTS_REASON": hostile},
		"flag carrying a line":  {"HAS_TESTS": "true", "REPRO_PASS_OUT": "true\nNODE_OPTIONS=--require=/tmp/evil.js", "REPRO_MESSAGE_OUT": "ok"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := runReproEnv(t, env)
			if err != nil {
				t.Fatalf("step failed: %v", err)
			}
			allowed := map[string]bool{"REPRO_PASS": true, "REPRO_INCONCLUSIVE": true, "REPRO_MESSAGE": true, "REPRO_TEST_IDS": true}
			for _, key := range keys(got) {
				if !allowed[key] {
					t.Fatalf("PR-controlled output defined %s (env = %q)", key, got)
				}
			}
			if message, ok := got["REPRO_MESSAGE"]; ok && env["REPRO_MESSAGE_OUT"] == hostile && message != hostile {
				t.Fatalf("REPRO_MESSAGE = %q, want the whole message kept as data", message)
			}
			for _, flag := range []string{"REPRO_PASS", "REPRO_INCONCLUSIVE"} {
				if value, ok := got[flag]; ok && value != "true" && value != "false" && value != "" {
					t.Fatalf("%s = %q, want true, false or empty", flag, value)
				}
			}
		})
	}
}

// The ordinary paths still hand green-lane-check what it reads.
func TestReproEnvStepWritesTheReproResult(t *testing.T) {
	got, err := runReproEnv(t, map[string]string{
		"HAS_TESTS": "true", "REPRO_PASS_OUT": "true", "REPRO_MESSAGE_OUT": "2 added tests fail on base and pass on head",
		"REPRO_TEST_IDS_OUT": "TestA,TestB",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["REPRO_PASS"] != "true" || got["REPRO_MESSAGE"] != "2 added tests fail on base and pass on head" || got["REPRO_TEST_IDS"] != "TestA,TestB" {
		t.Fatalf("env = %q", got)
	}
	got, err = runReproEnv(t, map[string]string{"HAS_TESTS": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if got["REPRO_INCONCLUSIVE"] != "true" || got["REPRO_MESSAGE"] != "No test files changed in PR" {
		t.Fatalf("env = %q", got)
	}
}
