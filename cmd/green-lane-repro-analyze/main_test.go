package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseResults(t *testing.T) {
	// Create a test JSON file
	jsonContent := `{"Action":"run","Test":"TestFoo","Package":"github.com/example/pkg"}
{"Action":"output","Test":"TestFoo","Output":"=== RUN   TestFoo\n"}
{"Action":"fail","Test":"TestFoo","Package":"github.com/example/pkg","Elapsed":0.01}
{"Action":"run","Test":"TestBar","Package":"github.com/example/pkg"}
{"Action":"pass","Test":"TestBar","Package":"github.com/example/pkg","Elapsed":0.02}
{"Action":"skip","Test":"TestSkipped","Package":"github.com/example/pkg"}
`

	tmpfile, err := os.CreateTemp("", "test_results_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(jsonContent)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	tests, output := parseResults(tmpfile.Name())

	// Check test statuses
	if tests["TestFoo"] != "fail" {
		t.Errorf("TestFoo status = %q, want %q", tests["TestFoo"], "fail")
	}
	if tests["TestBar"] != "pass" {
		t.Errorf("TestBar status = %q, want %q", tests["TestBar"], "pass")
	}
	if tests["TestSkipped"] != "skip" {
		t.Errorf("TestSkipped status = %q, want %q", tests["TestSkipped"], "skip")
	}

	// Check output capture
	if !strings.Contains(output, "=== RUN   TestFoo") {
		t.Errorf("output doesn't contain expected text, got: %q", output)
	}
}

func TestParseResultsNonexistent(t *testing.T) {
	tests, output := parseResults("/nonexistent/file.json")

	if len(tests) != 0 {
		t.Errorf("expected empty tests map for nonexistent file, got %d entries", len(tests))
	}
	if output != "" {
		t.Errorf("expected empty output for nonexistent file, got %q", output)
	}
}

func TestReadTestIDs(t *testing.T) {
	content := `TestOne
TestTwo
  TestThree  
TestFour
`

	tmpfile, err := os.CreateTemp("", "test_ids_*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	ids := readTestIDs(tmpfile.Name())

	expected := []string{"TestOne", "TestTwo", "TestThree", "TestFour"}
	if len(ids) != len(expected) {
		t.Fatalf("got %d test IDs, want %d", len(ids), len(expected))
	}

	for i, id := range ids {
		if id != expected[i] {
			t.Errorf("test ID %d = %q, want %q", i, id, expected[i])
		}
	}
}

func TestReadTestIDsNonexistent(t *testing.T) {
	ids := readTestIDs("/nonexistent/file.txt")

	if len(ids) != 0 {
		t.Errorf("expected empty slice for nonexistent file, got %d entries", len(ids))
	}
}
