package core_test

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	core "github.com/jfox/redline/mobile/core"
)

// Fallback belongs at the one place every request passes through.
//
// The first attempt put it in the Kotlin sources, which meant wiring it into
// each of them separately and reproducing each method's request shape. That is
// wrong twice over: FetchRuns makes two calls, so one relayed path cannot
// stand in for it, and anything not wired up silently keeps failing. Here it
// is one change for all ~65 endpoints.
func TestClientFallsBackToTheRelayWhenDirectIsUnreachable(t *testing.T) {
	relayed := 0
	// A relay stand-in that answers with the payload the desktop would.
	fallback := func(method, path, body string) (string, error) {
		relayed++
		return `{"ok":true}`, nil
	}

	// Port 1 refuses immediately, so the direct leg fails as it would with the
	// tailnet down.
	client := core.NewClient("http://127.0.0.1:1", "token")
	client.SetRelayFallback(core.RelayFallbackWithStatus(
		func(method, path, body string) (int, string, error) {
			answer, err := fallback(method, path, body)
			return 200, answer, err
		}))

	got, err := client.FetchHealth()
	if err != nil {
		t.Fatalf("with a relay available the request must succeed: %v", err)
	}
	if relayed != 1 {
		t.Errorf("relay attempts = %d, want 1", relayed)
	}
	if got == "" {
		t.Error("expected the relayed body to be returned")
	}
}

func TestClientWithoutARelayReportsTheDirectFailure(t *testing.T) {
	client := core.NewClient("http://127.0.0.1:1", "token")

	_, err := client.FetchHealth()
	if err == nil {
		t.Fatal("expected the direct failure")
	}
	if !strings.Contains(err.Error(), "reach redline") {
		t.Errorf("error = %v, want the direct reachability failure", err)
	}
}

// A relayed response must carry the desktop's own status.
//
// The tunnel already transports it -- TunnelResponse.Status exists precisely
// because "202 and 200 mean different things" -- but the phone's relay client
// dropped it and the fallback synthesised a flat 200. Every relayed answer
// therefore looked like success: a dispatch refused with 409 came back as
// "not started, not refused", which is the one outcome that cannot happen, and
// a 401 would never have prompted a re-pair.
//
// Dispatch is the sharpest case because it has three distinct outcomes, but
// the same loss applied to every endpoint.
func TestRelayedResponsePreservesTheDesktopStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"accepted", 202, `{}`, `"started":true`},
		{"already running", 409, `{"error":"already in flight"}`, `"refused":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := core.NewClient("http://127.0.0.1:1", "token")
			client.SetRelayFallback(core.RelayFallbackWithStatus(
				func(method, path, body string) (int, string, error) {
					return tc.status, tc.body, nil
				}))

			got, err := client.DispatchTask("task-1")
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("status %d produced %s, want it to contain %s", tc.status, got, tc.want)
			}
		})
	}
}

// An unauthorized relayed response has to be recognisable as one, or the app
// reports "unreachable" and the user never learns to re-pair.
func TestRelayedUnauthorizedIsStillUnauthorized(t *testing.T) {
	client := core.NewClient("http://127.0.0.1:1", "token")
	client.SetRelayFallback(core.RelayFallbackWithStatus(
		func(method, path, body string) (int, string, error) {
			return 401, `{"error":"unauthorized"}`, nil
		}))

	_, err := client.FetchHealth()
	if err == nil {
		t.Fatal("expected an error for a relayed 401")
	}
	if !core.IsUnauthorized(err) {
		t.Errorf("relayed 401 must be recognised as unauthorized, got: %v", err)
	}
}

// Two relayed requests in flight must not cross their statuses.
//
// The status was returned through a second call -- Do then StatusOf -- which
// is two operations on shared state. A probe crossed 1 in 200 under load and
// -race reported a genuine data race. On the phone one shared CoreClientHolder
// serves three view models, each refreshing on Dispatchers.IO, so this is
// reachable rather than theoretical: a dispatch could report the status of a
// usage refresh that happened to finish between the two calls.
//
// The status now comes back with the body, so there is nothing to interleave.
func TestConcurrentRelayedRequestsKeepTheirOwnStatus(t *testing.T) {
	client := core.NewClient("http://127.0.0.1:1", "token")
	client.SetRelayFallback(core.RelayFallbackWithStatus(
		func(method, path, body string) (int, string, error) {
			if strings.Contains(path, "accepted") {
				return 202, `{}`, nil
			}
			return 409, `{"error":"already running"}`, nil
		}))

	var crossed int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			task := "accepted-task"
			want := `"started":true`
			if n%2 == 1 {
				task, want = "conflict-task", `"refused":true`
			}
			got, err := client.DispatchTask(task)
			if err != nil || !strings.Contains(got, want) {
				atomic.AddInt64(&crossed, 1)
			}
		}(i)
	}
	wg.Wait()

	if crossed != 0 {
		t.Errorf("%d of 200 concurrent relayed dispatches got the wrong answer", crossed)
	}
}
