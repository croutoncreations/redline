// Package resetsignal provides a read-only, fail-closed preview of public
// Codex reset announcements. It is deliberately independent of the scheduler
// and notification service.
package resetsignal

import "time"

// Kind is the normalized meaning of a source record. The distinctions are
// intentional: predictions, promises, completed resets, and banked resets are
// not interchangeable evidence.
type Kind string

const (
	KindPrediction Kind = "PREDICTION"
	KindIntent     Kind = "INTENT"
	KindPromised   Kind = "PROMISED"
	KindScheduled  Kind = "SCHEDULED"
	KindCompleted  Kind = "COMPLETED"
	KindBanked     Kind = "BANKED"
	KindUnknown    Kind = "UNKNOWN"
)

type Freshness string

const (
	Fresh       Freshness = "fresh"
	Stale       Freshness = "stale"
	MissingTTL  Freshness = "missing_ttl"
	FutureDated Freshness = "future_dated"
)

type WindowCertainty string

const (
	WindowMissing              WindowCertainty = "missing"
	WindowAmbiguous            WindowCertainty = "ambiguous"
	WindowMirrorInterpretation WindowCertainty = "mirror_interpretation"
	WindowExplicitPrimary      WindowCertainty = "explicit_primary"
)

type Window struct {
	Start     *time.Time      `json:"start,omitempty"`
	End       *time.Time      `json:"end,omitempty"`
	Text      string          `json:"text,omitempty"`
	Certainty WindowCertainty `json:"certainty"`
	Basis     string          `json:"basis,omitempty"`
}

// Signal contains only normalized, decision-relevant source fields.
type Signal struct {
	Feed                 string     `json:"feed"`
	FeedURL              string     `json:"feed_url"`
	Attribution          string     `json:"attribution"`
	Kind                 Kind       `json:"kind"`
	AnnouncementID       string     `json:"announcement_id,omitempty"`
	ClaimedOriginalURL   string     `json:"claimed_original_source_url,omitempty"`
	OriginalAccount      string     `json:"original_account,omitempty"`
	Quote                string     `json:"quote,omitempty"`
	PublishedAt          *time.Time `json:"published_at,omitempty"`
	CheckedAt            *time.Time `json:"checked_at,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	Window               Window     `json:"window"`
	Freshness            Freshness  `json:"freshness"`
	Withdrawn            bool       `json:"withdrawn"`
	Corrected            bool       `json:"corrected"`
	GlobalAnnouncement   bool       `json:"global_announcement"`
	ObservedAccountReset bool       `json:"observed_account_reset"`
	Prediction24Percent  *float64   `json:"prediction_24h_percent,omitempty"`
	Prediction48Percent  *float64   `json:"prediction_48h_percent,omitempty"`
	// RetractedAnnouncement names an announcement a tracker retracted when
	// the retraction does not apply to this record itself (a banked-reset
	// record is a separate fact). The report still retracts that announcement.
	RetractedAnnouncement *AnnouncementRef `json:"retracted_announcement,omitempty"`
	Warnings              []string         `json:"warnings,omitempty"`
}

// AnnouncementRef identifies an original announcement by tracker ID and
// canonical original-source URL.
type AnnouncementRef struct {
	ID  string `json:"id,omitempty"`
	URL string `json:"claimed_original_source_url,omitempty"`
}

type FetchMetadata struct {
	Attempts       int           `json:"attempts"`
	RetryAfter     time.Duration `json:"retry_after,omitempty"`
	BackoffApplied time.Duration `json:"backoff_applied,omitempty"`
	StatusCode     int           `json:"status_code,omitempty"`
}

type SourceError struct {
	Feed     string        `json:"feed"`
	Code     string        `json:"code"`
	Message  string        `json:"message"`
	Metadata FetchMetadata `json:"metadata"`
}

type SourceResult struct {
	Signal   *Signal       `json:"signal,omitempty"`
	Error    *SourceError  `json:"error,omitempty"`
	Metadata FetchMetadata `json:"metadata"`
}

type EvidenceGroup struct {
	AnnouncementID       string   `json:"announcement_id"`
	ClaimedOriginalURL   string   `json:"claimed_original_source_url"`
	MirrorFeeds          []string `json:"mirror_feeds"`
	DirectSourceVerified bool     `json:"direct_source_verified"`
	VerificationStatus   string   `json:"verification_status"`
	Kinds                []Kind   `json:"kinds"`
}

type AttributedPrediction struct {
	Feed      string    `json:"feed"`
	SourceURL string    `json:"source_url"`
	Percent24 *float64  `json:"percent_24h,omitempty"`
	Percent48 *float64  `json:"percent_48h,omitempty"`
	Freshness Freshness `json:"freshness"`
	Current   bool      `json:"current"`
}

type Report struct {
	GeneratedAt                 time.Time              `json:"generated_at"`
	Mode                        string                 `json:"mode"`
	Sources                     []SourceResult         `json:"sources"`
	Evidence                    []EvidenceGroup        `json:"evidence"`
	State                       Kind                   `json:"state"`
	Summary                     string                 `json:"summary"`
	CurrentPredictions          []AttributedPrediction `json:"current_predictions"`
	HistoricalPredictions       []AttributedPrediction `json:"historical_predictions"`
	CombinedProbability         *float64               `json:"combined_probability"`
	UniqueOriginalAnnouncements int                    `json:"unique_original_announcements"`
	Applicability               string                 `json:"applicability"`
	Uncertainty                 []string               `json:"uncertainty"`
	AdvisoryAssessment          string                 `json:"advisory_assessment"`
}
