package resetsignal

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ResetBeaconURL = "https://resetbeacon.com/api/forecast?live=1"
	WillResetURL   = "https://willreset.com/api/notify"
	CodexResetURL  = "https://codex-reset.com/api/forecast"
)

type Source struct {
	Name        string
	Endpoint    string
	Attribution string
	Normalize   func([]byte, http.Header, time.Time) (Signal, error)
}

func DefaultSources() []Source {
	return []Source{
		{Name: "resetbeacon", Endpoint: ResetBeaconURL, Attribution: "Data: resetbeacon.com", Normalize: normalizeResetBeacon},
		{Name: "willreset", Endpoint: WillResetURL, Attribution: "Data: willreset.com", Normalize: normalizeWillReset},
		{Name: "codex-reset", Endpoint: CodexResetURL, Attribution: "Data: codex-reset.com", Normalize: normalizeCodexReset},
	}
}

// FetchAll performs one bounded refresh of each source. It does not poll,
// persist source state, send notifications, or call the scheduler.
func FetchAll(ctx context.Context, fetcher Fetcher, sources []Source) []SourceResult {
	if len(sources) == 0 {
		sources = DefaultSources()
	}
	results := make([]SourceResult, len(sources))
	var group sync.WaitGroup
	group.Add(len(sources))
	for index := range sources {
		go func(index int) {
			defer group.Done()
			source := sources[index]
			document, fetchErr := fetcher.get(ctx, source.Name, source.Endpoint)
			if fetchErr != nil {
				results[index] = SourceResult{Error: fetchErr, Metadata: fetchErr.Metadata}
				return
			}
			now := time.Now().UTC()
			if fetcher.Now != nil {
				now = fetcher.Now().UTC()
			}
			signal, err := source.Normalize(document.Body, document.Header, now)
			if err != nil {
				results[index] = SourceResult{Error: sourceError(source.Name, "decode", err.Error(), document.Metadata), Metadata: document.Metadata}
				return
			}
			signal.Feed = source.Name
			signal.FeedURL = source.Endpoint
			signal.Attribution = source.Attribution
			results[index] = SourceResult{Signal: &signal, Metadata: document.Metadata}
		}(index)
	}
	group.Wait()
	return results
}

type resetBeaconPayload struct {
	CalculatedAt     string  `json:"calculatedAt"`
	ValidUntil       string  `json:"validUntil"`
	StaleSince       *string `json:"staleSince"`
	PublicationState string  `json:"publicationState"`
	Answer           struct {
		State    string `json:"state"`
		Window   string `json:"window"`
		Deadline string `json:"deadline"`
		Posts    []struct {
			PostID       string `json:"postId"`
			SourceURL    string `json:"sourceUrl"`
			Quote        string `json:"quote"`
			PublishedAt  string `json:"publishedAt"`
			AuthorHandle string `json:"authorHandle"`
		} `json:"posts"`
	} `json:"answer"`
}

