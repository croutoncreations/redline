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
		if signal.Window.End != nil && expiresWithDeadline(signal) && !signal.Window.End.After(now) {
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
		// completedAt is the latest source time of a completion; zero when
		// the group has none.
		completedAt time.Time
		// announcedAt is the latest source publication time; zero when
		// only snapshot times are known.
		announcedAt time.Time
	}
	var candidates []candidate
	emptyFresh := false
	for key, signals := range grouped {
		var current []Signal
		var latest, completedAt, announcedAt time.Time
		for _, signal := range signals {
			if !isCurrent(signal, now) {
				continue
			}
			if carriesNoEvidence(key, signal) {
				emptyFresh = true
				continue
			}
			current = append(current, signal)
			if timestamp := signalTimestamp(signal); timestamp.After(latest) {
				latest = timestamp
			}
			if signal.PublishedAt != nil {
				if signal.PublishedAt.After(announcedAt) {
					announcedAt = *signal.PublishedAt
				}
				if isCompletion(signal) && signal.PublishedAt.After(completedAt) {
					completedAt = *signal.PublishedAt
				}
			}
		}
		if len(current) > 0 {
			candidates = append(candidates, candidate{key: key, signals: current, at: latest, completedAt: completedAt, announcedAt: announcedAt})
		}
	}
	if len(candidates) == 0 && emptyFresh {
		return KindUnknown, "Fresh public trackers report no current source-backed announcement and no live prediction."
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
	// Linked announcements outrank unlinked records, except that an unlinked
	// completion (for example codex-reset's completion from last_reset_at)
	// newer than every current linked announcement's publication is the
	// latest event. An unlinked retraction names no announcement, so it never
	// retracts one. Predictions and records dated only by snapshot time never
	// qualify, and a top linked group with no publication time keeps its
	// precedence.
	if top := candidates[0]; !strings.HasPrefix(top.key, "feed:") && !top.announcedAt.IsZero() {
		var newest time.Time
		for _, linked := range candidates {
			if !strings.HasPrefix(linked.key, "feed:") && linked.announcedAt.After(newest) {
				newest = linked.announcedAt
			}
		}
		for _, other := range candidates[1:] {
			// Candidates are sorted, so equal completion times keep key order.
			if strings.HasPrefix(other.key, "feed:") && other.completedAt.After(newest) {
				newest = other.completedAt
				selected = other.signals
			}
		}
	}
	for _, signal := range selected {
		if signal.Withdrawn || signal.Corrected || retractedByAnotherRecord(signal, results, now) {
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

// retractedByAnotherRecord reports whether a current record carries a
// retraction (RetractedAnnouncement) of the announcement this record names,
// matched by the same URL and ID identities used for grouping.
func retractedByAnotherRecord(signal Signal, results []SourceResult, now time.Time) bool {
	named := make(map[string]bool)
	for _, identity := range announcementIdentities(signal) {
		named[identity] = true
	}
	for _, result := range results {
		if result.Signal == nil || result.Signal.RetractedAnnouncement == nil || !isCurrent(*result.Signal, now) {
			continue
		}
		ref := result.Signal.RetractedAnnouncement
		for _, identity := range announcementIdentities(Signal{AnnouncementID: ref.ID, ClaimedOriginalURL: ref.URL}) {
			if named[identity] {
				return true
			}
		}
	}
	return false
}

// carriesNoEvidence reports a record that names no announcement and states
// nothing (for example Reset Beacon's "forecast": no new source-backed
// announcement). Such a record must not outrank another tracker's live
// evidence merely because it was checked more recently.
func carriesNoEvidence(groupKey string, signal Signal) bool {
	return strings.HasPrefix(groupKey, "feed:") && signal.Kind == KindUnknown && !signal.Withdrawn && !signal.Corrected &&
		signal.Prediction24Percent == nil && signal.Prediction48Percent == nil
}

// isCompletion reports an unretracted completion: the only unlinked record
// that can outrank an older linked announcement. Only its source time
// (PublishedAt) dates the event; a snapshot time does not.
func isCompletion(signal Signal) bool {
	return signal.Kind == KindCompleted && !signal.Withdrawn && !signal.Corrected
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
	// A window bounds only a forward-looking claim; a completion or a
	// retraction outlives it.
	if signal.Window.End != nil && expiresWithDeadline(signal) && !signal.Window.End.After(now) {
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

// groupEvidence deduplicates records that reference one original
// announcement. A record can name it by canonical URL, by announcement ID, or
// both, and trackers differ in which they supply (one may omit or fail to
// validate the URL that another provides). Records sharing either identity,
// directly or through a third record, are one announcement; keying on only
// one of them would count a single post twice. Records with neither stay
// separate per feed.
func groupEvidence(results []SourceResult) ([]EvidenceGroup, map[string][]Signal) {
	var signals []Signal
	for _, result := range results {
		if result.Signal != nil {
			signals = append(signals, *result.Signal)
		}
	}
	parent := make([]int, len(signals))
	for index := range parent {
		parent[index] = index
	}
	var find func(int) int
	find = func(index int) int {
		if parent[index] != index {
			parent[index] = find(parent[index])
		}
		return parent[index]
	}
	union := func(a, b int) {
		if rootA, rootB := find(a), find(b); rootA != rootB {
			if rootB < rootA {
				rootA, rootB = rootB, rootA
			}
			parent[rootB] = rootA
		}
	}
	firstByIdentity := make(map[string]int)
	for index, signal := range signals {
		for _, identity := range announcementIdentities(signal) {
			if first, seen := firstByIdentity[identity]; seen {
				union(first, index)
			} else {
				firstByIdentity[identity] = index
			}
		}
	}

	members := make(map[int][]Signal)
	for index, signal := range signals {
		root := find(index)
		members[root] = append(members[root], signal)
	}
	grouped := make(map[string][]Signal, len(members))
	groups := make([]EvidenceGroup, 0, len(members))
	for _, group := range members {
		url, id := groupIdentity(group)
		key := url
		if key == "" && id != "" {
			key = "announcement:" + id
		}
		if key == "" {
			key = "feed:" + group[0].Feed
		}
		grouped[key] = group
		feeds := make([]string, 0, len(group))
		for _, signal := range group {
			feeds = append(feeds, signal.Feed)
		}
		sort.Strings(feeds)
		groups = append(groups, EvidenceGroup{
			AnnouncementID: id, ClaimedOriginalURL: url,
			MirrorFeeds: feeds, DirectSourceVerified: false,
			VerificationStatus: "mirror_reference_not_directly_fetched", Kinds: uniqueKinds(group),
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		left := firstNonEmpty(groups[i].ClaimedOriginalURL, groups[i].AnnouncementID)
		right := firstNonEmpty(groups[j].ClaimedOriginalURL, groups[j].AnnouncementID)
		if left != right {
			return left < right
		}
		// Groups with no identity sort by feed so output order is stable.
		return strings.Join(groups[i].MirrorFeeds, ",") < strings.Join(groups[j].MirrorFeeds, ",")
	})
	return groups, grouped
}

// announcementIdentities lists the identities a record names its original
// announcement by. URL and ID live in separate namespaces so an ID can never
// collide with a URL string.
//
// The ID is joined to the URL only when they agree (the canonical status URL
// ends in that ID). A record whose URL and ID name different posts would
// otherwise bridge two real announcements, letting one's correction retract
// the other; such a record is identified by its URL alone.
func announcementIdentities(signal Signal) []string {
	url := strings.TrimSpace(signal.ClaimedOriginalURL)
	id := strings.TrimSpace(signal.AnnouncementID)
	var identities []string
	if url != "" {
		identities = append(identities, "url:"+url)
	}
	if id != "" && (url == "" || statusID(url) == id) {
		identities = append(identities, "id:"+id)
	}
	return identities
}

// statusID is the final path segment of a canonical x.com status URL.
func statusID(canonicalURL string) string {
	canonicalURL = strings.TrimSuffix(canonicalURL, "/")
	return canonicalURL[strings.LastIndex(canonicalURL, "/")+1:]
}

// groupIdentity picks the URL and ID shown for a deduplicated group: the
// first non-empty value of each in feed order, so the choice does not
// depend on fetch completion order.
func groupIdentity(group []Signal) (string, string) {
	ordered := append([]Signal(nil), group...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Feed < ordered[j].Feed })
	var url, id string
	for _, signal := range ordered {
		if url == "" {
			url = strings.TrimSpace(signal.ClaimedOriginalURL)
		}
	}
	// Show only an ID that took part in grouping and agrees with the URL.
	for _, signal := range ordered {
		candidate := strings.TrimSpace(signal.AnnouncementID)
		if candidate == "" || id != "" {
			continue
		}
		if own := strings.TrimSpace(signal.ClaimedOriginalURL); own != "" && statusID(own) != candidate {
			continue
		}
		if url != "" && statusID(url) != candidate {
			continue
		}
		id = candidate
	}
	return url, id
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
