package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

func TestNotificationTestPushCommand(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Push bool `json:"push"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Push {
					t.Error("missing push flag")
				}
				fmt.Fprintf(w, `{"id":1,"status":"delivered","payload":{"title":"Test","message":"Sample"},"push":{"configured":%t,"woken":2}}`, configured)
			}))
			defer relay.Close()
			var out, errOut bytes.Buffer
			code := cli.RunWithInput([]string{"--api", relay.URL, "notification", "test", "--push"}, strings.NewReader(""), &out, &errOut, time.Now)
			want := "push not configured"
			if configured {
				want = "2 devices woken"
			}
			if code != 0 || !strings.Contains(out.String(), want) {
				t.Fatalf("code=%d out=%s err=%s", code, &out, &errOut)
			}
		})
	}
}
