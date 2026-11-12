package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFiresalesContract(t *testing.T) {
	sale := `{"manual":[{"account":"claude/main","started_at":"2026-07-20T12:00:00Z","deadline":"2026-07-20T14:00:00Z","started_by":"phone"}],"eligible":[{"account":"claude/main","label":"Main","provider":"claude","weekly_resets_at":null,"reason_unavailable":null}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing credential")
		}
		switch r.Method {
		case "GET":
			if r.URL.Path != "/v1/firesales" {
				t.Error(r.URL.Path)
			}
			fmt.Fprint(w, sale)
		case "POST":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["account"] != "claude/main" || body["until"] != "2h" {
				t.Error(body)
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{"account":"claude/main"}`)
		case "DELETE":
			if r.URL.EscapedPath() != "/v1/firesales/claude%2Fmain" {
				t.Error(r.URL.EscapedPath())
			}
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "token")
	raw, err := client.ListFiresales()
	if err != nil || raw != sale {
		t.Fatalf("%s %v", raw, err)
	}
	if err := client.StartFiresale("claude/main", "2h"); err != nil {
		t.Fatal(err)
	}
	if err := client.StopFiresale("claude/main"); err != nil {
		t.Fatal(err)
	}
}

func TestFiresalesNotFoundOnOlderDesktop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	_, err := NewClient(server.URL, "token").ListFiresales()
	if !IsNotFound(err) {
		t.Fatalf("expected typed 404, got %v", err)
	}
}

func TestFiresalesErrorsAndRelay(t *testing.T) {
	for _, status := range []int{404, 403, 500} {
		client := NewClient("", "token")
		client.SetRelayFallback(RelayFallbackWithStatus(func(method, path, body string) (int, string, error) { return status, `{"error":"unavailable"}`, nil }))
		_, err := client.ListFiresales()
		if err == nil || IsNotFound(err) != (status == 404) {
			t.Fatalf("%d: %v", status, err)
		}
		if IsNotFound(fmt.Errorf("wrapped: %w", err)) != (status == 404) {
			t.Fatal("wrapped error lost")
		}
		if err := client.StopFiresale("main"); err == nil {
			t.Fatal("delete error lost")
		}
	}
	if IsNotFound(errors.New("404")) {
		t.Fatal("classified untyped error")
	}
}

func TestSendTestNotification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.Method != "POST" || r.URL.Path != "/v1/notifications/test" || body["event"] != "reset.announced" || body["push"] != true {
			t.Errorf("%s %s %v", r.Method, r.URL.Path, body)
		}
		w.WriteHeader(403)
	}))
	defer server.Close()
	if err := NewClient(server.URL, "token").SendTestNotification(); !IsForbidden(err) {
		t.Fatal(err)
	}
}
