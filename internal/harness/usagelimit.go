package harness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	// Prompt is the prompt the harness was given, for the usage limit
	// command. It is the copy read before the run, so the hook gets it
	// even if the agent changed or removed prompt_file. Not serialized:
	// it may be large and belongs to the task.
	Prompt string `json:"-"`
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
	// The whole transcript is streamed, not a tail: pi ends with turn_end
	// and agent_end events that repeat every message, and on a long run
	// they alone can be larger than any fixed tail.
	file, err := os.Open(outputFile)
	if err != nil {
		return nil
	}
	defer file.Close()
	var limit *UsageLimit
	switch harnessType {
	case "claude-code":
		limit = detectClaudeLimit(file)
	case "codex-cli":
		limit = detectCodexLimit(file)
	case "pi":
		limit = detectPiLimit(file)
	}
	if limit != nil {
		limit.SessionID = firstSessionID(harnessType, outputFile)
	}
	return limit
}

// maxEventLine bounds one transcript line. A longer line (pi's agent_end
// can carry every message of a long run) is skipped whole.
const maxEventLine = 64 * 1024 * 1024

// eachEvent calls visit for every JSON object line read from transcript.
// Only the "type" field is decoded up front; visit decodes what it needs.
func eachEvent(transcript io.Reader, visit func(eventType string, line []byte)) {
	reader := bufio.NewReaderSize(transcript, 64*1024)
	for {
		line, err := readEventLine(reader)
		if len(line) > 0 && line[0] == '{' {
			var header struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(line, &header) == nil {
				visit(header.Type, line)
			}
		}
		if err != nil {
			return
		}
	}
}

// readEventLine returns the next line, or nil for a line over maxEventLine
// (which is consumed and skipped).
func readEventLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > maxEventLine {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if tooLong {
			return nil, err
		}
		return bytes.TrimSpace(line), err
	}
}

// detectClaudeLimit requires the run's final result to be an error. The
// latest rate_limit_event decides whether the account was still refused:
// a rejection the run was not on overage for supplies the window and
// reset time, and any later event that lets work continue (the window
// reopened, or overage took over) clears it. The final error text alone
// is also enough when it names the limit.
func detectClaudeLimit(transcript io.Reader) *UsageLimit {
	var rejection *UsageLimit
	var final struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	sawResult := false
	eachEvent(transcript, func(eventType string, line []byte) {
		switch eventType {
		case "rate_limit_event":
			var event struct {
				Info struct {
					Status         string `json:"status"`
					ResetsAt       int64  `json:"resetsAt"`
					RateLimitType  string `json:"rateLimitType"`
					OverageStatus  string `json:"overageStatus"`
					IsUsingOverage bool   `json:"isUsingOverage"`
				} `json:"rate_limit_info"`
			}
			if json.Unmarshal(line, &event) != nil {
				return
			}
			info := event.Info
			if info.Status == "rejected" && !info.IsUsingOverage && info.OverageStatus != "allowed" {
				rejection = &UsageLimit{LimitType: info.RateLimitType, ResetsAt: unixTime(info.ResetsAt)}
			} else {
				rejection = nil
			}
		case "result":
			sawResult = json.Unmarshal(line, &final) == nil
		}
	})
	finalError := strings.TrimSpace(final.Result)
	if !sawResult || !final.IsError {
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

// detectCodexLimit requires the run's final failure to be the limit. An
// error event names the limit; the turn.failed that ends the turn is
// authoritative, so a different final failure (a sign-out) replaces it,
// and a later started or completed turn clears it.
func detectCodexLimit(transcript io.Reader) *UsageLimit {
	var limit *UsageLimit
	eachEvent(transcript, func(eventType string, line []byte) {
		switch eventType {
		case "turn.completed", "turn.started":
			limit = nil
		case "error":
			var event struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(line, &event) == nil && mentionsUsageLimit(event.Message) {
				limit = &UsageLimit{Message: strings.TrimSpace(event.Message)}
			}
		case "turn.failed":
			var event struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &event) != nil {
				return
			}
			switch message := event.Error.Message; {
			case mentionsUsageLimit(message):
				limit = &UsageLimit{Message: strings.TrimSpace(message)}
			case strings.TrimSpace(message) != "":
				limit = nil
			}
		}
	})
	return limit
}

// detectPiLimit requires the last assistant message to have ended with an
// error naming the limit.
func detectPiLimit(transcript io.Reader) *UsageLimit {
	var limit *UsageLimit
	eachEvent(transcript, func(eventType string, line []byte) {
		if eventType != "message_end" {
			return
		}
		var event struct {
			Message struct {
				Role         string `json:"role"`
				StopReason   string `json:"stopReason"`
				ErrorMessage string `json:"errorMessage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &event) != nil || event.Message.Role != "assistant" {
			return
		}
		limit = nil
		if event.Message.StopReason == "error" && mentionsUsageLimit(event.Message.ErrorMessage) {
			limit = &UsageLimit{Message: strings.TrimSpace(event.Message.ErrorMessage)}
		}
	})
	return limit
}

// firstSessionID reads the session identifier from the transcript's first
// lines.
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
