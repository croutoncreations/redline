package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type TestEvent struct {
	Action  string  `json:"Action"`
	Test    string  `json:"Test"`
	Package string  `json:"Package"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

func main() {
	// Parse both result files
	baseTests, baseOutput := parseResults("base_results.json")
	headTests, _ := parseResults("head_results.json")

	// Get test IDs from test_ids_unique.txt
	testIDs := readTestIDs("test_ids_unique.txt")

	// Check each test ID
	allPass := true
	var messages []string
	var failedTests []string

	for _, testID := range testIDs {
		baseStatus, baseOK := baseTests[testID]
		headStatus, headOK := headTests[testID]

		if !baseOK {
			messages = append(messages, fmt.Sprintf("%s: did not execute on base", testID))
			allPass = false
			continue
		}

		if !headOK {
			messages = append(messages, fmt.Sprintf("%s: did not execute on head", testID))
			allPass = false
			continue
		}

		// Base must have failed (executed and failed)
		if baseStatus != "fail" {
			messages = append(messages, fmt.Sprintf("%s: did not fail on base (status: %s)", testID, baseStatus))
			allPass = false
			continue
		}

		// Head must have passed
		if headStatus != "pass" {
			messages = append(messages, fmt.Sprintf("%s: did not pass on head (status: %s)", testID, headStatus))
			allPass = false
			continue
		}

		// Success!
		failedTests = append(failedTests, testID)
	}

	if allPass && len(failedTests) > 0 {
		fmt.Printf("pass=true\n")
		fmt.Printf("inconclusive=false\n")
		fmt.Printf("message=Tests %v failed on base, passed on head\n", failedTests)
		fmt.Printf("test_ids=%s\n", strings.Join(failedTests, ","))

		// Save base failure output
		if err := os.WriteFile("base_failure_output.txt", []byte(baseOutput), 0644); err == nil {
			fmt.Printf("base_output_file=base_failure_output.txt\n")
		}
	} else {
		fmt.Printf("pass=false\n")
		fmt.Printf("inconclusive=false\n")
		if len(messages) > 0 {
			fmt.Printf("message=%s\n", strings.Join(messages, "; "))
		} else {
			fmt.Printf("message=No tests to check\n")
		}
	}
}

func parseResults(filename string) (map[string]string, string) {
	tests := make(map[string]string)
	var output strings.Builder

	f, err := os.Open(filename)
	if err != nil {
		return tests, ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		var event TestEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		if event.Test != "" {
			if event.Action == "pass" {
				tests[event.Test] = "pass"
			} else if event.Action == "fail" {
				tests[event.Test] = "fail"
			} else if event.Action == "skip" {
				tests[event.Test] = "skip"
			}

			if event.Action == "output" && event.Output != "" {
				output.WriteString(event.Output)
			}
		}
	}

	return tests, output.String()
}

func readTestIDs(filename string) []string {
	var ids []string
	f, err := os.Open(filename)
	if err != nil {
		return ids
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			ids = append(ids, line)
		}
	}
	return ids
}
