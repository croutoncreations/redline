package mcpserver_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRedlineCommandServesMCPOverStdio(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/tasks" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `[{"id":"stdio-task","name":"Stdio task","priority":10,"state":"queued"}]`)
	}))
	defer api.Close()

	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	binary := filepath.Join(t.TempDir(), "redline")
	build := exec.Command("go", "build", "-o", binary, "./cmd/redline")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build redline: %v\n%s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	command := exec.Command(binary, "--api", api.URL, "mcp")
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "redline_tasks_list", Arguments: map[string]any{"limit": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || contentText(result) == "" {
		t.Fatalf("stdio tool result = %#v", result)
	}
}
