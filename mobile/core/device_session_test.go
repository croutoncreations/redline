package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExchangeDeviceSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/devices/session" || r.Header.Get("Authorization") != "Bearer master" {
			http.Error(w, "unexpected", 400)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "Pixel 9" || body["kind"] != "mobile" {
			http.Error(w, "bad body", 400)
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"token":"scoped","device":{"id":"d1","name":"Pixel 9","kind":"mobile"}}`))
	}))
	defer server.Close()
	token, err := NewClient(server.URL, "master").ExchangeDeviceSession("Pixel 9")
	if err != nil || token != "scoped" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	// A desktop that already treats this credential as a device (or predates the
	// endpoint) refuses; the caller keeps its current credential.
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
	}))
	defer refusing.Close()
	if token, err := NewClient(refusing.URL, "scoped").ExchangeDeviceSession("Pixel 9"); err == nil || token != "" {
		t.Fatalf("refused exchange returned token=%q err=%v", token, err)
	}
}
