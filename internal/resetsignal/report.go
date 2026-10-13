package resetsignal

import (
	"sort"
	"strings"
	"time"
)

const maxCurrentAnnouncementAge = 72 * time.Hour

// BuildReport creates an explanatory, read-only likelihood report. Public
// feed text is data only: it is never interpreted as an instruction and the
// report has no execution, notification, or scheduler capability.
func BuildReport(now time.Time, sources []SourceResult) Report {
	report := Report{
		GeneratedAt: now.UTC(), Mode: "likelihood_read_only", Sources: sources,
		State: KindUnknown, CombinedProbability: nil,
		Applicability:      "Unknown for any individual account; public announcements and tracker forecasts do not prove an account reset or available allowance.",
		AdvisoryAssessment: "disabled",
	}
	report.Evidence, _ = groupEvidence(sources)
	report.UniqueOriginalAnnouncements = countUniqueOriginalAnnouncements(report.Evidence)

	for _, result := range sources {
		if result.Error != nil {
			report.Uncertainty = append(report.Uncertainty, result.Error.Feed+": "+result.Error.Code)
			continue
		}
		if result.Signal == nil {
			continue
		}
		signal := *result.Signal
		current := isCurrent(signal, now)
		if signal.Prediction24Percent != nil || signal.Prediction48Percent != nil {
			prediction := AttributedPrediction{
				Feed: signal.Feed, SourceURL: signal.FeedURL,
				Percent24: signal.Prediction24Percent, Percent48: signal.Prediction48Percent,
				Freshness: signal.Freshness, Current: current,
			}
			if current {
				report.CurrentPredictions = append(report.CurrentPredictions, prediction)
			} else {
				report.HistoricalPredictions = append(report.HistoricalPredictions, prediction)
			}
		}
		if signal.Freshness != Fresh {
			report.Uncertainty = append(report.Uncertainty, signal.Feed+": freshness is "+string(signal.Freshness))
		}
		if signal.PublishedAt != nil && now.Sub(*signal.PublishedAt) > maxCurrentAnnouncementAge {
			report.Uncertainty = append(report.Uncertainty, signal.Feed+": source announcement is older than the 72-hour current-report horizon")
		}
		if signal.Window.End != nil && !signal.Window.End.After(now) {
			report.Uncertainty = append(report.Uncertainty, signal.Feed+": reported window expired; expiry does not prove completion")
		}
		if signal.Window.Certainty != WindowExplicitPrimary {
			report.Uncertainty = append(report.Uncertainty, signal.Feed+": no independently verified explicit source window")
		}
	}

	report.State, report.Summary = summarizeCurrent(now, sources)
	for _, group := range report.Evidence {
		if len(group.MirrorFeeds) > 1 {
			report.Uncertainty = append(report.Uncertainty, "multiple trackers reference "+firstNonEmpty(group.ClaimedOriginalURL, group.AnnouncementID)+"; this is one original announcement reference, not direct verification or independent confirmation")
		}
	}
	if len(report.CurrentPredictions)+len(report.HistoricalPredictions) > 1 {
		report.Uncertainty = append(report.Uncertainty, "tracker probabilities are separately attributed and are not averaged or treated as independent evidence")
	}
	report.Uncertainty = append(report.Uncertainty, "Redline does not calculate a combined reset probability")
	report.Uncertainty = uniqueStrings(report.Uncertainty)
	return report
}

