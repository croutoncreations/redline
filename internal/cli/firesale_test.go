package cli_test

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

func TestTaskCommandsSendFiresaleInterval(t *testing.T) {
	for _, args := range [][]string{
		{"task", "add", "--name", "Job", "--prompt", "Test", "--profile", "p", "--tier", "firesale", "--firesale-interval", "10m"},
		{"later", "--profile", "p", "--tier", "firesale", "--firesale-interval", "10m", "Test"},
	} {
		t.Run(args[0], func(t *testing.T) {
			api := &taskAPI{t: t, profiles: "[]"}
			server := httptest.NewServer(api.handler())
			defer server.Close()
			var stdout, stderr bytes.Buffer
			exit := cli.RunWithInput(append([]string{"--api", server.URL}, args...), strings.NewReader(""), &stdout, &stderr, time.Now)
			if exit != 0 {
				t.Fatalf("exit=%d stderr=%s", exit, &stderr)
			}
			if api.created["firesale_min_interval"] != "10m" || api.created["dispatch_tier"] != "firesale" {
				t.Fatalf("body=%+v", api.created)
			}
		})
	}
}
