package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	landing = "2222222222222222222222222222222222222222"
	pr      = "3333333333333333333333333333333333333333"
	base    = "4444444444444444444444444444444444444444"
)

// The queue ref's SHA is the base, not the PR head (as GitHub sends it).
var ref = "refs/heads/gh-readonly-queue/main/pr-12-" + landing

func payload(baseSHA, headRef string) []byte {
	return []byte(fmt.Sprintf(`{"merge_group":{"base_sha":%q,"head_ref":%q,"head_sha":"1111111111111111111111111111111111111111"}}`, baseSHA, headRef))
}

func fakeGit(changed []string, fail string) gitFunc {
	return func(args ...string) (string, error) {
		key := args[0]
		if key == "merge-base" && args[1] == "--is-ancestor" {
			key = "is-ancestor"
		}
		if key == fail {
			return "", fmt.Errorf("boom")
		}
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") && a != "origin" && a != "HEAD" && len(a) != 40 {
				return "", fmt.Errorf("git given an invalid revision %q", a)
			}
		}
		switch key {
		case "fetch":
			if args[len(args)-2] != landing {
				return "", fmt.Errorf("fetching unexpected landing %v", args)
			}
			return "", nil
		case "is-ancestor":
			return "", nil
		case "merge-base":
			if args[1] != landing || args[2] != pr {
				return "", fmt.Errorf("wrong merge-base %v", args)
			}
			return base, nil
		case "diff":
			if args[len(args)-2] != base || args[len(args)-1] != landing {
				return "", fmt.Errorf("wrong range %v", args)
			}
			return strings.Join(changed, "\n"), nil
		}
		return "", fmt.Errorf("unexpected git %v", args)
	}
}

func headOf(sha string, err error) prHeadFunc {
	return func(n int) (string, error) {
		if n != 12 {
			return "", fmt.Errorf("asked for PR %d", n)
		}
		return sha, err
	}
}

func TestMainDriftCheck(t *testing.T) {
	good := payload(landing, ref)
	for _, tc := range []struct {
		name    string
		event   string
		payload []byte
		changed []string
		fail    string
		head    prHeadFunc
		ok      bool
	}{
		{"pull_request passes", "pull_request", nil, nil, "", nil, true},
		{"push passes", "push", nil, nil, "", nil, true},
		{"no drift", "merge_group", good, nil, "", headOf(pr, nil), true},
		{"harmless drift", "merge_group", good, []string{"docs/a.md", "internal/cli/x.go"}, "", headOf(pr, nil), true},
		{"auth drift", "merge_group", good, []string{"internal/apiauth/check.go"}, "", headOf(pr, nil), false},
		{"workflow drift", "merge_group", good, []string{".github/workflows/ci.yml"}, "", headOf(pr, nil), false},
		{"lane drift", "merge_group", good, []string{"internal/greenlane/rules.go"}, "", headOf(pr, nil), false},
		{"drift-check drift", "merge_group", good, []string{"cmd/green-lane-main-drift/main.go"}, "", headOf(pr, nil), false},
		{"bad payload", "merge_group", []byte("not json"), nil, "", headOf(pr, nil), false},
		{"short base sha", "merge_group", payload("2222", ref), nil, "", headOf(pr, nil), false},
		{"missing base sha", "merge_group", payload("", ref), nil, "", headOf(pr, nil), false},
		{"non-hex base sha", "merge_group", payload(strings.Repeat("Z", 40), ref), nil, "", headOf(pr, nil), false},
		{"head ref not a queue ref", "merge_group", payload(landing, "refs/heads/feature"), nil, "", headOf(pr, nil), false},
		{"PR head lookup fails", "merge_group", good, nil, "", headOf("", fmt.Errorf("403")), false},
		{"PR head malformed", "merge_group", good, nil, "", headOf("abc", nil), false},
		{"PR head not in the merge group", "merge_group", good, nil, "is-ancestor", headOf(pr, nil), false},
		{"fetch fails", "merge_group", good, nil, "fetch", headOf(pr, nil), false},
		{"merge-base fails", "merge_group", good, nil, "merge-base", headOf(pr, nil), false},
		{"diff fails", "merge_group", good, nil, "diff", headOf(pr, nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.event, tc.payload, fakeGit(tc.changed, tc.fail), tc.head)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v want ok=%v", err, tc.ok)
			}
		})
	}
}

// The queue ref's SHA suffix must never be used as the PR head: with the real
// GitHub layout (suffix == base_sha) that would make the drift range empty.
func TestQueueRefSuffixIsNotThePRHead(t *testing.T) {
	asked := false
	head := func(n int) (string, error) { asked = true; return pr, nil }
	if err := run("merge_group", payload(landing, ref), fakeGit([]string{"internal/apiauth/x.go"}, ""), head); err == nil {
		t.Fatal("sensitive drift passed")
	}
	if !asked {
		t.Fatal("PR head was not resolved through the API")
	}
}

func TestAPIPRHead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != "/repos/o/r/pulls/12" {
			w.WriteHeader(403)
			return
		}
		fmt.Fprintf(w, `{"number":12,"head":{"sha":%q}}`, pr)
	}))
	defer server.Close()
	if got, err := apiPRHead(server.URL, "o/r", "tok")(12); err != nil || got != pr {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := apiPRHead(server.URL, "o/r", "bad")(12); err == nil {
		t.Fatal("API error ignored")
	}
	if _, err := apiPRHead(server.URL, "o/r", "tok")(13); err == nil {
		t.Fatal("wrong PR accepted")
	}
	if _, err := apiPRHead("", "o/r", "tok")(12); err == nil {
		t.Fatal("missing API URL accepted")
	}
}
