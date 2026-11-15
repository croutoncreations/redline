package harness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// UsageLimit describes a run the harness stopped because the account's
// subscription usage limit was reached, as opposed to the task failing.
type UsageLimit struct {
	// Message is the harness's own explanation, when it gave one.
	Message string `json:"message,omitempty"`
	// ResetsAt is when the limit lifts, when the harness reported it.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// LimitType names the window that ran out (for example five_hour or
	// seven_day), when the harness reported it.
	LimitType string `json:"limit_type,omitempty"`
	// SessionID is the harness's session for this run, so another process
	// can resume it (claude --resume, codex exec resume, pi --session).
	SessionID string `json:"session_id,omitempty"`
}

// usageLimitPhrases are the messages the CLIs print when a subscription
// limit stops a run. They are only matched against the CLI's own final
// error, never against tool output inside the transcript. Generic API 429
// wording ("rate_limit_error", an organization's per-minute limit) is left
// out: that is not a subscription limit. "your account's rate limit" is
// the API's refusal for a subscription (OAuth) account that is out of
// usage, which pi reports verbatim.
var usageLimitPhrases = []string{
	"hit your limit",
	"hit your usage limit",
	"usage limit",
	"out of codex",
	"5-hour limit",
	"weekly limit",
	"out of extra usage",
	"exceed your account's rate limit",
}

// legacyClaudeLimit matches Claude Code's older "Claude AI usage limit
// reached|<unix seconds>" message, which carries the reset time.
var legacyClaudeLimit = regexp.MustCompile(`usage limit reached\|(\d{9,11})`)

func mentionsUsageLimit(text string) bool {
	normalized := strings.ToLower(text)
	for _, phrase := range usageLimitPhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

// DetectUsageLimit reads a harness transcript and reports whether the run
// ended because the account's usage limit was reached. Only how the run
// ended counts: a limit the CLI hit and recovered from (overage, a retry,
// a fallback model) is not a stop. It reads the CLI's own structured
// events only, so a task whose tool output mentions a limit is not
// misclassified.
func DetectUsageLimit(harnessType, outputFile string) *UsageLimit {
	switch harnessType {
	case "claude-code", "codex-cli", "pi":
	default:
		return nil
	}
	data, err := readTail(outputFile, 256*1024)
	if err != nil || len(data) == 0 {
		return nil
	}
	var limit *UsageLimit
	switch harnessType {
	case "claude-code":
		limit = detectClaudeLimit(data)
	case "codex-cli":
		limit = detectCodexLimit(data)
	case "pi":
		limit = detectPiLimit(data)
	}
	if limit != nil {
		limit.SessionID = firstSessionID(harnessType, outputFile)
	}
	return limit
}

// eachEvent calls visit for every JSON object line in data.
func eachEvent(data []byte, visit func(eventType string, event map[string]json.RawMessage)) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var event map[string]json.RawMessage
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		visit(stringField(event, "type"), event)
	}
}

// detectClaudeLimit requires the run's final result to be an error. A
// rejected rate_limit_event that the run was not using overage for
// supplies the window and reset time; the final error text alone is also
// enough when it names the limit.
func detectClaudeLimit(data []byte) *UsageLimit {
	var rejection *UsageLimit
	var finalError string
	var finalIsError, sawResult bool
	eachEvent(data, func(eventType string, event map[string]json.RawMessage) {
		switch eventType {
		case "rate_limit_event":
			var info struct {
				Status         string `json:"status"`
				ResetsAt       int64  `json:"resetsAt"`
				RateLimitType  string `json:"rateLimitType"`
				OverageStatus  string `json:"overageStatus"`
				IsUsingOverage bool   `json:"isUsingOverage"`
			}
			if json.Unmarshal(event["rate_limit_info"], &info) != nil {
				return
			}
			if info.Status == "rejected" && !info.IsUsingOverage && info.OverageStatus != "allowed" {
				rejection = &UsageLimit{LimitType: info.RateLimitType, ResetsAt: unixTime(info.ResetsAt)}
			} else if info.Status == "allowed" || info.Status == "allowed_warning" {
				// The window opened again (or overage took over).
				rejection = nil
			}
		case "result":
			sawResult = true
			_ = json.Unmarshal(event["is_error"], &finalIsError)
			finalError = strings.TrimSpace(stringField(event, "result"))
		}
	})
	if !sawResult || !finalIsError {
		return nil
	}
	if rejection == nil && !mentionsUsageLimit(finalError) {
		return nil
	}
	limit := &UsageLimit{Message: finalError}
	if rejection != nil {
		limit.LimitType = rejection.LimitType
		limit.ResetsAt = rejection.ResetsAt
	}
	if limit.ResetsAt == nil {
		if match := legacyClaudeLimit.FindStringSubmatch(finalError); match != nil {
			seconds, _ := strconv.ParseInt(match[1], 10, 64)
			limit.ResetsAt = unixTime(seconds)
		}
	}
	return limit
}

