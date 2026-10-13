package domain

import "time"

// PrimerMode selects when the window primer sends its ping.
type PrimerMode string

const (
	// PrimerSchedule pings at fixed local times so a 5-hour window opens
	// when the operator wants it to.
	PrimerSchedule PrimerMode = "schedule"
	// PrimerReset pings right after every reset so a window is always open.
	PrimerReset PrimerMode = "reset"
)

// PrimerSettings configures the window primer for one provider account.
// Times are "HH:MM" in Timezone; an empty Timezone means the service's local
// zone. Days holds lower-case three-letter weekday names; empty means every
// day. Times and Days apply only to schedule mode.
type PrimerSettings struct {
	ProviderAccountID string     `json:"provider_account_id"`
	Enabled           bool       `json:"enabled"`
	Mode              PrimerMode `json:"mode"`
	Times             []string   `json:"times"`
	Days              []string   `json:"days"`
	Timezone          string     `json:"timezone"`
	Prompt            string     `json:"prompt"`
	Model             string     `json:"model"`
	CatchUpSeconds    int64      `json:"catch_up_seconds"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type PrimerOutcome string

const (
	PrimerRunning PrimerOutcome = "running"
	PrimerFired   PrimerOutcome = "fired"
	PrimerSkipped PrimerOutcome = "skipped"
	PrimerFailed  PrimerOutcome = "failed"
)

type PrimerVerification string

const (
	PrimerVerifyNone    PrimerVerification = ""
	PrimerVerifyPending PrimerVerification = "pending"
	PrimerVerified      PrimerVerification = "verified"
	// PrimerVerifyUnverified: a later usage sample showed no new window.
	PrimerVerifyUnverified PrimerVerification = "unverified"
	// PrimerVerifyUnknown: no usage sample arrived to check against. The
	// ping is presumed to have worked: it neither counts as a failure nor
	// drops the window it is believed to have opened.
	PrimerVerifyUnknown PrimerVerification = "unknown"
)

// PrimerAttempt records one decision to ping or not. SlotKey identifies the
// scheduled occurrence so a slot is never handled twice, including across
// restarts.
type PrimerAttempt struct {
	ID                int64              `json:"id"`
	ProviderAccountID string             `json:"provider_account_id"`
	Trigger           string             `json:"trigger"`
	SlotKey           string             `json:"slot_key"`
	TargetAt          time.Time          `json:"target_at"`
	Outcome           PrimerOutcome      `json:"outcome"`
	Reason            string             `json:"reason,omitempty"`
	Output            string             `json:"output,omitempty"`
	Verification      PrimerVerification `json:"verification,omitempty"`
	WindowResetsAt    *time.Time         `json:"window_resets_at,omitempty"`
	StartedAt         time.Time          `json:"started_at"`
	CompletedAt       time.Time          `json:"completed_at"`
	VerifiedAt        *time.Time         `json:"verified_at,omitempty"`
}

const EventPrimerFailed = "primer.failed"

// PrimerInterruptedReason marks attempts a restart or shutdown cut short.
// Stored rows are matched on this exact text, so it must never change.
const PrimerInterruptedReason = "Interrupted by a service restart; the ping may not have been sent."

// Interrupted reports whether a restart, not the ping, failed the attempt.
func (a PrimerAttempt) Interrupted() bool {
	return a.Outcome == PrimerFailed && a.Reason == PrimerInterruptedReason
}

// Failed reports whether the ping failed or, though sent, opened no window
// at all (as opposed to landing in one already open).
func (a PrimerAttempt) Failed() bool {
	if a.Outcome == PrimerFailed {
		return !a.Interrupted()
	}
	return a.Outcome == PrimerFired && a.Verification == PrimerVerifyUnverified && a.WindowResetsAt == nil
}
