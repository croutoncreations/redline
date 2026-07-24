package hermes_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/hermes"
)

func TestLoadDesktopConnectionDiscoversRemoteGatewayWithoutCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connection.json")
	if err := os.WriteFile(path, []byte(`{
		"mode":"remote",
		"remote":{"url":"http://hermes.test:9119","authMode":"oauth"},
		"profiles":{}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	connection, err := hermes.LoadDesktopConnection(path)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Runtime != "hermes" || connection.Transport != "gateway" ||
		connection.CredentialSource != "hermes_desktop" || connection.URL != "http://hermes.test:9119" {
		t.Fatalf("connection = %#v", connection)
	}
}

func TestDiscoverReturnsProfilesProjectsAndOnlyAuthenticatedModels(t *testing.T) {
	server := fakeGateway(t)
	defer server.Close()
	client := hermes.Client{HTTPClient: authenticatedClient(server.URL)}
	discovery, err := client.Discover(t.Context(), domain.RuntimeConnection{
		ID: "test", Runtime: "hermes", Transport: "gateway", URL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Version != "0.17.0" || len(discovery.Profiles) != 1 ||
		len(discovery.ProfileOptions) != 1 || len(discovery.ProfileOptions[0].Projects) != 1 {
		t.Fatalf("discovery = %#v", discovery)
	}
	options := discovery.ProfileOptions[0]
	if projectByName(options, "redline").PrimaryPath != "/srv/redline" ||
		options.Provider != "openai-codex" || options.Model != "gpt-5.5" ||
		len(options.Providers[0].Models) != 2500 {
		t.Fatalf("options = %#v", options)
	}
}

func TestRunPersistsExternalIdentityBeforeWaitingAndCollectsUsage(t *testing.T) {
	server := fakeGateway(t)
	defer server.Close()
	client := hermes.Client{HTTPClient: authenticatedClient(server.URL)}
	var external domain.ExternalRun
	result, err := client.Run(context.Background(), hermes.RunRequest{
		RunID: "run-123", Prompt: "Reply with exactly OK.",
		Connection: domain.RuntimeConnection{ID: "hermes-pi", Runtime: "hermes", Transport: "gateway", URL: server.URL},
		Context:    domain.AgentContext{Profile: "default", WorkingDirectory: "/srv/redline"},
		Model:      "gpt-5.5", Provider: "openai-codex",
		OnExternalStarted: func(value domain.ExternalRun) error {
			external = value
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "OK" || result.SessionID != "stored-session" ||
		external.RuntimeConnectionID != "hermes-pi" || external.SessionID != "stored-session" {
		t.Fatalf("result=%#v external=%#v", result, external)
	}
	if result.Usage["input"] != float64(8) || result.Usage["output"] != float64(1) {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func authenticatedClient(baseURL string) hermes.HTTPClientFactory {
	return func(context.Context, domain.RuntimeConnection) (*http.Client, string, error) {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar}, baseURL, nil
	}
}

func fakeGateway(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"version": "0.17.0", "release_date": "2026.6.19",
			"auth_required": true, "auth_providers": []string{"basic"},
		})
	})
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"profiles": []map[string]any{{
			"name": "default", "path": "/home/test/.hermes", "is_default": true,
			"model": "gpt-5.5", "provider": "openai-codex",
		}}})
	})
	mux.HandleFunc("POST /api/auth/ws-ticket", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ticket": "one-shot-ticket"})
	})
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != "one-shot-ticket" {
			http.Error(w, "missing ticket", http.StatusUnauthorized)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		for {
			var request struct {
				ID     string         `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := wsjson.Read(r.Context(), socket, &request); err != nil {
				return
			}
			var result any
			switch request.Method {
			case "projects.list":
				result = map[string]any{"projects": []map[string]any{{
					"id": "project-1", "name": "redline", "primary_path": "/srv/redline",
				}}}
			case "model.options":
				models := make([]string, 2500)
				for index := range models {
					models[index] = "gpt-5.5-model-" + strings.Repeat("x", 8) + fmt.Sprint(index)
				}
				result = map[string]any{
					"model": "gpt-5.5", "provider": "openai-codex",
					"providers": []map[string]any{{
						"slug": "openai-codex", "name": "OpenAI Codex", "is_current": true,
						"authenticated": true, "models": models,
					}},
				}
			case "session.create":
				result = map[string]any{"session_id": "live-session", "stored_session_id": "stored-session"}
			case "prompt.submit":
				result = map[string]any{"status": "streaming"}
			case "session.usage":
				result = map[string]any{"calls": 1, "input": 8, "output": 1, "total": 9}
			case "session.status":
				result = map[string]any{"model": "gpt-5.5", "provider": "openai-codex"}
			default:
				result = map[string]any{}
			}
			writeRPC(t, r.Context(), socket, request.ID, result)
			if request.Method == "prompt.submit" {
				writeRPCEvent(t, r.Context(), socket, map[string]any{
					"type": "message.complete", "session_id": "live-session",
					"payload": map[string]any{"text": "OK"},
				})
			}
		}
	})
	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeRPC(t *testing.T, ctx context.Context, socket *websocket.Conn, id string, result any) {
	t.Helper()
	if err := wsjson.Write(ctx, socket, map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil &&
		!strings.Contains(err.Error(), "closed") {
		t.Error(err)
	}
}

func writeRPCEvent(t *testing.T, ctx context.Context, socket *websocket.Conn, params any) {
	t.Helper()
	if err := wsjson.Write(ctx, socket, map[string]any{"jsonrpc": "2.0", "method": "event", "params": params}); err != nil {
		t.Error(err)
	}
}

func projectByName(p hermes.ProfileOptions, name string) hermes.Project {
	for _, project := range p.Projects {
		if project.Name == name {
			return project
		}
	}
	return hermes.Project{}
}
