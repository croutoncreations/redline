package resetsignal

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
)

type Announcement struct {
	Key                  string          `json:"key"`
	Kind                 Kind            `json:"kind"`
	Quote                string          `json:"quote"`
	QuoteSource          string          `json:"quote_source,omitempty"`
	QuoteFeed            string          `json:"quote_feed,omitempty"`
	Attribution          string          `json:"attribution,omitempty"`
	AttributionURLs      string          `json:"attribution_urls,omitempty"`
	URL                  string          `json:"url"`
	Account              string          `json:"account"`
	PublishedAt          *time.Time      `json:"published_at"`
	WindowEnd            *time.Time      `json:"window_end"`
	WindowCertainty      WindowCertainty `json:"window_certainty"`
	Feeds                []string        `json:"feeds"`
	DirectSourceVerified bool            `json:"direct_source_verified"`
}

// ViableAnnouncement preserves the report's blocking rules (including newer
// unlinked completions), then prefers the best-backed viable linked group.
// This alert-only ranking does not change the report's state or selection.
func ViableAnnouncement(report Report) *Announcement {
	var selected []Signal
	kind, _ := summarizeCurrentSelection(report.GeneratedAt, report.Sources, &selected)
	if announcementKindRank(kind) == 0 {
		return nil
	}
	selectedViable := false
	for _, s := range selected {
		if viableAnnouncementKind(s, report) == kind {
			selectedViable = true
		}
	}
	if !selectedViable {
		return nil
	}

	type candidate struct {
		announcement Announcement
		at           time.Time
	}
	var candidates []candidate
	_, groups := groupEvidence(report.Sources)
	for key, members := range groups {
		var viable []Signal
		var strongest Signal
		var groupKind Kind
		var latest time.Time
		retracted := false
		for _, s := range members {
			if isCurrent(s, report.GeneratedAt) && (s.Withdrawn || s.Corrected || retractedByAnotherRecord(s, report.Sources, report.GeneratedAt)) {
				retracted = true
			}
			k := viableAnnouncementKind(s, report)
			if k == "" {
				continue
			}
			viable = append(viable, s)
			if s.PublishedAt.After(latest) {
				latest = *s.PublishedAt
			}
			if announcementKindRank(k) > announcementKindRank(groupKind) || (k == groupKind && signalTimestamp(s).After(signalTimestamp(strongest))) {
				groupKind, strongest = k, s
			}
		}
		if retracted || len(viable) == 0 {
			continue
		}
		sort.SliceStable(viable, func(i, j int) bool {
			iOriginal, jOriginal := hasOriginalQuote(viable[i]), hasOriginalQuote(viable[j])
			if iOriginal != jOriginal {
				return iOriginal
			}
			if !viable[i].PublishedAt.Equal(*viable[j].PublishedAt) {
				return viable[i].PublishedAt.After(*viable[j].PublishedAt)
			}
			return viable[i].Feed < viable[j].Feed
		})
		backing := viable[0]
		quote, quoteSource, quoteFeed := backing.Quote, backing.QuoteSource, backing.Feed
		if !hasOriginalQuote(backing) {
			// A mirror of the same post may carry the post's own words even
			// when it cannot back the alert itself (e.g. no TTL). Its text
			// is used for the quote only; kind and viability never change.
			for _, s := range members {
				if hasOriginalQuote(s) && !s.Withdrawn && !s.Corrected {
					quote, quoteSource, quoteFeed = s.Quote, s.QuoteSource, s.Feed
					break
				}
			}
		}
		url, _ := groupIdentity(members)
		account := backing.OriginalAccount
		if account == "" {
			_, account = canonicalOriginal(url)
		}
		var feeds, attributions, attributionURLs []string
		for _, s := range members {
			feeds = append(feeds, s.Feed)
			if s.Attribution != "" {
				attributions = append(attributions, s.Attribution)
			}
			if s.AttributionURL != "" {
				attributionURLs = append(attributionURLs, s.AttributionURL)
			}
		}
		sort.Strings(feeds)
		sort.Strings(attributions)
		sort.Strings(attributionURLs)
		a := Announcement{Key: key, Kind: groupKind, Quote: sanitizeText(quote, 280), QuoteSource: quoteSource, QuoteFeed: quoteFeed, Attribution: strings.Join(uniqueStrings(attributions), ","), AttributionURLs: strings.Join(uniqueStrings(attributionURLs), ","), URL: url, Account: sanitizeText(account, 80), PublishedAt: backing.PublishedAt, WindowEnd: strongest.Window.End, WindowCertainty: strongest.Window.Certainty, Feeds: uniqueStrings(feeds)}
		candidates = append(candidates, candidate{announcement: a, at: latest})
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i].announcement, candidates[j].announcement
		if len(a.Feeds) != len(b.Feeds) {
			return len(a.Feeds) > len(b.Feeds)
		}
		aOriginal, bOriginal := a.QuoteSource == "original_post" && a.Quote != "", b.QuoteSource == "original_post" && b.Quote != ""
		if aOriginal != bOriginal {
			return aOriginal
		}
		if a.Kind != b.Kind {
			return announcementKindRank(a.Kind) > announcementKindRank(b.Kind)
		}
		if !candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].at.After(candidates[j].at)
		}
		return a.Key < b.Key
	})
	if len(candidates) == 0 {
		return nil
	}
	return &candidates[0].announcement
}

