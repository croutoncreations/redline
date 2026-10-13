package cursorusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/nativeusage"
)

const defaultCursorAPI = "https://api2.cursor.sh"

// Cursor dashboard Connect RPC methods. These are the same endpoints Cursor's
// own dashboard and OpenUsage's Cursor provider use. Redline calls only the
// four that describe allowances; it never downloads usage exports or calls
// cookie-authenticated web endpoints.
const (
	usagePath   = "/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
	grokBotPath = "/aiserver.v1.DashboardService/GetSandUsageStatus"
	planPath    = "/aiserver.v1.DashboardService/GetPlanInfo"
	creditsPath = "/aiserver.v1.DashboardService/GetCreditGrantsBalance"
)

// NativeClient is Redline's built-in Cursor collector. It reads Cursor's
// existing desktop session through a read-only credential and never
// refreshes, rewrites or stores it.
type NativeClient struct {
	Credentials nativeusage.ReadOnlyCredentials
	HTTPClient  *http.Client
	// BaseURL overrides the Cursor API origin (tests only).
	BaseURL string
	Now     func() time.Time
}

// errSessionRejected is a fixed, credential-free explanation of a 401/403.
var errSessionRejected = errors.New("cursor rejected the saved sign-in; open Cursor to sign in again")

// Fetch collects one report. A failure of the primary usage call still
// returns any Grok Bot meter that succeeded, with the failure in Errors.
func (c NativeClient) Fetch(ctx context.Context) (Report, error) {
	if c.Credentials == nil {
		return Report{}, fmt.Errorf("cursor credentials are unavailable")
	}
	credential, err := c.Credentials.AccessWithoutRefresh(ctx, "cursor")
	if err != nil {
		return Report{}, err
	}
	if strings.TrimSpace(credential.AccessToken) == "" {
		return Report{}, nativeusage.ErrCursorSignedOut
	}
	token := credential.AccessToken
	report := newReport()
	report.Source = SourceNative

	usage, usageErr := c.call(ctx, usagePath, token)
	if errors.Is(usageErr, errSessionRejected) {
		// Every other call would be rejected the same way.
		return Report{}, usageErr
	}
	if usageErr == nil {
		if mapErr := mapPeriodUsage(usage, &report); mapErr != nil {
			usageErr = mapErr
		}
	}
	if usageErr != nil {
		report.Errors = append(report.Errors, "Cursor plan usage unavailable: "+usageErr.Error())
	}

	if grok, err := c.call(ctx, grokBotPath, token); err != nil {
		report.Errors = append(report.Errors, "Grok Bot usage unavailable: "+err.Error())
	} else if meter, ok := mapGrokBot(grok); ok {
		report.Meters = append(report.Meters, meter)
	}

	if usageErr == nil {
		if plan, err := c.call(ctx, planPath, token); err == nil {
			report.Plan = planName(plan)
		}
		if credits, err := c.call(ctx, creditsPath, token); err != nil {
			report.Errors = append(report.Errors, "Credit grants unavailable: "+err.Error())
		} else if balance, ok := mapCreditGrants(credits); ok {
			report.Balances = append(report.Balances, balance)
		}
	}

	if !report.HasData() {
		if usageErr != nil {
			return Report{}, usageErr
		}
		return Report{}, fmt.Errorf("cursor reported no usage meters")
	}
	now := c.now()
	report.FetchedAt = timePointer(now)
	report.finish()
	return report, nil
}

func (c NativeClient) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c NativeClient) call(ctx context.Context, path, token string) (map[string]any, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = defaultCursorAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("build request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("User-Agent", "Redline")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// Transport errors can quote the URL but never headers; still keep
		// the message fixed so nothing request-specific is shown.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("connection failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read failed")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errSessionRejected
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, fmt.Errorf("invalid response")
	}
	return payload, nil
}

