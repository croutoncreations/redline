package cursorusage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/openusage"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func meterByKey(report Report, key string) (Meter, bool) {
	for _, meter := range report.Meters {
		if meter.Key == key {
			return meter, true
		}
	}
	return Meter{}, false
}

// The recorded OpenUsage 0.7.13 response keeps the three pools apart, with
// their own units, limits and resets, and the credit balance separate.
func TestFromLimitsKeepsCursorPoolsGrokBotAndCreditsDistinct(t *testing.T) {
	raw, err := os.ReadFile("testdata/openusage-cursor-limits.json")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := openusage.ParseLimits(raw)
	if err != nil {
		t.Fatal(err)
	}
	report, err := FromLimits(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if report.Source != SourceOpenUsage || report.Plan != "Pro" {
		t.Fatalf("source=%q plan=%q", report.Source, report.Plan)
	}
	if !report.FetchedAt.Equal(mustTime(t, "2026-10-09T05:45:34.883Z")) || !report.ExpiresAt.Equal(mustTime(t, "2026-10-09T05:50:34.883Z")) {
		t.Fatalf("fetched=%v expires=%v", report.FetchedAt, report.ExpiresAt)
	}
	var order []string
	for _, meter := range report.Meters {
		order = append(order, meter.Key)
	}
	if got := strings.Join(order, ","); got != "autoUsage,apiUsage,totalUsage,grokBot" {
		t.Fatalf("meter order = %s", got)
	}
	cursorModels, _ := meterByKey(report, "autoUsage")
	otherModels, _ := meterByKey(report, "apiUsage")
	grok, _ := meterByKey(report, "grokBot")
	if cursorModels.Label != "Cursor Models" || cursorModels.Group != GroupMonthly || cursorModels.Used != 0 {
		t.Fatalf("cursor models = %#v", cursorModels)
	}
	if otherModels.Label != "Other Models" || otherModels.Used != 100 || *otherModels.Limit != 100 ||
		!otherModels.ResetsAt.Equal(mustTime(t, "2026-10-13T04:16:45Z")) || *otherModels.WindowSeconds != 2592000 {
		t.Fatalf("other models = %#v", otherModels)
	}
	if grok.Label != "Grok Bot" || grok.Group != GroupWeekly || grok.Used != 58.008217 || grok.Unit != "percent" ||
		!grok.ResetsAt.Equal(mustTime(t, "2026-10-11T04:22:34.248Z")) || *grok.WindowSeconds != 604800 {
		t.Fatalf("grok bot = %#v", grok)
	}
	if _, ok := meterByKey(report, "onDemand"); ok {
		t.Fatal("absent on-demand must not be invented")
	}
	if len(report.Balances) != 1 || report.Balances[0].Label != "Credits" || report.Balances[0].Available != 22.23 ||
		report.Balances[0].Unit != "usd" || report.Balances[0].ExpiresAt != nil {
		t.Fatalf("balances = %#v", report.Balances)
	}
	if len(report.NotReported) != 0 {
		t.Fatalf("not reported = %v", report.NotReported)
	}
}

// OpenUsage's stale flag only describes response-generation time; Redline
// judges freshness against its own clock.
func TestReportStalenessUsesCurrentClockNotOpenUsageFlag(t *testing.T) {
	raw, _ := os.ReadFile("testdata/openusage-cursor-limits.json")
	envelope, _ := openusage.ParseLimits(raw)
	report, err := FromLimits(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if report.At(mustTime(t, "2026-10-09T05:50:00Z")).Stale {
		t.Fatal("report before expiresAt should be current")
	}
	if report.At(mustTime(t, "2026-10-09T05:51:00Z")).Stale {
		t.Fatal("report just past expiresAt is within the source's normal refresh and stays current")
	}
	if !report.At(mustTime(t, "2026-10-09T05:52:34.883Z")).Stale {
		t.Fatal("report once the source misses its refresh must be stale even though OpenUsage said stale:false")
	}
}

func TestFromLimitsPartialFailureKeepsWorkingMetersAndOmitsMissingOnes(t *testing.T) {
	envelope, err := openusage.ParseLimits([]byte(`{
		"schema":"openusage.limits.v1","generatedAt":"2026-10-09T05:50:21Z",
		"providers":{"cursor":{"displayName":"Cursor","fetchedAt":"2026-10-09T05:45:00Z","expiresAt":"2026-10-09T05:50:00Z","stale":false,
		  "resources":{
		    "grokBot":{"kind":"consumption","unit":"percent","used":12,"limit":100},
		    "onDemand":{"kind":"consumption","unit":"usd","used":4.5},
		    "mystery":{"kind":"consumption","unit":"percent","used":1,"limit":100}
		  }}},
		"errors":[{"providerId":"cursor","message":"Usage request failed (HTTP 503). Token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.sig leaked"},
		          {"providerId":"grok","message":"Grok CLI is not signed in"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	report, err := FromLimits(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 1 || strings.Contains(report.Errors[0], "eyJ") || !strings.Contains(report.Errors[0], "HTTP 503") {
		t.Fatalf("errors = %#v (only Cursor's, sanitized)", report.Errors)
	}
	grok, ok := meterByKey(report, "grokBot")
	if !ok || grok.ResetsAt != nil || grok.WindowSeconds != nil {
		t.Fatalf("grok bot without reset should stay unknown: %#v", grok)
	}
	onDemand, ok := meterByKey(report, "onDemand")
	if !ok || onDemand.Limit != nil || onDemand.Used != 4.5 || onDemand.Group != GroupOnDemand {
		t.Fatalf("on-demand without a limit must have no denominator: %#v", onDemand)
	}
	if _, ok := meterByKey(report, "mystery"); ok {
		t.Fatal("unknown resources are ignored")
	}
	if got := strings.Join(report.NotReported, ","); got != "Cursor Models,Other Models" {
		t.Fatalf("not reported = %q", got)
	}
}

func TestFromLimitsBalanceExpiriesAndZeroLimitDenominator(t *testing.T) {
	envelope, _ := openusage.ParseLimits([]byte(`{"schema":"openusage.limits.v1","providers":{"cursor":{
		"fetchedAt":"2026-10-09T05:45:00Z","expiresAt":"2026-10-09T05:50:00Z",
		"resources":{"credits":{"kind":"balance","unit":"usd","available":5,"expiresAt":["2026-11-01T00:00:00Z","bogus"]},
		             "totalUsage":{"kind":"consumption","unit":"usd","used":3,"limit":0}}}},"errors":[]}`))
	report, err := FromLimits(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Balances) != 1 || len(report.Balances[0].ExpiresAt) != 1 || !report.Balances[0].ExpiresAt[0].Equal(mustTime(t, "2026-11-01T00:00:00Z")) {
		t.Fatalf("balance = %#v", report.Balances)
	}
	total, _ := meterByKey(report, "totalUsage")
	if total.Limit != nil {
		t.Fatalf("a zero limit is not a denominator: %#v", total)
	}
}

func TestFromLimitsWithoutCursorCardIsAnError(t *testing.T) {
	envelope, _ := openusage.ParseLimits([]byte(`{"schema":"openusage.limits.v1","providers":{},"errors":[{"providerId":"cursor","message":"Not logged in"}]}`))
	if _, err := FromLimits(envelope); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("err = %v", err)
	}
	if _, err := openusage.ParseLimits([]byte(`{"schema":"openusage.limits.v2"}`)); err == nil {
		t.Fatal("unknown schema must be rejected")
	}
}

// Only the exact Cursor card is requested; Grok (the Grok Build CLI) is a
// different product and is never read for Grok Bot.
func TestOpenUsageClientRequestsExactCursorCardOnly(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		raw, _ := os.ReadFile("testdata/openusage-cursor-limits.json")
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	report, err := (OpenUsageClient{Client: openusage.Client{BaseURL: server.URL, HTTPClient: server.Client()}}).Fetch(context.Background())
	if err != nil || len(report.Meters) != 4 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if strings.Join(paths, ";") != "GET /v1/limits/cursor" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestOpenUsageClientReportsUnknownProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"provider_not_found"}`))
	}))
	defer server.Close()
	_, err := (OpenUsageClient{Client: openusage.Client{BaseURL: server.URL}}).Fetch(context.Background())
	if err != openusage.ErrProviderUnknown {
		t.Fatalf("err = %v", err)
	}
}