func normalizeResetBeacon(body []byte, _ http.Header, now time.Time) (Signal, error) {
	var payload resetBeaconPayload
	if err := decodeOne(body, &payload); err != nil {
		return Signal{}, err
	}
	checked, err := requiredTime(payload.CalculatedAt, "calculatedAt")
	if err != nil {
		return Signal{}, err
	}
	expires, err := requiredTime(payload.ValidUntil, "validUntil")
	if err != nil {
		return Signal{}, err
	}
	state := firstNonEmpty(payload.Answer.State, payload.PublicationState)
	kind := kindFromState(state)
	if strings.EqualFold(strings.TrimSpace(state), "forecast") {
		// Reset Beacon's compatibility state "forecast" means no new
		// source-backed announcement, and its numerical fields stay empty: it
		// is no current evidence, not a prediction.
		kind = KindUnknown
	}
	uncheckedState := resetBeaconUncheckedState(payload.Answer.State, payload.PublicationState)
	signal := Signal{
		Kind:               kind,
		CheckedAt:          &checked,
		ExpiresAt:          &expires,
		Freshness:          freshness(now, &checked, &expires, nil, payload.StaleSince != nil || uncheckedState != ""),
		Window:             Window{Text: payload.Answer.Window, Certainty: WindowMissing},
		GlobalAnnouncement: true,
	}
	if uncheckedState != "" {
		// Reset Beacon documents checking, stale, and unavailable as a
		// missing or outdated current check, whatever validUntil says, so the
		// record and any post it retains are not current evidence.
		signal.Warnings = append(signal.Warnings, "state "+strconv.Quote(uncheckedState)+" means the current check is missing or outdated; the record is stale")
	}
	signal.Withdrawn = strings.EqualFold(payload.PublicationState, "withdrawn") || strings.EqualFold(payload.PublicationState, "superseded")
	signal.Corrected = strings.EqualFold(payload.PublicationState, "corrected")
	forecast := strings.EqualFold(strings.TrimSpace(state), "forecast")
	if forecast && len(payload.Answer.Posts) > 0 {
		// "forecast" means no new source-backed announcement, so any listed
		// post is history and must not name a current announcement.
		signal.Warnings = append(signal.Warnings, "forecast state lists posts; they were treated as history, not a current announcement")
	}
	if len(payload.Answer.Posts) > 0 && !forecast {
		post := payload.Answer.Posts[0]
		signal.AnnouncementID = post.PostID
		signal.ClaimedOriginalURL, signal.OriginalAccount = canonicalOriginal(post.SourceURL)
		signal.Quote = post.Quote
		published, parseErr := requiredTime(post.PublishedAt, "answer.posts[0].publishedAt")
		if parseErr != nil {
			return Signal{}, parseErr
		}
		signal.PublishedAt = &published
		if signal.OriginalAccount == "" {
			signal.OriginalAccount = post.AuthorHandle
		}
	}
	if payload.Answer.Deadline != "" {
		deadline, parseErr := requiredTime(payload.Answer.Deadline, "answer.deadline")
		if parseErr != nil {
			return Signal{}, parseErr
		}
		// The deadline is Reset Beacon's reading of the source phrase, not a
		// verified source window, so it stays a mirror interpretation and can
		// never satisfy hasExplicitPrimaryWindow.
		signal.Window.Certainty = WindowMirrorInterpretation
		signal.Window.Basis = "Reset Beacon supplied an interpreted deadline; not reported as a primary-source deadline because the source phrase leaves the PST timezone ambiguous"
		signal.Warnings = append(signal.Warnings, "tracker-interpreted deadline is not reported as a primary-source deadline")
		// The deadline only bounds a forward-looking claim: once it has
		// passed, a promise the tracker keeps serving as fresh is history.
		// It must not expire facts that outlive it: a completion, or a
		// withdrawal/correction that has to keep retracting the promise.
		if expiresWithDeadline(signal) {
			signal.Window.End = &deadline
			signal.Window.Basis = "Reset Beacon's interpreted deadline; attributed to the tracker and used only to expire the forward-looking record, not as a primary-source deadline because the source phrase leaves the PST timezone ambiguous"
		}
	}
	validateSignal(&signal, now)
	return signal, nil
}

// resetBeaconUncheckedState returns the first of Reset Beacon's answer.state
// or publicationState that is checking, stale, or unavailable, or "".
func resetBeaconUncheckedState(states ...string) string {
	for _, state := range states {
		switch normalized := strings.ToLower(strings.TrimSpace(state)); normalized {
		case "checking", "stale", "unavailable":
			return normalized
		}
	}
	return ""
}

// expiresWithDeadline reports whether a tracker deadline or window end bounds
// the record's claim: true only for forward-looking states (including a
// tracker prediction) that have not been retracted. A record with no window
// end, such as codex-reset's model-only snapshot, is never expired by it.
func expiresWithDeadline(signal Signal) bool {
	if signal.Withdrawn || signal.Corrected {
		return false
	}
	switch signal.Kind {
	case KindPromised, KindScheduled, KindIntent, KindPrediction:
		return true
	default:
		return false
	}
}