// number reads a finite JSON number or numeric string; booleans and anything
// else are absent.
func number(value any) (float64, bool) {
	var parsed float64
	switch typed := value.(type) {
	case json.Number:
		f, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		parsed = f
	case float64:
		parsed = typed
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		parsed = f
	default:
		return 0, false
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

func boolean(value any) (bool, bool) {
	typed, ok := value.(bool)
	return typed, ok
}

func object(value any) map[string]any {
	typed, _ := value.(map[string]any)
	return typed
}

// mapPeriodUsage maps GetCurrentPeriodUsage. Only Cursor-reported
// percentages become monthly meters; no plan price is ever used as a
// denominator.
func mapPeriodUsage(usage map[string]any, report *Report) error {
	if enabled, ok := boolean(usage["enabled"]); ok && !enabled {
		return fmt.Errorf("no active Cursor subscription")
	}
	planUsage := object(usage["planUsage"])
	if planUsage == nil {
		return fmt.Errorf("plan usage missing from response")
	}
	reset, window := billingCycle(usage)
	monthly := func(key string, field string) {
		used, ok := number(planUsage[field])
		if !ok || used < 0 {
			return
		}
		definition, _ := resourceFor(key)
		report.Meters = append(report.Meters, Meter{Key: key, Label: definition.label, Group: GroupMonthly,
			Unit: "percent", Used: used, Limit: floatPointer(100), ResetsAt: reset, WindowSeconds: window})
	}
	monthly("autoUsage", "autoPercentUsed")
	monthly("apiUsage", "apiPercentUsed")
	monthly("totalUsage", "totalPercentUsed")

	if spend := object(usage["spendLimitUsage"]); spend != nil {
		limit := firstNumber(spend, "individualLimit", "pooledLimit")
		remaining := firstNumber(spend, "individualRemaining", "pooledRemaining")
		spent := onDemandSpentCents(spend, limit, remaining)
		definition, _ := resourceFor("onDemand")
		switch {
		case limit > 0:
			report.Meters = append(report.Meters, Meter{Key: "onDemand", Label: definition.label, Group: GroupOnDemand,
				Unit: "usd", Used: cents(spent), Limit: floatPointer(cents(limit))})
		case spent > 0:
			// Spend without a cap: shown as spend, never as a bar.
			report.Meters = append(report.Meters, Meter{Key: "onDemand", Label: definition.label, Group: GroupOnDemand,
				Unit: "usd", Used: cents(spent)})
		}
		// No limit and no spend means on-demand is off; nothing is shown so
		// it can never read as available quota.
	}
	return nil
}

func firstNumber(values map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := number(values[key]); ok {
			return value
		}
	}
	return 0
}

func onDemandSpentCents(spend map[string]any, limit, remaining float64) float64 {
	var reported []float64
	for _, key := range []string{"individualUsed", "pooledUsed", "totalSpend"} {
		if value, ok := number(spend[key]); ok {
			reported = append(reported, value)
		}
	}
	for _, value := range reported {
		if value > 0 {
			return value
		}
	}
	if inferred := limit - remaining; limit > 0 && inferred > 0 {
		return inferred
	}
	if len(reported) > 0 {
		return max(0, reported[0])
	}
	return 0
}

func cents(value float64) float64 { return math.Round(value) / 100 }

// billingCycle reads the cycle end as the reset. A missing end stays unknown.
func billingCycle(usage map[string]any) (*time.Time, *int64) {
	end, ok := number(usage["billingCycleEnd"])
	if !ok || end <= 0 {
		return nil, nil
	}
	reset := time.UnixMilli(int64(end)).UTC()
	if start, ok := number(usage["billingCycleStart"]); ok && end > start {
		return &reset, intPointer(int64((end - start) / 1000))
	}
	return &reset, nil
}

// mapGrokBot maps GetSandUsageStatus. Pooled enterprise allowances and
// accounts without an included Grok Bot allowance have no personal meter.
func mapGrokBot(usage map[string]any) (Meter, bool) {
	if pooled, _ := boolean(usage["usesPooledEnterpriseAllowance"]); pooled {
		return Meter{}, false
	}
	if hasLimit, ok := boolean(usage["hasNonZeroIncludedLimit"]); ok && !hasLimit {
		return Meter{}, false
	}
	if zero, _ := boolean(usage["includedLimitZero"]); zero {
		return Meter{}, false
	}
	used, ok := number(usage["usagePercent"])
	if !ok || used < 0 {
		return Meter{}, false
	}
	meter := Meter{Key: "grokBot", Label: "Grok Bot", Group: GroupWeekly, Unit: "percent", Used: used, Limit: floatPointer(100)}
	if text, _ := usage["nextResetTimestampUtc"].(string); strings.TrimSpace(text) != "" {
		if reset, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(text)); err == nil {
			meter.ResetsAt = timePointer(reset.UTC())
			if startText, _ := usage["currentPeriodStart"].(string); startText != "" {
				if start, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(startText)); err == nil && reset.After(start) {
					meter.WindowSeconds = intPointer(int64(reset.Sub(start) / time.Second))
				}
			}
		}
	}
	if meter.WindowSeconds == nil {
		meter.WindowSeconds = intPointer(int64((7 * 24 * time.Hour) / time.Second))
	}
	return meter, true
}

func planName(body map[string]any) string {
	info := object(body["planInfo"])
	name, _ := info["planName"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	words := strings.Fields(name)
	for i, word := range words {
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

// mapCreditGrants reports promotional credit grants. Prepaid account balance
// lives behind a cookie-authenticated web endpoint Redline does not call, so
// this is labelled as grants rather than as the account's whole credit.
func mapCreditGrants(body map[string]any) (Balance, bool) {
	has, ok := boolean(body["hasCreditGrants"])
	if !ok || !has {
		return Balance{}, false
	}
	total, okTotal := number(body["totalCents"])
	used, okUsed := number(body["usedCents"])
	if !okTotal || !okUsed || total <= 0 || used < 0 {
		return Balance{}, false
	}
	// GetCreditGrantsBalance reports no grant expiry, so none is shown;
	// an absent expiry never means "does not expire".
	return Balance{Key: "creditGrants", Label: "Credit grants", Unit: "usd", Available: cents(max(0, total-used))}, true
}
