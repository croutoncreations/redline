package harness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

// UsageLimit reports that a harness stopped because the provider account
// reached its usage limit, as opposed to the task itself failing.
type UsageLimit struct {
	// ResetsAt is when the provider said the limit resets; zero when the
	// harness did not say.
	ResetsAt time.Time `json:"resets_at,omitzero"`
	// LimitType is the provider's name for the limit, e.g. five_hour.
	LimitType string `json:"limit_type,omitempty"`
}

// ProfileCanResume reports whether runs of this profile leave a session that
// Redline can continue: only Claude Code is resumed, and not when the
// profile has turned session saving off.
func ProfileCanResume(profile domain.ExecutionProfile) bool {
	return profile.HarnessType == "claude-code" &&
		!slices.Contains(profile.HarnessArgs, "--no-session-persistence")
}

// claudeResumeRejected reports Claude Code's own refusal to reopen a session
// passed to --resume (checked against 2.1.318), e.g. because the session
// file is gone or belongs to another config directory.
func claudeResumeRejected(paths ...string) bool {
	for _, path := range paths {
		data, err := readTail(path, 64*1024)
		if err != nil {
			continue
		}
		text := string(data)
		if strings.Contains(text, "No conversation found with session ID") ||
			strings.Contains(text, "--resume session load failed") {
			return true
		}
	}
	return false
}

// ResumePrompt is sent to the continued session in place of the task prompt,
// which the session already holds.
const ResumePrompt = "Your previous turn in this session stopped because the account reached its usage limit. " +
	"The limit has now reset. Continue the original task from where you left off. " +
	"Check the workspace for work already done rather than repeating it."

const (
	sessionHeadBytes = 64 * 1024
	limitTailBytes   = 256 * 1024
)

// claudeSessionID reads Claude Code's session id from its stream-json
// output, where every event carries session_id.
func claudeSessionID(path string) string {
	data, err := readHead(path, sessionHeadBytes)
	if err != nil {
		return ""
	}
	var found string
	eachJSONLine(data, func(line map[string]json.RawMessage) bool {
		found = jsonString(line["session_id"])
		return found == ""
	})
	return found
}

type rateLimitInfo struct {
	Status        string `json:"status"`
	ResetsAt      int64  `json:"resetsAt"`
	RateLimitType string `json:"rateLimitType"`
}

// claudeUsageLimit finds Claude Code's own report that the account's usage
// limit refused the request that ended the run. Only top-level stream events
// are read, never message content, so tool output that mentions a limit
// cannot trigger it.
//
// Claude Code (checked against 2.1.318) reports a refused request as an
// assistant event with error "rate_limit"; when the account's plan limit was
// the cause it adds api_error "usage_limit_reached" with the limit's
// rate_limit_info. A rate_limit_event whose status is "rejected" says the
// same thing. A later ordinary assistant turn means Claude Code got past the
// refusal (for example on another model), so the run did not stop there.
func claudeUsageLimit(path string) *UsageLimit {
	data, err := readTail(path, limitTailBytes)
	if err != nil {
		return nil
	}
	var limit *UsageLimit
	eachJSONLine(data, func(line map[string]json.RawMessage) bool {
		switch jsonString(line["type"]) {
		case "rate_limit_event":
			var info rateLimitInfo
			if json.Unmarshal(line["rate_limit_info"], &info) == nil && info.Status == "rejected" {
				limit = mergeLimit(limit, info)
			}
		case "assistant":
			if jsonString(line["api_error"]) != "usage_limit_reached" {
				if jsonString(line["error"]) == "" {
					limit = nil
				}
				return true
			}
			var params struct {
				RateLimitInfo rateLimitInfo `json:"rate_limit_info"`
			}
			_ = json.Unmarshal(line["api_error_params"], &params)
			limit = mergeLimit(limit, params.RateLimitInfo)
		}
		return true
	})
	return limit
}

func mergeLimit(current *UsageLimit, info rateLimitInfo) *UsageLimit {
	if current == nil {
		current = &UsageLimit{}
	}
	if info.ResetsAt > 0 {
		current.ResetsAt = time.Unix(info.ResetsAt, 0).UTC()
	}
	if info.RateLimitType != "" {
		current.LimitType = info.RateLimitType
	}
	return current
}

// eachJSONLine calls fn for every line that is a JSON object until fn
// returns false. A partial first line from a tail read is skipped.
func eachJSONLine(data []byte, fn func(map[string]json.RawMessage) bool) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), len(data)+1)
	for scanner.Scan() {
		var line map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if !fn(line) {
			return
		}
	}
}

func jsonString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func readHead(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}