type willResetPayload struct {
	Answer        string   `json:"answer"`
	FetchedAt     string   `json:"fetched_at"`
	LastResetAt   string   `json:"last_reset_at"`
	LastResetID   string   `json:"last_reset_id"`
	LastResetText string   `json:"last_reset_text"`
	LastResetURL  string   `json:"last_reset_url"`
	PromiseActive bool     `json:"promise_active"`
	PromiseQuote  string   `json:"promise_quote"`
	Status        string   `json:"status"`
	Probability24 *float64 `json:"prob_24"`
	Probability48 *float64 `json:"prob_48"`
}

func normalizeWillReset(body []byte, _ http.Header, now time.Time) (Signal, error) {
	var payload willResetPayload
	if err := decodeOne(body, &payload); err != nil {
		return Signal{}, err
	}
	checked, err := requiredTime(payload.FetchedAt, "fetched_at")
	if err != nil {
		return Signal{}, err
	}
	kind := kindFromState(payload.Answer)
	if payload.PromiseActive {
		kind = KindPromised
	}
	signal := Signal{
		Kind:                kind,
		AnnouncementID:      payload.LastResetID,
		Quote:               firstNonEmpty(payload.PromiseQuote, payload.LastResetText),
		CheckedAt:           &checked,
		Freshness:           freshness(now, &checked, nil, nil, false),
		Window:              Window{Certainty: WindowMissing, Basis: "compact notify feed provides no documented deadline"},
		GlobalAnnouncement:  true,
		Prediction24Percent: payload.Probability24,
		Prediction48Percent: payload.Probability48,
	}
	signal.Withdrawn = strings.EqualFold(payload.Status, "withdrawn") || strings.EqualFold(payload.Status, "superseded")
	signal.Corrected = strings.EqualFold(payload.Status, "corrected")
	signal.ClaimedOriginalURL, signal.OriginalAccount = canonicalOriginal(payload.LastResetURL)
	validateSignal(&signal, now)
	return signal, nil
}

type codexResetPayload struct {
	Mode          string `json:"mode"`
	UpdatedAt     string `json:"updated_at"`
	LastResetAt   string `json:"last_reset_at"`
	BankedState   string `json:"banked_state"`
	Probabilities struct {
		Rounded24 *float64 `json:"rounded_24h"`
		Rounded48 *float64 `json:"rounded_48h"`
	} `json:"probabilities"`
	OfficialSignal *struct {
		TweetID    string `json:"tweet_id"`
		Summary    string `json:"summary"`
		At         string `json:"at"`
		URL        string `json:"url"`
		SignalType string `json:"signal_type"`
		Window     *struct {
			Start string `json:"start"`
			End   string `json:"end"`
			Text  string `json:"text"`
		} `json:"window"`
	} `json:"official_signal"`
	LatestAlert *codexLatestAlert `json:"latest_alert"`
}

