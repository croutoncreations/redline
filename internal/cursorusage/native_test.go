package cursorusage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/nativeusage"
)

const fakeCursorToken = "fake-cursor-token-for-tests"

type fakeCursorCredentials struct {
	token string
	err   error
	calls int
}

func (f *fakeCursorCredentials) AccessWithoutRefresh(_ context.Context, provider string) (nativeusage.Credential, error) {
	f.calls++
	if provider != "cursor" {
		return nativeusage.Credential{}, errors.New("wrong provider")
	}
	return nativeusage.Credential{AccessToken: f.token}, f.err
}

type fakeCursorAPI struct {
	mu        sync.Mutex
	responses map[string]struct {
		status int
		body   string
	}
	calls []string
}

func newFakeCursorAPI(t *testing.T, responses map[string]string) (*fakeCursorAPI, *httptest.Server) {
	t.Helper()
	api := &fakeCursorAPI{responses: map[string]struct {
		status int
		body   string
	}{}}
	for path, body := range responses {
		status := http.StatusOK
		if strings.HasPrefix(body, "HTTP ") {
			switch body {
			case "HTTP 401":
				status = http.StatusUnauthorized
			case "HTTP 503":
				status = http.StatusServiceUnavailable
			}
			body = `{}`
		}
		api.responses[path] = struct {
			status int
			body   string
		}{status, body}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fakeCursorToken ||
			r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Errorf("unexpected request %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		api.mu.Lock()
		api.calls = append(api.calls, r.URL.Path)
		response, ok := api.responses[r.URL.Path]
		api.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(response.status)
		_, _ = w.Write([]byte(response.body))
	}))
	t.Cleanup(server.Close)
	return api, server
}

var fixedNow = time.Date(2026, 10, 9, 6, 0, 0, 0, time.UTC)

const proUsage = `{"enabled":true,"billingCycleStart":"1781438541000","billingCycleEnd":"1784030541000",
	"planUsage":{"limit":2000,"totalSpend":96,"totalPercentUsed":4.8,"autoPercentUsed":0,"apiPercentUsed":100},
	"spendLimitUsage":{"limitType":"user"}}`

func TestNativeClientMapsPoolsGrokBotAndCreditGrants(t *testing.T) {
	api, server := newFakeCursorAPI(t, map[string]string{
		usagePath:   proUsage,
		grokBotPath: `{"usagePercent":58.008217,"currentPeriodStart":"2026-10-04T04:22:34.248Z","nextResetTimestampUtc":"2026-10-11T04:22:34.248Z","hasNonZeroIncludedLimit":true}`,
		planPath:    `{"planInfo":{"planName":"pro"}}`,
		creditsPath: `{"hasCreditGrants":true,"totalCents":"2500","usedCents":"277"}`,
	})
	creds := &fakeCursorCredentials{token: fakeCursorToken}
	report, err := (NativeClient{Credentials: creds, HTTPClient: server.Client(), BaseURL: server.URL, Now: func() time.Time { return fixedNow }}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.calls != 1 || report.Source != SourceNative || report.Plan != "Pro" || !report.FetchedAt.Equal(fixedNow) {
		t.Fatalf("report = %#v", report)
	}
	if len(api.calls) != 4 {
		t.Fatalf("calls = %v", api.calls)
	}
	cycleEnd := time.UnixMilli(1784030541000).UTC()
	auto, _ := meterByKey(report, "autoUsage")
	other, _ := meterByKey(report, "apiUsage")
	if auto.Used != 0 || other.Used != 100 || !auto.ResetsAt.Equal(cycleEnd) || *other.WindowSeconds != 2592000 || *other.Limit != 100 {
		t.Fatalf("auto=%#v other=%#v", auto, other)
	}
	grok, _ := meterByKey(report, "grokBot")
	if grok.Used != 58.008217 || grok.Group != GroupWeekly || !grok.ResetsAt.Equal(mustTime(t, "2026-10-11T04:22:34.248Z")) || *grok.WindowSeconds != 604800 {
		t.Fatalf("grok = %#v", grok)
	}
	if _, ok := meterByKey(report, "onDemand"); ok {
		t.Fatal("on-demand with no limit and no spend is disabled and must not appear as quota")
	}
	if len(report.Balances) != 1 || report.Balances[0].Label != "Credit grants" || report.Balances[0].Available != 22.23 || report.Balances[0].ExpiresAt != nil {
		t.Fatalf("balances = %#v", report.Balances)
	}
	for _, meter := range report.Meters {
		if meter.Unit == "percent" && (meter.Limit == nil || *meter.Limit != 100) {
			t.Fatalf("percent meters use Cursor's own 100%% scale, never a price: %#v", meter)
		}
	}
}

func TestNativeClientOnDemandBoundedAndSpendOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		spend     string
		wantUsed  float64
		wantLimit *float64
	}{
		"bounded":    {`{"individualLimit":5000,"individualRemaining":1000}`, 40, floatPointer(50)},
		"spend only": {`{"individualUsed":16474,"limitType":"user","totalSpend":16474}`, 164.74, nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, server := newFakeCursorAPI(t, map[string]string{
				usagePath: `{"planUsage":{"totalPercentUsed":10},"spendLimitUsage":` + tc.spend + `}`,
			})
			report, err := (NativeClient{Credentials: &fakeCursorCredentials{token: fakeCursorToken}, BaseURL: server.URL, Now: func() time.Time { return fixedNow }}).Fetch(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			meter, ok := meterByKey(report, "onDemand")
			if !ok || meter.Used != tc.wantUsed || (tc.wantLimit == nil) != (meter.Limit == nil) ||
				(tc.wantLimit != nil && *meter.Limit != *tc.wantLimit) {
				t.Fatalf("on-demand = %#v", meter)
			}
			total, _ := meterByKey(report, "totalUsage")
			if total.ResetsAt != nil {
				t.Fatalf("a missing billing cycle leaves the reset unknown: %#v", total)
			}
		})
	}
}

