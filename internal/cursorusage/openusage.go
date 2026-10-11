package cursorusage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/openusage"
)

// cursorCardID is OpenUsage's exact Cursor provider ID. The generic "grok"
// card is the Grok Build CLI, a different product from Cursor's Grok Bot, and
// is deliberately never read here.
const cursorCardID = "cursor"

// OpenUsageClient reads the Cursor card from OpenUsage's limits API. It
// reports what OpenUsage last fetched and never asks it to refresh.
type OpenUsageClient struct {
	Client openusage.Client
}

func (c OpenUsageClient) Fetch(ctx context.Context) (Report, error) {
	envelope, err := c.Client.FetchLimits(ctx, cursorCardID)
	if err != nil {
		return Report{}, err
	}
	return FromLimits(envelope)
}

// FromLimits converts an openusage.limits.v1 envelope. Values are kept as
// OpenUsage reported them: a missing limit stays missing, a missing reset
// stays unknown, and resources Redline does not recognise are ignored.
func FromLimits(envelope openusage.LimitsEnvelope) (Report, error) {
	report := newReport()
	for _, failure := range envelope.Errors {
		if strings.EqualFold(failure.ProviderID, cursorCardID) && strings.TrimSpace(failure.Message) != "" {
			report.Errors = append(report.Errors, failure.Message)
		}
	}
	provider, ok := envelope.Providers[cursorCardID]
	if !ok {
		if len(report.Errors) > 0 {
			return Report{}, fmt.Errorf("OpenUsage has no Cursor data: %s", sanitizeMessage(report.Errors[0]))
		}
		return Report{}, fmt.Errorf("OpenUsage has no Cursor data yet; open OpenUsage and enable Cursor")
	}
	fetchedAt, err := openusage.ParseTime(provider.FetchedAt)
	if err != nil {
		return Report{}, fmt.Errorf("OpenUsage Cursor data has no valid fetch time")
	}
	report.Source = SourceOpenUsage
	report.Plan = strings.TrimSpace(provider.Plan)
	report.FetchedAt = timePointer(fetchedAt.UTC())
	if expiresAt, err := openusage.ParseTime(provider.ExpiresAt); err == nil {
		report.ExpiresAt = timePointer(expiresAt.UTC())
	}
	for key, value := range provider.Resources {
		definition, known := resourceFor(key)
		if !known {
			continue
		}
		unit := strings.ToLower(strings.TrimSpace(value.Unit))
		if definition.balance || strings.EqualFold(value.Kind, "balance") {
			if value.Available == nil {
				continue
			}
			balance := Balance{Key: key, Label: definition.label, Unit: unit, Available: *value.Available}
			for _, text := range value.ExpiresAt {
				if expires, err := openusage.ParseTime(text); err == nil {
					balance.ExpiresAt = append(balance.ExpiresAt, expires.UTC())
				}
			}
			report.Balances = append(report.Balances, balance)
			continue
		}
		if value.Used == nil || *value.Used < 0 {
			continue
		}
		meter := Meter{Key: key, Label: definition.label, Group: definition.group, Unit: unit, Used: *value.Used}
		if value.Limit != nil && *value.Limit > 0 {
			meter.Limit = floatPointer(*value.Limit)
		}
		if reset, err := openusage.ParseTime(value.ResetsAt); err == nil {
			meter.ResetsAt = timePointer(reset.UTC())
		}
		if value.WindowSeconds != nil && *value.WindowSeconds > 0 {
			meter.WindowSeconds = intPointer(int64(*value.WindowSeconds))
		}
		report.Meters = append(report.Meters, meter)
	}
	report.finish()
	return report, nil
}

// fetcher is one Cursor collector.
type fetcher interface {
	Fetch(context.Context) (Report, error)
}

// fetchTimeout bounds one collection so a slow provider never holds a
// dashboard request.
const fetchTimeout = 20 * time.Second
