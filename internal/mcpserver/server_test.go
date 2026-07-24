package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/apiclient"
	"github.com/jfox/redline/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, handler http.Handler) *mcp.ClientSession {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := mcpserver.New(apiclient.Client{BaseURL: api.URL, HTTPClient: api.Client()})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = server.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "redline-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestServerExposesAgentToolsWithSafetyAnnotations(t *testing.T) {
	session := connect(t, http.NotFoundHandler())
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{
		"redline_overview",
		"redline_provider_status",
		"redline_provider_capacity",
		"redline_tasks_list",
		"redline_task_get",
		"redline_profiles_list",
		"redline_runs_list",
		"redline_run_get",
		"redline_run_events",
		"redline_run_logs",
		"redline_task_create",
		"redline_task_update",
		"redline_task_control",
		"redline_provider_control",
		"redline_scheduler_evaluate",
		"redline_scheduler_dispatch",
	}
	names := make([]string, 0, len(result.Tools))
	tools := make(map[string]*mcp.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		tools[tool.Name] = tool
	}
	slices.Sort(names)
	slices.Sort(expected)
	if !slices.Equal(names, expected) {
		t.Fatalf("tools = %v, want %v", names, expected)
	}
	readOnly := []string{
		"redline_overview", "redline_provider_status", "redline_provider_capacity",
		"redline_tasks_list", "redline_task_get", "redline_profiles_list",
		"redline_runs_list", "redline_run_get", "redline_run_events", "redline_run_logs",
	}
	for _, name := range readOnly {
		if tools[name].Annotations == nil || !tools[name].Annotations.ReadOnlyHint {
			t.Errorf("%s should be annotated read-only", name)
		}
	}
	if tools["redline_scheduler_dispatch"].Annotations == nil ||
		tools["redline_scheduler_dispatch"].Annotations.ReadOnlyHint {
		t.Fatal("scheduler dispatch should be visibly mutating")
	}
	if tools["redline_scheduler_dispatch"].Annotations.DestructiveHint == nil ||
		!*tools["redline_scheduler_dispatch"].Annotations.DestructiveHint {
		t.Fatal("scheduler dispatch should be annotated as potentially destructive")
	}
	if tools["redline_scheduler_dispatch"].Annotations.OpenWorldHint == nil ||
		!*tools["redline_scheduler_dispatch"].Annotations.OpenWorldHint {
		t.Fatal("scheduler dispatch should be annotated as potentially interacting with external systems")
	}
	if tools["redline_task_update"].Annotations.DestructiveHint == nil ||
		!*tools["redline_task_update"].Annotations.DestructiveHint {
		t.Fatal("task update should be annotated as potentially destructive")
	}
	if tools["redline_task_control"].Annotations.IdempotentHint {
		t.Fatal("task control should not be annotated idempotent because retry can requeue work")
	}
}