func hasOriginalQuote(s Signal) bool {
	return s.QuoteSource == "original_post" && strings.TrimSpace(s.Quote) != ""
}

func announcementKindRank(kind Kind) int {
	switch kind {
	case KindCompleted:
		return 3
	case KindScheduled:
		return 2
	case KindPromised:
		return 1
	default:
		return 0
	}
}

func viableAnnouncementKind(s Signal, report Report) Kind {
	if !isCurrent(s, report.GeneratedAt) || s.PublishedAt == nil || s.PublishedAt.After(report.GeneratedAt) || (s.ClaimedOriginalURL == "" && s.AnnouncementID == "") || s.Withdrawn || s.Corrected || retractedByAnotherRecord(s, report.Sources, report.GeneratedAt) {
		return ""
	}
	kind := s.Kind
	if kind == KindScheduled && (s.Window.End == nil || s.Window.Certainty != WindowExplicitPrimary) {
		kind = KindPromised
	}
	if announcementKindRank(kind) == 0 {
		return ""
	}
	return kind
}

// AnnouncementRetracted requires a fresh matching correction, not disappearance
// of a post, a failed poll, an expired window, or an unrelated tracker edit.
func AnnouncementRetracted(report Report, announcement Announcement) bool {
	ref := Signal{ClaimedOriginalURL: announcement.URL}
	if strings.HasPrefix(announcement.Key, "announcement:") {
		ref.AnnouncementID = strings.TrimPrefix(announcement.Key, "announcement:")
	} else if ref.ClaimedOriginalURL == "" {
		ref.ClaimedOriginalURL = announcement.Key
	}
	if ref.ClaimedOriginalURL != "" {
		ref.AnnouncementID = statusID(ref.ClaimedOriginalURL)
	}
	if retractedByAnotherRecord(ref, report.Sources, report.GeneratedAt) {
		return true
	}
	// Group with the stored identity to handle an ID-only correction to a URL.
	results := append([]SourceResult(nil), report.Sources...)
	ref.Feed = "stored-announcement"
	results = append(results, SourceResult{Signal: &ref})
	_, groups := groupEvidence(results)
	for _, group := range groups {
		matches := false
		for _, s := range group {
			if s.Feed == ref.Feed {
				matches = true
			}
		}
		if !matches {
			continue
		}
		for _, s := range group {
			if isCurrent(s, report.GeneratedAt) && (s.Withdrawn || s.Corrected) {
				return true
			}
		}
	}
	return false
}