func TestNativeClientGrokBotIneligibleOrPooledIsOmitted(t *testing.T) {
	for _, body := range []string{
		`{"usagePercent":42,"usesPooledEnterpriseAllowance":true}`,
		`{"usagePercent":0,"hasNonZeroIncludedLimit":false}`,
		`{"usagePercent":0,"includedLimitZero":true}`,
		`{"usagePercent":true}`,
		`{"usagePercent":-1}`,
	} {
		_, server := newFakeCursorAPI(t, map[string]string{usagePath: proUsage, grokBotPath: body})
		report, err := (NativeClient{Credentials: &fakeCursorCredentials{token: fakeCursorToken}, BaseURL: server.URL}).Fetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := meterByKey(report, "grokBot"); ok {
			t.Fatalf("%s: Grok Bot must be omitted, not shown as zero", body)
		}
		if !strings.Contains(strings.Join(report.NotReported, ","), "Grok Bot") {
			t.Fatalf("%s: not reported = %v", body, report.NotReported)
		}
	}
}

// Grok Bot keeps working when Cursor's plan usage call fails.
func TestNativeClientPartialFailureKeepsGrokBot(t *testing.T) {
	_, server := newFakeCursorAPI(t, map[string]string{
		usagePath:   "HTTP 503",
		grokBotPath: `{"usagePercent":100,"nextResetTimestampUtc":"2026-10-11T04:22:34Z"}`,
	})
	report, err := (NativeClient{Credentials: &fakeCursorCredentials{token: fakeCursorToken}, BaseURL: server.URL}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	grok, ok := meterByKey(report, "grokBot")
	if !ok || grok.Used != 100 {
		t.Fatalf("grok = %#v", grok)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "HTTP 503") {
		t.Fatalf("errors = %v", report.Errors)
	}
	if got := strings.Join(report.NotReported, ","); got != "Cursor Models,Other Models" {
		t.Fatalf("not reported = %q", got)
	}
}

func TestNativeClientRejectedSessionStopsWithoutLeakingToken(t *testing.T) {
	api, server := newFakeCursorAPI(t, map[string]string{usagePath: "HTTP 401"})
	_, err := (NativeClient{Credentials: &fakeCursorCredentials{token: fakeCursorToken}, BaseURL: server.URL}).Fetch(context.Background())
	if err == nil || strings.Contains(err.Error(), fakeCursorToken) || !strings.Contains(err.Error(), "sign in") {
		t.Fatalf("err = %v", err)
	}
	if len(api.calls) != 1 {
		t.Fatalf("a rejected session must not be retried or refreshed: %v", api.calls)
	}
}

func TestNativeClientSignedOutMakesNoRequests(t *testing.T) {
	api, server := newFakeCursorAPI(t, map[string]string{})
	_, err := (NativeClient{Credentials: &fakeCursorCredentials{err: nativeusage.ErrCursorSignedOut}, BaseURL: server.URL}).Fetch(context.Background())
	if !errors.Is(err, nativeusage.ErrCursorSignedOut) || len(api.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, api.calls)
	}
}