// detectCodexLimit requires the last turn to have failed on the limit,
// with no completed turn after it.
func detectCodexLimit(data []byte) *UsageLimit {
	var limit *UsageLimit
	eachEvent(data, func(eventType string, event map[string]json.RawMessage) {
		var message string
		switch eventType {
		case "turn.completed", "turn.started":
			limit = nil
			return
		case "error":
			message = stringField(event, "message")
		case "turn.failed":
			var failure struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(event["error"], &failure)
			message = failure.Message
		default:
			return
		}
		if message != "" && mentionsUsageLimit(message) {
			limit = &UsageLimit{Message: strings.TrimSpace(message)}
		}
	})
	return limit
}

// detectPiLimit requires the last assistant message to have ended with an
// error naming the limit.
func detectPiLimit(data []byte) *UsageLimit {
	var limit *UsageLimit
	eachEvent(data, func(eventType string, event map[string]json.RawMessage) {
		if eventType != "message_end" {
			return
		}
		var message struct {
			Role         string `json:"role"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
		}
		if json.Unmarshal(event["message"], &message) != nil || message.Role != "assistant" {
			return
		}
		limit = nil
		if message.StopReason == "error" && mentionsUsageLimit(message.ErrorMessage) {
			limit = &UsageLimit{Message: strings.TrimSpace(message.ErrorMessage)}
		}
	})
	return limit
}

// firstSessionID reads the session identifier from the transcript's first
// lines, which the tail read may have cut off.
func firstSessionID(harnessType, path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for lines := 0; lines < 50 && scanner.Scan(); lines++ {
		var event map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch harnessType {
		case "claude-code":
			if id := stringField(event, "session_id"); id != "" {
				return id
			}
		case "codex-cli":
			if stringField(event, "type") == "thread.started" {
				return stringField(event, "thread_id")
			}
		case "pi":
			if stringField(event, "type") == "session" {
				return stringField(event, "id")
			}
		}
	}
	return ""
}

func stringField(event map[string]json.RawMessage, name string) string {
	var value string
	if raw, ok := event[name]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return value
}

func unixTime(seconds int64) *time.Time {
	if seconds <= 0 {
		return nil
	}
	value := time.Unix(seconds, 0).UTC()
	return &value
}

// UsageLimitFailure is the run error recorded for a usage-limit stop.
func UsageLimitFailure(harnessType string, limit UsageLimit) string {
	name := map[string]string{"claude-code": "Claude Code", "codex-cli": "Codex CLI", "pi": "pi"}[harnessType]
	if name == "" {
		name = harnessType
	}
	failure := name + " stopped at its usage limit."
	if limit.ResetsAt != nil {
		failure += " It resets at " + limit.ResetsAt.Format(time.RFC3339) + "."
	}
	if limit.SessionID != "" {
		failure += fmt.Sprintf(" The workspace and session %s are kept, so the job can be resumed.", limit.SessionID)
	}
	return failure
}
