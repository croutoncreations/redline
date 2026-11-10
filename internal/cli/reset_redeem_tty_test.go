package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

// Wait until the subprocess prompts before writing; macOS script otherwise
// injects an EOF when a finite input reader closes before the child is ready.
type redemptionPromptOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	ready  chan struct{}
	once   sync.Once
}

func (o *redemptionPromptOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.buffer.Write(p)
	if strings.Contains(o.buffer.String(), "Continue? [y/N]") {
		o.once.Do(func() { close(o.ready) })
	}
	return n, err
}

func (o *redemptionPromptOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

func TestResetRedeemTTYConfirmationHelper(t *testing.T) {
	apiURL := os.Getenv("REDLINE_TEST_REDEEM_TTY_API")
	if apiURL == "" {
		return
	}
	os.Exit(cli.RunWithInput([]string{"--api", apiURL, "reset", "redeem", "--provider", "main"}, os.Stdin, os.Stdout, os.Stderr, time.Now))
}

func TestResetRedeemTTYConfirmationBindsIDAndDefaultsNo(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("script PTY integration requires macOS or Linux")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script PTY utility unavailable")
	}
	for _, answer := range []string{"y\n", "n\n", "\n"} {
		t.Run(fmt.Sprintf("answer=%q", answer), func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprint(w, `{"provider":"claude","banked_reset_details":[{"id":"confirmed-grant","resets_left":1,"claimable":true,"usable_now":true,"expires_at":"2099-01-01T00:00:00Z","clears":["five_hour","weekly"]}]}`)
					return
				}
				posts.Add(1)
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if len(body) != 2 || body["dry_run"] != false || body["expected_reset_id"] != "confirmed-grant" {
					t.Error(body)
				}
				fmt.Fprint(w, `{"outcome":"redeemed","verified":true}`)
			}))
			defer server.Close()
			args := []string{"-q", "/dev/null", os.Args[0], "-test.run=^TestResetRedeemTTYConfirmationHelper$"}
			if runtime.GOOS == "linux" {
				args = []string{"-q", "-e", "-c", `exec "$REDLINE_TEST_REDEEM_BINARY" -test.run=^TestResetRedeemTTYConfirmationHelper$`, "/dev/null"}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, script, args...)
			cmd.Env = append(os.Environ(), "REDLINE_TEST_REDEEM_TTY_API="+server.URL, "REDLINE_TEST_REDEEM_BINARY="+os.Args[0])
			output := &redemptionPromptOutput{ready: make(chan struct{})}
			cmd.Stdout = output
			cmd.Stderr = output
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-output.ready:
				_, _ = input.Write([]byte(answer))
			case <-ctx.Done():
			}
			err = cmd.Wait()
			for _, want := range []string{"Provider: main", "Reset: confirmed-grant", "Expires: 2099-01-01T00:00:00Z", "Clears: five_hour, weekly", "This spends a one-time reset. Continue? [y/N]"} {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("missing %q: err=%v output=%s", want, err, output)
				}
			}
			if answer == "y\n" {
				if posts.Load() != 1 || err != nil {
					t.Fatalf("posts=%d err=%v output=%s", posts.Load(), err, output)
				}
			} else if posts.Load() != 0 || !strings.Contains(output.String(), "Cancelled") {
				t.Fatalf("posts=%d output=%s", posts.Load(), output)
			}
		})
	}
}
