package mcpserver_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestTaskToolsExposeAndForwardFiresaleInterval(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{"id":"job","name":"Job","type":"recurring","dispatch_tier":"firesale","firesale_min_interval":600000000000}`)
	}
	mux.HandleFunc("POST /v1/tasks", respond)
	mux.HandleFunc("PATCH /v1/tasks/job", respond)
	session := connect(t, mux)
	for _, name := range []string{"redline_task_create", "redline_task_update"} {
		args := map[string]any{"id": "job", "name": "Job", "priority": 50, "execution_profile_id": "p", "type": "recurring", "dispatch_tier": "firesale", "firesale_min_interval": "10m"}
		result := callTool(t, session, name, args)
		if result.IsError {
			t.Fatalf("%s: %s", name, contentText(result))
		}
		if body["firesale_min_interval"] != "10m" || body["dispatch_tier"] != "firesale" {
			t.Fatalf("body=%+v", body)
		}
		var output struct {
			Data struct {
				FiresaleMinInterval string `json:"firesale_min_interval"`
			} `json:"data"`
		}
		decodeStructured(t, result, &output)
		if output.Data.FiresaleMinInterval != "10m0s" {
			t.Fatalf("output=%+v", output)
		}
	}
	result := callTool(t, session, "redline_task_update", map[string]any{"id": "job", "firesale_min_interval": ""})
	if result.IsError || body["firesale_min_interval"] != "" {
		t.Fatalf("clear: %s body=%+v", contentText(result), body)
	}
}