func normalizeCodexReset(body []byte, header http.Header, now time.Time) (Signal, error) {
	var payload codexResetPayload
	if err := decodeOne(body, &payload); err != nil {
		return Signal{}, err
	}
	checkedText := firstNonEmpty(header.Get("X-Published-Checked-At"), payload.UpdatedAt)
	checked, err := requiredTime(checkedText, "updated_at")
	if err != nil {
		return Signal{}, err
	}
	var expires *time.Time
	if text := header.Get("X-Published-Expires-At"); text != "" {
		parsed, parseErr := requiredTime(text, "X-Published-Expires-At")
		if parseErr != nil {
			return Signal{}, parseErr
		}
		expires = &parsed
	}
	kind := kindFromState(payload.Mode)
	// The forecast's ordinary mode ("model") is undocumented; only the
	// probability fields are. A snapshot that carries them and nothing more
	// specific is a tracker prediction.
	if kind == KindUnknown && (payload.Probabilities.Rounded24 != nil || payload.Probabilities.Rounded48 != nil) {
		kind = KindPrediction
	}
	signal := Signal{
		Kind:                kind,
		CheckedAt:           &checked,
		ExpiresAt:           expires,
		Freshness:           freshness(now, &checked, expires, nil, false),
		Window:              Window{Certainty: WindowMissing},
		GlobalAnnouncement:  true,
		Prediction24Percent: payload.Probabilities.Rounded24,
		Prediction48Percent: payload.Probabilities.Rounded48,
	}
	if payload.OfficialSignal != nil {
		item := payload.OfficialSignal
		signal.Kind = kindFromState(item.SignalType)
		signal.AnnouncementID = item.TweetID
		signal.Quote = item.Summary
		signal.ClaimedOriginalURL, signal.OriginalAccount = canonicalOriginal(item.URL)
		published, parseErr := requiredTime(item.At, "official_signal.at")
		if parseErr != nil {
			return Signal{}, parseErr
		}
		signal.PublishedAt = &published
		if item.Window != nil {
			window, parseErr := parseWindow(item.Window.Start, item.Window.End, item.Window.Text)
			if parseErr != nil {
				return Signal{}, parseErr
			}
			signal.Window = window
		}
	} else if strings.TrimSpace(payload.BankedState) != "" && !strings.EqualFold(payload.BankedState, "unknown") {
		signal.Kind = KindBanked
	}
	if payload.LatestAlert != nil {
		applyCodexLatestAlert(&signal, payload.LatestAlert, now)
	}
	applyCodexLastReset(&signal, payload.LastResetAt, payload.OfficialSignal != nil, now)
	validateSignal(&signal, now)
	return signal, nil
}

// codexLatestAlert is codex-reset's undocumented latest_alert object. It is
// supplementary: the documented reset signal is last_reset_at.
type codexLatestAlert struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	SourceAt  string `json:"source_at"`
	Summary   string `json:"summary"`
	URL       string `json:"url"`
	Corrected bool   `json:"corrected"`
	Window    *struct {
		Start string `json:"start"`
		End   string `json:"end"`
		Text  string `json:"text"`
	} `json:"window"`
}

func applyCodexLatestAlert(signal *Signal, alert *codexLatestAlert, now time.Time) {
	if signal.AnnouncementID == "" {
		// Without an official signal the snapshot is the tracker's live
		// prediction. A retained alert may name the announcement only
		// while its own source time is inside the current-report horizon;
		// an old, future-dated, or undated alert must not age or replace
		// the live probabilities, so it is noted and otherwise ignored. An
		// alert with no id names no announcement and is ignored the same
		// way: its empty id would otherwise match the snapshot's empty one.
		if strings.TrimSpace(alert.ID) == "" {
			signal.Warnings = append(signal.Warnings, "latest alert not used as the announcement: no id")
			return
		}
		published, usable, reason := fallbackAlertTime(alert.SourceAt, now)
		if !usable {
			signal.Warnings = append(signal.Warnings, "latest alert not used as the announcement: "+reason)
			return
		}
		signal.AnnouncementID = alert.ID
		signal.Quote = alert.Summary
		signal.ClaimedOriginalURL, signal.OriginalAccount = canonicalOriginal(alert.URL)
		signal.PublishedAt = &published
	}
	if alert.ID == signal.AnnouncementID {
		state := strings.ToLower(strings.TrimSpace(alert.State))
		switch {
		case state == "active":
			// An active alert leaves the claim unchanged. Its "corrected"
			// flag marks an edited alert message, not a retraction; keep
			// it visible as a warning only.
			if alert.Corrected {
				signal.Warnings = append(signal.Warnings, "active latest alert was edited by the tracker; the claim is unchanged")
			}
		case alertConfirmed(state):
			// A confirmed reset alert is a completion. Its "corrected"
			// flag marks an edited alert message, not a retraction. A
			// banked reset is a separate fact and is never replaced.
			switch {
			case signal.Kind == KindBanked:
				signal.Warnings = append(signal.Warnings, "confirmed alert did not replace the banked-reset record")
			case strings.EqualFold(strings.TrimSpace(alert.Kind), "reset"):
				signal.Kind = KindCompleted
			default:
				signal.Warnings = append(signal.Warnings, "confirmed alert of kind "+strconv.Quote(strings.TrimSpace(alert.Kind))+" did not change the claim")
			}
		default:
			// Withdrawn, superseded, expired, and any state Redline does
			// not recognize retract the claim: fail closed.
			signal.Withdrawn = true
			signal.Corrected = alert.Corrected
			if !alertRetracted(state) {
				signal.Warnings = append(signal.Warnings, "unrecognized latest alert state "+strconv.Quote(state)+" treated as a retraction")
			}
		}
	} else {
		signal.Warnings = append(signal.Warnings, "latest alert belongs to a different announcement and did not modify this signal")
	}
}

