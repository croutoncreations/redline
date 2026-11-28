package apicredits

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/croutoncreations/redline/internal/openusage"
)

// Claude Console prepaid credits.
//
// A Claude subscription's monthly API credit lands in a linked Claude
// Console organization as a promo tranche with program_id
// subscription_api_credit. The Console's own endpoint
// /api/organizations/{uuid}/prepaid/credits lists it beside purchased
// tranches and other promotional programs, and accepts only the Console
// browser session (the sessionKey cookie).

// ConsoleSubscriptionProgram is the promo program of a Claude subscription's
// API credit: the only Console credit treated as schedulable capacity.
const ConsoleSubscriptionProgram = "subscription_api_credit"

// maxConsoleExponent bounds the minor-unit exponent a response may use.
const maxConsoleExponent = 9

var (
	ErrNoConsoleAPIOrganization    = errors.New("no Claude Console organization has API access")
	ErrSeveralConsoleOrganizations = errors.New("several Console organizations have API access; set organization_id")
)

var consoleOrganizationPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidConsoleOrganizationID reports whether id is a Console organization
// UUID, the only data-derived part of a Console request URL.
func ValidConsoleOrganizationID(id string) bool {
	return consoleOrganizationPattern.MatchString(id)
}

// ConsoleOrganization is the part of a Console organization Redline needs.
type ConsoleOrganization struct {
	UUID         string   `json:"uuid"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

// maxConsoleOrganizationName bounds a displayed organization name, in runes.
const maxConsoleOrganizationName = 60

// DisplayName is the organization's name, safe to print: a personal
// organization is often named after its owner's email address, so anything
// shaped like one is redacted, and control and formatting characters are
// dropped so a name cannot rewrite the terminal.
func (o ConsoleOrganization) DisplayName() string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, o.Name)
	name = strings.TrimSpace(openusage.RedactEmails(name))
	if runes := []rune(name); len(runes) > maxConsoleOrganizationName {
		name = string(runes[:maxConsoleOrganizationName-1]) + "…"
	}
	return name
}

// SeveralConsoleOrganizationsError is ErrSeveralConsoleOrganizations with the
// organizations that have API access, so a person can pick one.
type SeveralConsoleOrganizationsError struct {
	Organizations []ConsoleOrganization
}

func (e *SeveralConsoleOrganizationsError) Error() string {
	return ErrSeveralConsoleOrganizations.Error()
}

func (e *SeveralConsoleOrganizationsError) Unwrap() error { return ErrSeveralConsoleOrganizations }

func hasAPIAccess(org ConsoleOrganization) bool {
	for _, capability := range org.Capabilities {
		if capability == "api" {
			return true
		}
	}
	return false
}

// ParseConsoleOrganizations decodes GET /api/organizations.
func ParseConsoleOrganizations(body []byte) ([]ConsoleOrganization, error) {
	var orgs []ConsoleOrganization
	if err := json.Unmarshal(body, &orgs); err != nil || orgs == nil {
		return nil, fmt.Errorf("invalid Console organizations response")
	}
	return orgs, nil
}

// SelectConsoleOrganization picks the one organization with API access.
// Several are an error, not a guess: one person can belong to several
// same-named organizations, and reading the wrong one would schedule
// against someone else's credit.
func SelectConsoleOrganization(orgs []ConsoleOrganization) (string, error) {
	var found []string
	for _, org := range orgs {
		if hasAPIAccess(org) {
			found = append(found, org.UUID)
		}
	}
	switch {
	case len(found) == 0:
		return "", ErrNoConsoleAPIOrganization
	case len(found) > 1:
		return "", ErrSeveralConsoleOrganizations
	case !ValidConsoleOrganizationID(found[0]):
		return "", fmt.Errorf("invalid Console organizations response")
	}
	return strings.ToLower(found[0]), nil
}

type consoleAmount struct {
	AmountMinor *int64 `json:"amount_minor"`
	Exponent    *int   `json:"exponent"`
}

type consoleBalance struct {
	Money   json.RawMessage `json:"money"`
	Credits *consoleAmount  `json:"credits"`
}

type consoleTranchePayload struct {
	RemainingMinorUnits *int64          `json:"remaining_amount_minor_units"`
	GrantedMinorUnits   *int64          `json:"granted_amount_minor_units"`
	Currency            string          `json:"currency"`
	ExpiresAt           *string         `json:"expires_at"`
	GrantedAt           *string         `json:"granted_at"`
	Remaining           *consoleBalance `json:"remaining"`
	Granted             *consoleBalance `json:"granted"`
	ProgramID           *string         `json:"program_id"`
}

type consoleCreditsPayload struct {
	Currency      string                   `json:"currency"`
	Tranches      *[]consoleTranchePayload `json:"tranches"`
	PromoTranches *[]consoleTranchePayload `json:"promo_tranches"`
}

// errConsoleCredits is the one error a malformed or unexpected credits
// response gives: the account reads as unknown, and no body is echoed.
var errConsoleCredits = errors.New("unexpected Claude Console credits response")

// ParseConsoleCredits decodes a Console prepaid/credits response observed at
// now into the same Report the OpenAI parser produces. Promo is the
// in-effect subscription_api_credit tranches; purchased tranches and other
// promo programs are PaidAvailable, shown but never capacity. The top-level
// amount and balance sum every pot and are never used. Anything not
// understood (another currency, a missing program, an unreadable date or
// amount) fails the whole read rather than being guessed at.
func ParseConsoleCredits(body []byte, now time.Time) (Report, error) {
	var payload consoleCreditsPayload
	if err := json.Unmarshal(body, &payload); err != nil || payload.Tranches == nil || payload.PromoTranches == nil {
		return Report{}, errConsoleCredits
	}
	if !strings.EqualFold(payload.Currency, "USD") {
		return Report{}, errConsoleCredits
	}
	report := Report{Grants: make([]Grant, 0, len(*payload.Tranches)+len(*payload.PromoTranches))}
	for _, raw := range *payload.Tranches {
		grant, err := consoleGrant(raw, KindPurchase, now)
		if err != nil {
			return Report{}, err
		}
		report.Grants = append(report.Grants, grant)
		if grant.State == StateAvailable {
			report.PaidAvailable += grant.Remaining
		}
	}
	for _, raw := range *payload.PromoTranches {
		// program_id is required on promo tranches only, on purpose: it is
		// what tells subscription credit from other promo programs. A
		// purchased tranche is purchase credit whatever its program says.
		if raw.ProgramID == nil || strings.TrimSpace(*raw.ProgramID) == "" {
			return Report{}, errConsoleCredits
		}
		kind := KindOther
		if *raw.ProgramID == ConsoleSubscriptionProgram {
			kind = KindPromo
		}
		grant, err := consoleGrant(raw, kind, now)
		if err != nil {
			return Report{}, err
		}
		report.Grants = append(report.Grants, grant)
		if grant.State != StateAvailable {
			continue
		}
		if kind != KindPromo {
			report.PaidAvailable += grant.Remaining
			continue
		}
		report.Promo.Granted += grant.Amount
		report.Promo.Remaining += grant.Remaining
		// NextExpiring is everything that lapses at ExpiresAt, so tranches
		// sharing the soonest expiry are summed.
		switch {
		case report.Promo.ExpiresAt == nil || grant.ExpiresAt.Before(*report.Promo.ExpiresAt):
			report.Promo.ExpiresAt = grant.ExpiresAt
			report.Promo.EffectiveAt = grant.EffectiveAt
			report.Promo.NextExpiring = grant.Remaining
		case grant.ExpiresAt.Equal(*report.Promo.ExpiresAt):
			report.Promo.NextExpiring += grant.Remaining
		}
	}
	report.Promo.NextExpiring = roundDollars(report.Promo.NextExpiring)
	report.Promo.Granted = roundDollars(report.Promo.Granted)
	report.Promo.Remaining = roundDollars(report.Promo.Remaining)
	report.PaidAvailable = roundDollars(report.PaidAvailable)
	report.TotalAvailable = roundDollars(report.Promo.Remaining + report.PaidAvailable)
	return report, nil
}

// consoleGrant converts one tranche. A subscription tranche is available
// only while it has started, has an expiry still ahead and has money left.
// Purchased and other credit may have no expiry; it is available while it
// has started (if a start is given), has not expired and has money left.
func consoleGrant(raw consoleTranchePayload, kind string, now time.Time) (Grant, error) {
	if !strings.EqualFold(raw.Currency, "USD") {
		return Grant{}, errConsoleCredits
	}
	remaining, err := consoleDollars(raw.Remaining, raw.RemainingMinorUnits)
	if err != nil {
		return Grant{}, err
	}
	granted, err := consoleDollars(raw.Granted, raw.GrantedMinorUnits)
	if err != nil {
		return Grant{}, err
	}
	// Subscription credit is scheduling capacity, so more left than was
	// granted is a balance Redline does not understand.
	if kind == KindPromo && remaining > granted {
		return Grant{}, errConsoleCredits
	}
	expires, err := consoleTime(raw.ExpiresAt)
	if err != nil {
		return Grant{}, err
	}
	start, err := consoleTime(raw.GrantedAt)
	if err != nil {
		return Grant{}, err
	}
	grant := Grant{Kind: kind, State: StateInactive, Amount: granted, Remaining: remaining,
		Used: roundDollars(max(granted-remaining, 0)), EffectiveAt: start, ExpiresAt: expires}
	available := remaining > 0 &&
		(expires == nil || now.Before(*expires)) &&
		(start == nil || !now.Before(*start))
	if kind == KindPromo {
		available = available && expires != nil && start != nil
	}
	if available {
		grant.State = StateAvailable
	}
	return grant, nil
}

// consoleDollars reads a balance object's credits, falling back to the
// legacy minor-units field (cents) only when the object is missing. A
// money value beside credits must agree with it.
func consoleDollars(balance *consoleBalance, minorUnits *int64) (float64, error) {
	if balance == nil {
		if minorUnits == nil || *minorUnits < 0 {
			return 0, errConsoleCredits
		}
		return roundDollars(float64(*minorUnits) / 100), nil
	}
	if balance.Credits == nil {
		return 0, errConsoleCredits
	}
	amount, err := consoleAmountDollars(*balance.Credits)
	if err != nil {
		return 0, err
	}
	if money := strings.TrimSpace(string(balance.Money)); money != "" && money != "null" {
		// A money object that names a currency must name USD: the same
		// digits in another currency are not the same amount.
		var other struct {
			consoleAmount
			Currency *string `json:"currency"`
		}
		if err := json.Unmarshal(balance.Money, &other); err != nil {
			return 0, errConsoleCredits
		}
		if other.Currency != nil && !strings.EqualFold(*other.Currency, "USD") {
			return 0, errConsoleCredits
		}
		otherAmount, err := consoleAmountDollars(other.consoleAmount)
		if err != nil || otherAmount != amount {
			return 0, errConsoleCredits
		}
	}
	return amount, nil
}

func consoleAmountDollars(amount consoleAmount) (float64, error) {
	if amount.AmountMinor == nil || amount.Exponent == nil || *amount.AmountMinor < 0 ||
		*amount.Exponent < 0 || *amount.Exponent > maxConsoleExponent {
		return 0, errConsoleCredits
	}
	return roundDollars(float64(*amount.AmountMinor) / math.Pow10(*amount.Exponent)), nil
}

// consoleTime parses an ISO timestamp; null is "not given", anything else
// unreadable is an error.
func consoleTime(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil, errConsoleCredits
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

// roundDollars drops float noise from sums of minor-unit amounts.
func roundDollars(value float64) float64 {
	return math.Round(value*1e9) / 1e9
}
