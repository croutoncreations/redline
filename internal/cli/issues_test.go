package cli_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

func TestIssueCommandsUseServiceAPI(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, path string
	}{
		{[]string{"issue-source", "list"}, "GET", "/v1/issue-sources"},
		{[]string{"issue-source", "preview", "backlog"}, "GET", "/v1/issue-sources/backlog/preview"},
		{[]string{"issue-source", "sync", "backlog"}, "POST", "/v1/issue-sources/backlog/sync"},
		{[]string{"issue-source", "disable", "backlog"}, "POST", "/v1/issue-sources/backlog/disable"},
		{[]string{"issue", "list"}, "GET", "/v1/issues"},
		{[]string{"issue", "retry", "item-1"}, "POST", "/v1/issues/item-1/retry"},
		{[]string{"issue", "retry-delivery", "task-1"}, "POST", "/v1/issue-delivery/task-1/retry"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("request=%s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`[]`))
			}))
			defer server.Close()
			var out, stderr bytes.Buffer
			if code := cli.Run(append([]string{"--api", server.URL, "--config", filepath.Join(t.TempDir(), "absent.yaml")}, tc.args...), &out, &stderr, time.Now); code != 0 || calls != 1 {
				t.Fatalf("code=%d calls=%d stderr=%s", code, calls, &stderr)
			}
		})
	}
}
func TestIssueSourceAddReadsYAMLWithoutOpeningDatabase(t *testing.T) {
	file := filepath.Join(t.TempDir(), "source.yaml")
	if e := os.WriteFile(file, []byte("id: backlog\nrepository: owner/repo\nauth: public\nexecution_profile_id: local\nenabled: true\n"), 0600); e != nil {
		t.Fatal(e)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/issue-sources" {
			t.Error("wrong endpoint")
		}
		w.Write([]byte(`{"id":"backlog"}`))
	}))
	defer server.Close()
	var out, stderr bytes.Buffer
	if code := cli.Run([]string{"--api", server.URL, "issue-source", "add", "--file", file}, &out, &stderr, time.Now); code != 0 || calls != 1 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
}
