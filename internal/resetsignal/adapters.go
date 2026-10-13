package resetsignal

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
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
	signal := Signal{
		Kind:               kindFromState(firstNonEmpty(payload.Answer.State, payload.PublicationState)),
		CheckedAt:          &checked,
		ExpiresAt:          &expires,
		Freshness:          freshness(now, &checked, &expires, nil, payload.StaleSince != nil),
		Window:             Window{Text: payload.Answer.Window, Certainty: WindowMissing},
		GlobalAnnouncement: true,
	}
	signal.Withdrawn = strings.EqualFold(payload.PublicationState, "withdrawn") || strings.EqualFold(payload.PublicationState, "superseded")
	signal.Corrected = strings.EqualFold(payload.PublicationState, "corrected")
	if len(payload.Answer.Posts) > 0 {
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
		_ = deadline
		signal.Window.Certainty = WindowMirrorInterpretation
		signal.Window.Basis = "Reset Beacon supplied an interpreted deadline; omitted because the primary quote does not clarify the PST timezone phrase"
		signal.Warnings = append(signal.Warnings, "tracker-interpreted deadline is not reported as a primary-source deadline")
	}
	validateSignal(&signal, now)
	return signal, nil
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
	LatestAlert *struct {
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
	} `json:"latest_alert"`
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
	signal := Signal{
		Kind:                kindFromState(payload.Mode),
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
		alert := payload.LatestAlert
		if signal.AnnouncementID == "" {
			signal.AnnouncementID = alert.ID
			signal.Quote = alert.Summary
			signal.ClaimedOriginalURL, signal.OriginalAccount = canonicalOriginal(alert.URL)
		}
		if alert.ID == signal.AnnouncementID {
			signal.Corrected = alert.Corrected
			signal.Withdrawn = !strings.EqualFold(alert.State, "active")
		} else {
			signal.Warnings = append(signal.Warnings, "latest alert belongs to a different announcement and did not modify this signal")
		}
	}
	validateSignal(&signal, now)
	return signal, nil
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
	if signal.Window.End != nil && !signal.Window.End.After(now) {
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
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Host = "x.com"
	return parsed.String(), parts[0]
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