func TestReadToolsBoundListsAndLogTails(t *testing.T) {
	var requestedTail int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[
			{"id":"one","name":"One","priority":1,"state":"queued"},
			{"id":"two","name":"Two","priority":2,"state":"queued"},
			{"id":"three","name":"Three","priority":3,"state":"queued"}
		]`)
	})
	mux.HandleFunc("GET /v1/runs/run-1/logs", func(w http.ResponseWriter, r *http.Request) {
		requestedTail = queryInt(t, r.URL.Query(), "tail_bytes")
		fmt.Fprint(w, `{"stream":"stdout","content":"done\n","size":5,"truncated":false}`)
	})
	session := connect(t, mux)

	tasks := callTool(t, session, "redline_tasks_list", map[string]any{"limit": 2})
	if tasks.IsError {
		t.Fatalf("tasks error: %v", tasks.Content)
	}
	var taskOutput struct {
		Count     int               `json:"count"`
		Truncated bool              `json:"truncated"`
		Data      []json.RawMessage `json:"data"`
	}
	decodeStructured(t, tasks, &taskOutput)
	if taskOutput.Count != 2 || len(taskOutput.Data) != 2 || !taskOutput.Truncated {
		t.Fatalf("task output = %#v", taskOutput)
	}

	logs := callTool(t, session, "redline_run_logs", map[string]any{
		"run_id": "run-1", "stream": "stdout", "tail_bytes": 999_999,
	})
	if logs.IsError {
		t.Fatalf("logs error: %v", logs.Content)
	}
	if requestedTail != 32*1024 {
		t.Fatalf("tail_bytes = %d, want %d", requestedTail, 32*1024)
	}
}

func TestTaskResultsBoundLargePrompts(t *testing.T) {
	largePrompt := strings.Repeat("x", 100_000)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tasks/large", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "large", "name": "Large", "prompt": largePrompt,
			"priority": 1, "state": "queued",
		})
	})
	session := connect(t, mux)
	result := callTool(t, session, "redline_task_get", map[string]any{"id": "large"})
	if result.IsError {
		t.Fatalf("task error: %s", contentText(result))
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 20_000 {
		t.Fatalf("task result is unbounded: %d bytes", len(encoded))
	}
	if !strings.Contains(string(encoded), `"prompt_truncated":true`) {
		t.Fatalf("task result does not explain truncation: %s", encoded)
	}
}

func TestRunEventsBoundLargePayloads(t *testing.T) {
	largePayload := strings.Repeat("x", 100_000)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/runs/run-1/events", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": 1, "run_id": "run-1", "type": "harness.completed",
			"occurred_at": "2026-07-24T12:00:00Z",
			"payload":     map[string]any{"output": largePayload},
		}})
	})
	session := connect(t, mux)
	result := callTool(t, session, "redline_run_events", map[string]any{
		"run_id": "run-1", "limit": 1,
	})
	if result.IsError {
		t.Fatalf("events error: %s", contentText(result))
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 20_000 {
		t.Fatalf("event result is unbounded: %d bytes", len(encoded))
	}
	if !strings.Contains(string(encoded), `"payload_truncated":true`) {
		t.Fatalf("event result does not explain truncation: %s", encoded)
	}
}

func TestMutationToolsUseExistingAPIValidation(t *testing.T) {
	var created map[string]any
	var dispatched map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"agent-task","name":"Agent task","priority":50,"execution_profile_id":"codex-devx","type":"one_off","dispatch_tier":"behind","enabled":true,"state":"queued","min_interval":3600000000000}`)
	})
	mux.HandleFunc("POST /v1/scheduler/execute", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&dispatched); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"result":{"decision":"run","reason":"surplus"},"run":{"id":"run-1"}}`)
	})
	session := connect(t, mux)

	createdResult := callTool(t, session, "redline_task_create", map[string]any{
		"id":                   "agent-task",
		"name":                 "Agent task",
		"prompt":               "Reply with one word.",
		"priority":             50,
		"execution_profile_id": "codex-devx",
		"type":                 "one_off",
		"dispatch_tier":        "behind",
		"min_interval":         "1h",
	})
	if createdResult.IsError {
		t.Fatalf("create error: %s", contentText(createdResult))
	}
	if created["min_interval"] != "1h" || created["execution_profile_id"] != "codex-devx" {
		t.Fatalf("created body = %#v", created)
	}

	dispatchResult := callTool(t, session, "redline_scheduler_dispatch", map[string]any{
		"provider_account_id": "codex-main",
	})
	if dispatchResult.IsError {
		t.Fatalf("dispatch error: %v", dispatchResult.Content)
	}
	if dispatched["provider_account_id"] != "codex-main" {
		t.Fatalf("dispatch body = %#v", dispatched)
	}
}

func TestAPIErrorsBecomeVisibleToolErrors(t *testing.T) {
	session := connect(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"provider is paused"}`)
	}))
	result := callTool(t, session, "redline_scheduler_dispatch", map[string]any{
		"provider_account_id": "claude-main",
	})
	if !result.IsError {
		t.Fatal("expected an MCP tool error")
	}
	text := contentText(result)
	if !strings.Contains(text, "provider is paused") {
		t.Fatalf("error content = %s", text)
	}
}

func contentText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decodeStructured(t *testing.T, result *mcp.CallToolResult, output any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, output); err != nil {
		t.Fatal(err)
	}
}

func queryInt(t *testing.T, values url.Values, key string) int {
	t.Helper()
	var value int
	if _, err := fmt.Sscan(values.Get(key), &value); err != nil {
		t.Fatalf("%s = %q: %v", key, values.Get(key), err)
	}
	return value
}