func summarizeCurrent(now time.Time, results []SourceResult) (Kind, string) {
	_, grouped := groupEvidence(results)
	type candidate struct {
		key     string
		signals []Signal
		at      time.Time
	}
	var candidates []candidate
	for key, signals := range grouped {
		var current []Signal
		var latest time.Time
		for _, signal := range signals {
			if !isCurrent(signal, now) {
				continue
			}
			current = append(current, signal)
			if timestamp := signalTimestamp(signal); timestamp.After(latest) {
				latest = timestamp
			}
		}
		if len(current) > 0 {
			candidates = append(candidates, candidate{key: key, signals: current, at: latest})
		}
	}
	if len(candidates) == 0 {
		return KindUnknown, "No fresh, current public tracker evidence is available; any retained records are historical only."
	}
	sort.Slice(candidates, func(i, j int) bool {
		iLinked := !strings.HasPrefix(candidates[i].key, "feed:")
		jLinked := !strings.HasPrefix(candidates[j].key, "feed:")
		if iLinked != jLinked {
			return iLinked
		}
		if !candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].at.After(candidates[j].at)
		}
		return candidates[i].key < candidates[j].key
	})
	selected := candidates[0].signals
	for _, signal := range selected {
		if signal.Withdrawn || signal.Corrected {
			return KindUnknown, "The fresh record for the selected announcement is corrected or withdrawn; its optimistic interpretation is retracted."
		}
	}
	for _, preferred := range []Kind{KindCompleted, KindScheduled, KindPromised, KindIntent, KindPrediction, KindBanked} {
		for _, signal := range selected {
			if signal.Kind != preferred {
				continue
			}
			switch preferred {
			case KindCompleted:
				return preferred, "A fresh tracker record reports a completed global event; this is not proof that any particular account reset."
			case KindScheduled:
				if hasExplicitPrimaryWindow(selected) {
					return preferred, "Fresh public trackers classify the source post as scheduled and include an explicit source window."
				}
				return KindPromised, "A fresh tracker labels the post scheduled, but without an independently verified explicit source window Redline reports only an attributed promise; timing remains ambiguous."
			case KindPromised:
				return preferred, "Fresh public trackers classify the source post as a promise; timing remains ambiguous and completion is not established."
			case KindIntent:
				return preferred, "A fresh public tracker reports intent, not a schedule or completion."
			case KindPrediction:
				return preferred, "Only fresh tracker predictions are available; their probabilities are shown with attribution and are not Redline confidence."
			case KindBanked:
				return preferred, "A fresh banked-reset record is a separate fact and is not treated as a completed reset."
			}
		}
	}
	return KindUnknown, "The fresh public tracker state is unknown."
}

func hasExplicitPrimaryWindow(signals []Signal) bool {
	for _, signal := range signals {
		if signal.Kind == KindScheduled && signal.Window.Certainty == WindowExplicitPrimary && signal.Window.End != nil {
			return true
		}
	}
	return false
}

func isCurrent(signal Signal, now time.Time) bool {
	if signal.Freshness != Fresh {
		return false
	}
	if signal.PublishedAt != nil {
		age := now.Sub(*signal.PublishedAt)
		if age < -5*time.Minute || age > maxCurrentAnnouncementAge {
			return false
		}
	}
	if signal.Window.End != nil && !signal.Window.End.After(now) {
		return false
	}
	return true
}

func signalTimestamp(signal Signal) time.Time {
	if signal.PublishedAt != nil {
		return *signal.PublishedAt
	}
	if signal.CheckedAt != nil {
		return *signal.CheckedAt
	}
	return time.Time{}
}

func groupEvidence(results []SourceResult) ([]EvidenceGroup, map[string][]Signal) {
	grouped := make(map[string][]Signal)
	for _, result := range results {
		if result.Signal == nil {
			continue
		}
		signal := *result.Signal
		key := signal.ClaimedOriginalURL
		if key == "" && signal.AnnouncementID != "" {
			key = "announcement:" + signal.AnnouncementID
		}
		if key == "" {
			key = "feed:" + signal.Feed
		}
		grouped[key] = append(grouped[key], signal)
	}
	groups := make([]EvidenceGroup, 0, len(grouped))
	for _, signals := range grouped {
		feeds := make([]string, 0, len(signals))
		for _, signal := range signals {
			feeds = append(feeds, signal.Feed)
		}
		sort.Strings(feeds)
		groups = append(groups, EvidenceGroup{
			AnnouncementID: signals[0].AnnouncementID, ClaimedOriginalURL: signals[0].ClaimedOriginalURL,
			MirrorFeeds: feeds, DirectSourceVerified: false,
			VerificationStatus: "mirror_reference_not_directly_fetched", Kinds: uniqueKinds(signals),
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		return firstNonEmpty(groups[i].ClaimedOriginalURL, groups[i].AnnouncementID) < firstNonEmpty(groups[j].ClaimedOriginalURL, groups[j].AnnouncementID)
	})
	return groups, grouped
}

func countUniqueOriginalAnnouncements(groups []EvidenceGroup) int {
	count := 0
	for _, group := range groups {
		if group.ClaimedOriginalURL != "" || group.AnnouncementID != "" {
			count++
		}
	}
	return count
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
