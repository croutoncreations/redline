package openusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
)

// ErrCardNotFound means OpenUsage knows the provider family but has no card
// with the requested ID: the account is not signed in on this Mac (or not in
// OpenUsage), or the card ID is mistyped. It is never answered by falling back
// to another card, since that would show one account's numbers under another.
var ErrCardNotFound = errors.New("OpenUsage has no card with this ID")

// FetchCard reads one account card from GET /v1/limits/{card} and converts it
// to a snapshot. Upstream OpenUsage gives every signed-in account and
// organization its own card ("claude", "claude@<hash8>") and matches the
// route's ID against the exact card ID or the provider family. Only the exact
// card is ever read, so a family request that returns several cards cannot
// put another account's numbers here.
//
// The card's display name is deliberately not read or stored: OpenUsage
// builds it from the account email.
func (c Client) FetchCard(ctx context.Context, provider, card string) (decision.UsageSnapshot, []byte, error) {
	card = strings.ToLower(strings.TrimSpace(card))
	envelope, err := c.FetchLimits(ctx, card)
	if errors.Is(err, ErrProviderUnknown) {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("%w: %q", ErrCardNotFound, card)
	}
	if err != nil {
		return decision.UsageSnapshot{}, nil, err
	}
	snapshot, err := SnapshotFromCard(envelope, provider, card)
	if err != nil {
		return decision.UsageSnapshot{}, nil, err
	}
	// The raw payload kept for debugging is the selected card's resources
	// only, without the display name, so no email reaches the database.
	raw, _ := json.Marshal(redactedCard(envelope, card))
	return snapshot, raw, nil
}

type redactedLimitsCard struct {
	Schema    string                    `json:"schema"`
	Card      string                    `json:"card"`
	Plan      string                    `json:"plan,omitempty"`
	FetchedAt string                    `json:"fetchedAt"`
	Stale     bool                      `json:"stale"`
	Resources map[string]LimitsResource `json:"resources"`
}

func redactedCard(envelope LimitsEnvelope, card string) redactedLimitsCard {
	selected := envelope.Providers[card]
	return redactedLimitsCard{Schema: envelope.Schema, Card: card, Plan: selected.Plan, FetchedAt: selected.FetchedAt,
		Stale: selected.Stale, Resources: selected.Resources}
}

// limitsResource maps OpenUsage limits resource keys to Redline allowances.
// Unlisted keys (extraUsage, sonnet, spend) are not allowances Redline
// schedules against and are ignored rather than guessed at.
var limitsResource = map[string]struct{ key, label, scope, role string }{
	"session": {"session", "Session", "account", "short"},
	"weekly":  {"weekly", "Weekly", "account", "weekly"},
	"fable":   {"model:fable:weekly", "Fable", "model", "weekly"},
}

