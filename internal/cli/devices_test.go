package cli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/apiauth"
	"github.com/croutoncreations/redline/internal/cli"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/store"
)

func TestDevicesCLI(t *testing.T) {
	for _, args := range [][]string{{"devices", "list"}, {"devices", "revoke", "device/one"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			configPath, token := writePairingConfig(t, nil)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Errorf("missing owner credential")
				}
				if args[1] == "list" {
					if r.Method != "GET" || r.URL.Path != "/v1/devices" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					fmt.Fprint(w, `[{"id":"device/one","name":"Phone","kind":"mobile"}]`)
				} else {
					if r.Method != "DELETE" || r.URL.EscapedPath() != "/v1/devices/device%2Fone" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					w.WriteHeader(204)
				}
			}))
			defer s.Close()
			var stdout, stderr bytes.Buffer
			exit := cli.Run(append([]string{"--config", configPath, "--api", s.URL}, args...), &stdout, &stderr, time.Now)
			if exit != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", exit, &stdout, &stderr)
			}
			if args[1] == "list" && !strings.Contains(stdout.String(), "Phone") {
				t.Fatalf("stdout=%s", &stdout)
			}
		})
	}
}

func TestTokenRotateRevokesDeviceSessions(t *testing.T) {
	configPath, owner := writePairingConfig(t, nil)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "redline.db")
	if err := os.WriteFile(configPath, []byte(strings.Replace(string(data), "database: redline.db", "database: "+databasePath, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIToken = owner
	db, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := api.NewServer(cfg, db, time.Now)
	bootstrap := httptest.NewRecorder()
	s.ServeHTTP(bootstrap, httptest.NewRequest("GET", "http://localhost/?access_token="+owner, nil))
	cookies := bootstrap.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("bootstrap=%d %s", bootstrap.Code, bootstrap.Body)
	}
	call := func() int {
		r := httptest.NewRequest("GET", "http://localhost/v1/tasks", nil)
		r.Header.Set("Authorization", "Bearer "+cookies[0].Value)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if code := call(); code != 200 {
		t.Fatalf("before rotation=%d", code)
	}
	var stdout, stderr bytes.Buffer
	// Rotate from another working directory: the CLI must not need to find
	// the service's database for revocation to take effect.
	t.Chdir(t.TempDir())
	if exit := cli.Run([]string{"--config", configPath, "token", "rotate", "--yes"}, &stdout, &stderr, time.Now); exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, &stderr)
	}
	current, err := apiauth.ReadToken(configPath)
	if err != nil || current == owner {
		t.Fatalf("token not rotated: %v", err)
	}
	// Restart the service with the rotated token, as the output instructs.
	cfg.APIToken = current
	s = api.NewServer(cfg, db, time.Now)
	if code := call(); code != 401 {
		t.Fatalf("after rotation and restart=%d", code)
	}
}