func sanitizeText(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	chars := []rune(strings.Join(strings.Fields(text), " "))
	if len(chars) > limit {
		chars = chars[:limit]
	}
	return string(chars)
}

func (a Announcement) Notification(now time.Time, retracted bool) domain.NotificationEvent {
	account := strings.TrimPrefix(sanitizeText(a.Account, 80), "@")
	attribution := "public trackers"
	if account != "" {
		attribution = "@" + account
	}
	quote := a.Quote
	if a.QuoteSource == "original_post" {
		quote = stripTrailingPostLinks(quote)
	}
	quote = sanitizeText(quote, 280)
	label := attribution
	if account == "thsottiaux" {
		label = "Tibo (@thsottiaux)"
	}
	status := "promised"
	if a.Kind == KindCompleted {
		status = "reported complete"
	} else if a.Kind == KindScheduled {
		status = "scheduled"
	}
	if a.Kind != KindCompleted && a.WindowEnd != nil {
		status += " by " + a.WindowEnd.UTC().Format("Mon 15:04 MST")
		if a.WindowCertainty != WindowExplicitPrimary {
			status += " (tracker's reading)"
		}
	}
	title := "Codex reset announced"
	switch a.Kind {
	case KindPromised:
		title = "Codex reset promised"
	case KindScheduled:
		title = "Codex reset scheduled"
	}
	message := "Global Codex reset " + status + "."
	if quote != "" && a.QuoteSource == "original_post" {
		quoteSentence := fmt.Sprintf("%s: %q", label, quote)
		if !strings.ContainsAny(quote[len(quote)-1:], ".!?") {
			quoteSentence += "."
		}
		message = quoteSentence + " " + message
	} else if quote != "" {
		feed := sanitizeText(a.QuoteFeed, 80)
		if feed == "" {
			feed = "tracker"
		}
		message = fmt.Sprintf("Codex global reset %s (%s: %q).", status, feed, quote)
		if a.URL != "" {
			message += " Source post by " + attribution + ": " + a.URL + "."
		}
	}
	message += " Seen by " + strings.Join(a.Feeds, ", ") + "."
	if retracted {
		title = "Codex reset announcement retracted"
		message = "Retracted: the earlier Codex reset announcement from " + attribution + " was withdrawn or corrected."
	}
	if a.Attribution != "" {
		var credits []string
		for _, credit := range strings.Split(a.Attribution, ",") {
			credits = append(credits, strings.TrimPrefix(strings.TrimSpace(credit), "Data: "))
		}
		credit := strings.Join(credits, ", ")
		if len(a.Feeds) == 1 && a.Feeds[0] == "codex-reset" {
			credit = "https://codex-reset.com/"
		}
		message += " Data: " + credit + "."
	}
	data := map[string]string{"key": a.Key, "kind": string(a.Kind), "url": a.URL, "account": account, "quote": quote, "quote_source": a.QuoteSource, "attribution": a.Attribution, "attribution_urls": a.AttributionURLs, "published_at": timestamp(a.PublishedAt), "window_end": timestamp(a.WindowEnd), "window_certainty": string(a.WindowCertainty), "feeds": strings.Join(a.Feeds, ","), "direct_source_verified": "false", "retracted": fmt.Sprint(retracted)}
	return domain.NotificationEvent{Version: 1, Type: domain.EventResetAnnounced, OccurredAt: now.UTC(), Title: title, Message: message, Data: data}
}

func timestamp(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// ResetEventCandidate is an unverified PR-3 seam only. No scheduler consumes it.
func (a Announcement) ResetEventCandidate() decision.ResetEvent {
	e := decision.ResetEvent{Source: decision.ResetPredictedGlobal, Clears: []string{"weekly"}, Confidence: "unverified", Detail: a.URL}
	if a.WindowEnd != nil {
		e.At = *a.WindowEnd
	}
	return e
}
