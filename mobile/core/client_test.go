package core_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

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

// A malformed relay answer is a bug, not a 200.
//
// splitRelayAnswer degraded an unparseable prefix to success. FormatRelayAnswer
// is the only producer and always emits a valid decimal, so a malformed prefix
// can only mean the encoding broke -- and reporting that as a successful
// response would hand the UI a body it would try to render as data. This
// session has been bitten three times by a failure that looked like success;
// this one is cheap to make loud.
func TestMalformedRelayAnswerIsAnErrorNotASuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
	}{
		{"no status prefix at all", `{"providers":[]}`},
		{"non-numeric prefix", `oops {"providers":[]}`},
		{"status out of range", `999 {"providers":[]}`},
		{"empty", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := core.NewClient("http://127.0.0.1:1", "token")
			client.SetRelayFallback(core.RelayFallbackRaw(
				func(method, path, body string) (string, error) { return tc.answer, nil }))

			_, err := client.FetchHealth()
			if err == nil {
				t.Fatal("a malformed relay answer must be an error")
			}
			if core.IsUnauthorized(err) {
				t.Error("a malformed answer is not an auth failure")
			}
		})
	}
}

// A well-formed answer still works, including a body that itself starts with
// something status-shaped: only the first space is consumed.
func TestRelayAnswerWithAStatusShapedBody(t *testing.T) {
	client := core.NewClient("http://127.0.0.1:1", "token")
	client.SetRelayFallback(core.RelayFallbackWithStatus(
		func(method, path, body string) (int, string, error) {
			return 200, `404 not the status`, nil
		}))

	// The body is not JSON, so decoding fails -- but it must fail as a decode
	// problem on a 200, proving the prefix was read as 200 and not as 404.
	_, err := client.FetchHealth()
	if err != nil && strings.Contains(err.Error(), "404") {
		t.Errorf("the body was parsed as the status: %v", err)
	}
}

// The route must belong to the response, not to the client.
//
// A client-wide flag read back through a second call is the same defect as the
// status race fixed earlier, one layer up: three view models share one client,
// so a Runs refresh going direct could clear the flag a Usage refresh had just
// set, and the pill would claim the tailnet over data that crossed the paid
// relay. Runs and Queue never read the flag but every one of their requests
// writes it.
//
// Carrying it in the payload leaves nothing to interleave.
func TestUsageViewCarriesItsOwnRoute(t *testing.T) {
	relayed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"providers":[],"health":{"scheduler_enabled":false}}`))
	}))
	defer relayed.Close()

	// Direct is dead, so this fetch is served by the relay.
	client := core.NewClient("http://127.0.0.1:1", "token")
	client.SetRelayFallback(core.RelayFallbackWithStatus(
		func(method, path, body string) (int, string, error) {
			return 200, `{"providers":[],"health":{"scheduler_enabled":false}}`, nil
		}))

	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var view struct {
		Relayed bool `json:"relayed"`
	}
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !view.Relayed {
		t.Error("a relayed fetch must say so in its own payload")
	}

	// A direct fetch on a live server must report direct in its payload, even
	// if some other request relayed in between.
	directClient := core.NewClient(relayed.URL, "token")
	directClient.SetRelayFallback(core.RelayFallbackWithStatus(
		func(method, path, body string) (int, string, error) {
			return 200, `{}`, nil
		}))
	rawDirect, err := directClient.FetchUsage()
	if err != nil {
		t.Fatalf("direct fetch: %v", err)
	}
	// A fresh struct: 'relayed' is omitempty, so decoding a direct payload
	// into the previous value would leave the earlier true in place and the
	// test would pass while reading nothing.
	var directView struct {
		Relayed bool `json:"relayed"`
	}
	if err := json.Unmarshal([]byte(rawDirect), &directView); err != nil {
		t.Fatalf("decode direct: %v", err)
	}
	if directView.Relayed {
		t.Error("a direct fetch must not claim the relay")
	}
}
