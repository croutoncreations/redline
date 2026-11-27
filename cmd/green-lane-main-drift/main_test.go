package main

import (
	"fmt"
	"strings"
	"testing"
)

const (
	landing = "2222222222222222222222222222222222222222"
	pr      = "3333333333333333333333333333333333333333"
	base    = "4444444444444444444444444444444444444444"
)

func payload(baseSHA, headRef string) []byte {
	return []byte(fmt.Sprintf(`{"merge_group":{"base_sha":%q,"head_ref":%q,"head_sha":"1111111111111111111111111111111111111111"}}`, baseSHA, headRef))
}

func fakeGit(changed []string, fail string) gitFunc {
	return func(args ...string) (string, error) {
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") && a != "origin" && len(a) != 40 {
				return "", fmt.Errorf("git given an invalid revision %q", a)
			}
		}
		if args[0] == fail {
			return "", fmt.Errorf("boom")
		}
		switch args[0] {
		case "fetch":
			return "", nil
		case "merge-base":
			if args[2] != pr {
				return "", fmt.Errorf("wrong merge-base %v", args)
			}
			return base, nil
		case "diff":
			if args[len(args)-2] != base {
				return "", fmt.Errorf("wrong range %v", args)
			}
			return strings.Join(changed, "\n"), nil
		}
		return "", fmt.Errorf("unexpected git %v", args)
	}
}

func TestMainDriftCheck(t *testing.T) {
	ref := "refs/heads/gh-readonly-queue/main/pr-12-" + pr
	good := payload(landing, ref)
	for _, tc := range []struct {
		name    string
		event   string
		payload []byte
		changed []string
		fail    string
		ok      bool
	}{
		{"pull_request passes", "pull_request", nil, nil, "", true},
		{"push passes", "push", nil, nil, "", true},
		{"no drift", "merge_group", good, nil, "", true},
		{"harmless drift", "merge_group", good, []string{"docs/a.md", "internal/cli/x.go"}, "", true},
		{"auth drift", "merge_group", good, []string{"internal/apiauth/check.go"}, "", false},
		{"workflow drift", "merge_group", good, []string{".github/workflows/ci.yml"}, "", false},
		{"lane drift", "merge_group", good, []string{"internal/greenlane/rules.go"}, "", false},
		{"bad payload", "merge_group", []byte("not json"), nil, "", false},
		{"short base sha", "merge_group", payload("2222", ref), nil, "", false},
		{"missing base sha", "merge_group", payload("", ref), nil, "", false},
		{"non-hex base sha", "merge_group", payload(strings.Repeat("Z", 40), ref), nil, "", false},
		{"head ref not a queue ref", "merge_group", payload(landing, "refs/heads/feature"), nil, "", false},
		{"head ref with short sha", "merge_group", payload(landing, "refs/heads/gh-readonly-queue/main/pr-12-3333"), nil, "", false},
		{"fetch fails", "merge_group", good, nil, "fetch", false},
		{"merge-base fails", "merge_group", good, nil, "merge-base", false},
		{"diff fails", "merge_group", good, nil, "diff", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.event, tc.payload, fakeGit(tc.changed, tc.fail))
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v want ok=%v", err, tc.ok)
			}
		})
	}
}