// applyCodexLastReset applies codex-reset's documented reset signal: a
// last_reset_at that moved forward confirms a global reset. It completes the
// snapshot only when it is inside the 72-hour current-report horizon, not
// future-dated, and nothing more specific or more cautious applies. It is
// not linked to any announcement, so it never relabels an official signal,
// never replaces a banked-reset record (a separate fact), never overrides a
// retraction, and never relabels an alert-named announcement.
func applyCodexLastReset(signal *Signal, lastResetAt string, hasOfficialSignal bool, now time.Time) {
	if strings.TrimSpace(lastResetAt) == "" {
		return
	}
	resetAt, err := requiredTime(lastResetAt, "last_reset_at")
	if err != nil {
		signal.Warnings = append(signal.Warnings, "invalid last_reset_at ignored")
		return
	}
	if resetAt.After(now.Add(5 * time.Minute)) {
		signal.Warnings = append(signal.Warnings, "last_reset_at is in the future and was ignored")
		return
	}
	if now.Sub(resetAt) > maxCurrentAnnouncementAge {
		// An older reset is history; the snapshot's live state stands.
		return
	}
	switch {
	case hasOfficialSignal:
		signal.Warnings = append(signal.Warnings, "last_reset_at reports a reset within 72 hours; it is not linked to the official signal and did not change its claim")
	case signal.Kind == KindBanked:
		signal.Warnings = append(signal.Warnings, "last_reset_at reports a reset within 72 hours; it did not replace the banked-reset record")
	case signal.Withdrawn || signal.Corrected:
		signal.Warnings = append(signal.Warnings, "last_reset_at reports a reset within 72 hours; the latest alert's retraction still applies")
	case signal.Kind == KindCompleted:
		// A confirmed reset alert already completed the snapshot.
	case signal.AnnouncementID != "":
		// last_reset_at names no post, so it never relabels the announcement a
		// latest alert named; only a confirmed reset alert completes that.
		signal.Warnings = append(signal.Warnings, "last_reset_at reports a reset within 72 hours; it is not linked to the latest alert's announcement and did not change its claim")
	default:
		signal.Kind = KindCompleted
		if signal.PublishedAt == nil {
			signal.PublishedAt = &resetAt
		}
		signal.Warnings = append(signal.Warnings, "completion derived from the documented last_reset_at; no original-source post is linked")
	}
}

// fallbackAlertTime parses a latest alert's source time and reports whether
// the alert is recent enough to stand in for an official signal.
func fallbackAlertTime(sourceAt string, now time.Time) (time.Time, bool, string) {
	if strings.TrimSpace(sourceAt) == "" {
		return time.Time{}, false, "no source_at"
	}
	published, err := requiredTime(sourceAt, "latest_alert.source_at")
	if err != nil {
		return time.Time{}, false, "invalid source_at"
	}
	if published.After(now.Add(5 * time.Minute)) {
		return time.Time{}, false, "source_at is in the future"
	}
	if now.Sub(published) > maxCurrentAnnouncementAge {
		return time.Time{}, false, "older than the 72-hour current-report horizon"
	}
	return published, true, ""
}