// SnapshotFromCard converts one card of an openusage.limits.v1 envelope.
// Values are taken as reported: a missing number is missing, a missing reset
// is never invented, and a card OpenUsage marks stale stays marked.
func SnapshotFromCard(envelope LimitsEnvelope, provider, card string) (decision.UsageSnapshot, error) {
	family := strings.ToLower(strings.TrimSpace(provider))
	card = strings.ToLower(strings.TrimSpace(card))
	var cardErrors []string
	for _, failure := range envelope.Errors {
		if strings.EqualFold(failure.ProviderID, card) && strings.TrimSpace(failure.Message) != "" {
			// OpenUsage words card errors with the account's email; it
			// must not reach the database, the dashboard or the phone.
			cardErrors = append(cardErrors, RedactEmails(strings.TrimSpace(failure.Message)))
		}
	}
	selected, ok := envelope.Providers[card]
	if !ok {
		if len(cardErrors) > 0 {
			return decision.UsageSnapshot{}, fmt.Errorf("OpenUsage card %q has no data: %s", card, cardErrors[0])
		}
		return decision.UsageSnapshot{}, fmt.Errorf("%w: %q", ErrCardNotFound, card)
	}
	observedAt, err := parseTime("fetchedAt", selected.FetchedAt)
	if err != nil {
		return decision.UsageSnapshot{}, fmt.Errorf("OpenUsage card %q: %w", card, err)
	}
	snapshot := decision.UsageSnapshot{
		Provider: family, ObservedAt: observedAt.UTC(), Source: "openusage", Confidence: "high",
		Plan: strings.TrimSpace(selected.Plan), SourceStale: selected.Stale,
	}
	if selected.Stale || len(cardErrors) > 0 {
		snapshot.Confidence = "medium"
	}
	keys := make([]string, 0, len(selected.Resources))
	for key := range selected.Resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var weeklyReset time.Time
	if weekly, ok := selected.Resources["weekly"]; ok && strings.TrimSpace(weekly.ResetsAt) != "" {
		if parsed, parseErr := parseTime("resetsAt", weekly.ResetsAt); parseErr == nil {
			weeklyReset = parsed.UTC()
		}
	}
	weeklyFound := false
	for _, name := range keys {
		resource := selected.Resources[name]
		if name == "rateLimitResets" {
			resets, ok := resetCredits(resource, observedAt)
			if ok {
				snapshot.ApplyBankedResets(&resets)
			}
			continue
		}
		definition, known := limitsResource[name]
		if !known {
			continue
		}
		remaining, ok := remainingFromResource(resource)
		if !ok {
			if definition.role == "short" && definition.scope == "account" {
				snapshot.ShortWindowUnavailable = true
				snapshot.Confidence = "medium"
			}
			continue
		}
		if strings.TrimSpace(resource.ResetsAt) == "" {
			switch {
			case definition.role == "short" && definition.scope == "account" && resource.Used != nil && *resource.Used == 0:
				// Untouched: the clock starts on first use. Known state, all
				// available, and no reset time to invent.
				snapshot.ShortWindowNotStarted = true
			case definition.role == "short":
				snapshot.ShortWindowUnavailable = definition.scope == "account"
				snapshot.Confidence = "medium"
			case definition.scope == "model" && !weeklyReset.IsZero():
				snapshot.Allowances = append(snapshot.Allowances, decision.AllowanceWindow{
					Key: definition.key, SourceLabel: definition.label, Scope: definition.scope, Role: definition.role,
					Remaining: remaining, ResetsAt: weeklyReset, PeriodDurationSeconds: windowSeconds(resource, definition.role),
					ResetInferred: true,
				})
				snapshot.Confidence = "medium"
			}
			continue
		}
		reset, err := parseTime("resetsAt", resource.ResetsAt)
		if err != nil {
			return decision.UsageSnapshot{}, fmt.Errorf("OpenUsage card %q resource %q: %w", card, name, err)
		}
		reset = reset.UTC()
		snapshot.Allowances = append(snapshot.Allowances, decision.AllowanceWindow{
			Key: definition.key, SourceLabel: definition.label, Scope: definition.scope, Role: definition.role,
			Remaining: remaining, ResetsAt: reset, PeriodDurationSeconds: windowSeconds(resource, definition.role),
		})
		switch definition.key {
		case "session":
			snapshot.Short = &decision.UsageWindow{Remaining: remaining, ResetsAt: reset}
		case "weekly":
			snapshot.Weekly = decision.UsageWindow{Remaining: remaining, ResetsAt: reset}
			weeklyFound = true
		}
	}
	if snapshot.ShortWindowNotStarted && (snapshot.Short != nil || snapshot.ShortWindowUnavailable) {
		snapshot.ShortWindowNotStarted = false
	}
	if !weeklyFound {
		if _, reported := selected.Resources["weekly"]; reported || len(cardErrors) > 0 {
			// OpenUsage listed a weekly window it could not fill, or the card
			// errored: that is a failure to read, not an account without one.
			if len(cardErrors) > 0 {
				return decision.UsageSnapshot{}, fmt.Errorf("OpenUsage card %q has no weekly limit: %s", card, cardErrors[0])
			}
			return decision.UsageSnapshot{}, fmt.Errorf("OpenUsage card %q has no weekly limit; the login may lack usage access", card)
		}
		// The card simply has no account-wide weekly limit (Team seats
		// report the 5-hour window, model windows and reset credits only).
		// Keep what it does report; weekly is "not reported", never a
		// number. Model windows stay model windows, with their own resets.
		snapshot.WeeklyUnavailable = true
		if snapshot.Short == nil && !snapshot.ShortWindowNotStarted && !snapshot.ShortWindowUnavailable &&
			len(snapshot.Allowances) == 0 && snapshot.BankedResets == nil {
			return decision.UsageSnapshot{}, fmt.Errorf("OpenUsage card %q reports no usage windows", card)
		}
	}
	if err := snapshot.Validate(); err != nil {
		return decision.UsageSnapshot{}, fmt.Errorf("normalize OpenUsage card %q: %w", card, err)
	}
	return snapshot, nil
}