// alertRetracted reports alert states that withdraw the alert's claim.
func alertRetracted(state string) bool {
	switch state {
	case "withdrawn", "retracted", "superseded", "cancelled", "canceled", "expired", "dismissed", "false_alarm":
		return true
	default:
		return false
	}
}

// alertConfirmed reports alert states that confirm the event happened.
func alertConfirmed(state string) bool {
	switch state {
	case "confirmed", "completed", "fulfilled":
		return true
	default:
		return false
	}
}

func parseWindow(startText, endText, description string) (Window, error) {
	window := Window{Text: description, Certainty: WindowAmbiguous, Basis: "documented official_signal.window"}
	if startText == "" || endText == "" {
		return window, nil
	}
	start, err := requiredTime(startText, "window.start")
	if err != nil {
		return Window{}, err
	}
	end, err := requiredTime(endText, "window.end")
	if err != nil {
		return Window{}, err
	}
	window.Start = &start
	window.End = &end
	window.Certainty = WindowMirrorInterpretation
	window.Basis = "tracker-normalized official_signal.window; attributed to the tracker, not independently verified primary metadata"
	return window, nil
}

func validateSignal(signal *Signal, now time.Time) {
	signal.Prediction24Percent = validatedProbability(signal.Prediction24Percent, "24h", signal)
	signal.Prediction48Percent = validatedProbability(signal.Prediction48Percent, "48h", signal)
	if signal.ClaimedOriginalURL == "" && signal.Kind != KindPrediction && signal.Kind != KindUnknown && signal.Kind != KindBanked {
		signal.Warnings = append(signal.Warnings, "missing or invalid original-source URL")
	}
	if signal.PublishedAt != nil && signal.PublishedAt.After(now.Add(5*time.Minute)) {
		signal.Freshness = FutureDated
		signal.Warnings = append(signal.Warnings, "source publication time is in the future")
	}
	if signal.Window.End != nil && expiresWithDeadline(*signal) && !signal.Window.End.After(now) {
		signal.Warnings = append(signal.Warnings, "announcement window has expired; this does not prove completion")
	}
}

func validatedProbability(value *float64, label string, signal *Signal) *float64 {
	if value == nil {
		return nil
	}
	if *value < 0 || *value > 100 {
		signal.Warnings = append(signal.Warnings, "invalid "+label+" tracker probability omitted")
		return nil
	}
	return value
}

func freshness(now time.Time, checked, expires, published *time.Time, explicitlyStale bool) Freshness {
	if explicitlyStale {
		return Stale
	}
	for _, timestamp := range []*time.Time{checked, published} {
		if timestamp != nil && timestamp.After(now.Add(5*time.Minute)) {
			return FutureDated
		}
	}
	if expires == nil {
		return MissingTTL
	}
	if !expires.After(now) {
		return Stale
	}
	return Fresh
}

func requiredTime(value, field string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, fmt.Errorf("missing %s", field)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s: %w", field, err)
	}
	return parsed.UTC(), nil
}

func canonicalOriginal(raw string) (string, string) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return "", ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "x.com" && host != "twitter.com" && host != "www.x.com" && host != "www.twitter.com" {
		return "", ""
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "status" || parts[2] == "" {
		return "", ""
	}
	// One spelling per post: no query, fragment, or trailing slash, so the
	// same status cannot appear as two identities.
	return "https://x.com/" + parts[0] + "/status/" + parts[2], parts[0]
}

func kindFromState(value string) Kind {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "forecast", "prediction", "possible", "likely":
		return KindPrediction
	case "intent", "intended":
		return KindIntent
	case "promise", "promised", "announced":
		return KindPromised
	case "scheduled":
		return KindScheduled
	case "confirmed", "completed", "fulfilled", "reset":
		return KindCompleted
	case "banked", "available", "arriving":
		return KindBanked
	default:
		return KindUnknown
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func uniqueKinds(signals []Signal) []Kind {
	set := make(map[Kind]struct{})
	for _, signal := range signals {
		set[signal.Kind] = struct{}{}
	}
	kinds := make([]Kind, 0, len(set))
	for kind := range set {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}