// remainingFromResource reads a bounded consumption resource as a remaining
// fraction. Only percent-like bounded values are accepted.
func remainingFromResource(resource LimitsResource) (float64, bool) {
	if resource.Used == nil || resource.Limit == nil || *resource.Limit <= 0 {
		return 0, false
	}
	used, limit := *resource.Used, *resource.Limit
	if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 {
		return 0, false
	}
	if used > limit {
		used = limit
	}
	return 1 - used/limit, true
}

func windowSeconds(resource LimitsResource, role string) int64 {
	if resource.WindowSeconds != nil && *resource.WindowSeconds > 0 {
		return int64(*resource.WindowSeconds)
	}
	if role == "short" {
		return int64(decision.ShortWindowDuration / time.Second)
	}
	return decision.WeekSeconds
}

// resetCredits reads OpenUsage's rateLimitResets balance: how many one-off
// limit resets the account holds, and each one's deadline when it has one.
// A missing count is "not reported" (nil), never zero; deadlines that had
// already passed when OpenUsage fetched the card are dropped.
func resetCredits(resource LimitsResource, observedAt time.Time) (decision.BankedResets, bool) {
	value := resource.Available
	if value == nil {
		value = resource.Remaining
	}
	if value == nil || *value < 0 || math.IsNaN(*value) || *value != math.Trunc(*value) {
		return decision.BankedResets{}, false
	}
	resets := decision.BankedResets{Available: int(*value)}
	for _, text := range resource.ExpiresAt {
		expires, err := parseTime("expiresAt", strings.TrimSpace(text))
		if err != nil || !expires.After(observedAt) {
			continue
		}
		resets.Expiries = append(resets.Expiries, expires.UTC())
	}
	sort.Slice(resets.Expiries, func(i, j int) bool { return resets.Expiries[i].Before(resets.Expiries[j]) })
	if len(resets.Expiries) > 0 {
		soonest := resets.Expiries[0]
		resets.NextExpiresAt = &soonest
	}
	return resets, true
}

// CardSummary describes one OpenUsage card for account setup, without the
// display name (which contains the account email).
type CardSummary struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	Plan      string    `json:"plan,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
	Stale     bool      `json:"stale"`
	HasWeekly bool      `json:"has_weekly"`
	Error     string    `json:"error,omitempty"`
}

// ListCards asks OpenUsage which cards it has for a provider family, for the
// account setup flow. It reads what OpenUsage last fetched and changes
// nothing. Card errors are passed through only after removing anything that
// looks like an email address.
func (c Client) ListCards(ctx context.Context, provider string) ([]CardSummary, error) {
	family := strings.ToLower(strings.TrimSpace(provider))
	envelope, err := c.FetchLimits(ctx, family)
	if errors.Is(err, ErrProviderUnknown) {
		return []CardSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	cards := make([]CardSummary, 0, len(envelope.Providers))
	for id, card := range envelope.Providers {
		lower := strings.ToLower(id)
		if lower != family && !strings.HasPrefix(lower, family+"@") {
			continue
		}
		summary := CardSummary{ID: lower, Provider: family, Plan: strings.TrimSpace(card.Plan), Stale: card.Stale}
		if fetched, parseErr := parseTime("fetchedAt", card.FetchedAt); parseErr == nil {
			summary.FetchedAt = fetched.UTC()
		}
		_, summary.HasWeekly = card.Resources["weekly"]
		cards = append(cards, summary)
	}
	for _, failure := range envelope.Errors {
		for index := range cards {
			if strings.EqualFold(failure.ProviderID, cards[index].ID) {
				cards[index].Error = RedactEmails(failure.Message)
			}
		}
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	return cards, nil
}

// emailLike matches a local part and a host around an "@", with or without
// a dot in the host (user@localhost counts).
var emailLike = regexp.MustCompile(`[^\s<>()\[\]"',;:/]+@[^\s<>()\[\]"',;:/]+`)

// RedactEmails replaces anything shaped like an email address. OpenUsage
// card ids ("claude@ab12cd34") are left alone: they are opaque hashes, not
// identities, and the setup flow needs them.
func RedactEmails(text string) string {
	return emailLike.ReplaceAllStringFunc(text, func(match string) string {
		if cardIDPattern.MatchString(match) {
			return match
		}
		return "[account]"
	})
}

// No dot allowed: "claude@example.com" is an address, not a card. Upstream
// card suffixes are hex hashes, optionally salted with "-n".
var cardIDPattern = regexp.MustCompile(`^(claude|codex|cursor)@[a-z0-9][a-z0-9_-]{0,63}$`)
