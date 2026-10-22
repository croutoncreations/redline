package greenlane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// AutomatedReviewComment is GitHub-owned issue-comment evidence, not PR text.
// Both the publishing App and immutable bot user ID must match.
type AutomatedReviewComment struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	User         string `json:"user"`
	UserType     string `json:"user_type"`
	AppID        int64  `json:"app_id"`
	Body         string `json:"body"`
	UpdatedAt    string `json:"updated_at"`
	EditorID     int64  `json:"editor_id"`
	EditorUser   string `json:"editor_user"`
	EditorType   string `json:"editor_type"`
	LastEditedAt string `json:"last_edited_at"`
}

func TrustedAutomatedSummary(c AutomatedReviewComment) bool {
	if c.ID <= 0 || c.UserType != "Bot" {
		return false
	}
	switch {
	case c.AppID == 347564 && c.UserID == 136622811 && c.User == "coderabbitai[bot]":
		return strings.HasPrefix(c.Body, "<!-- coderabbit-ai-generated:v1 -->") && strings.Contains(c.Body, "\n<!-- This is an auto-generated comment: summarize by coderabbit.ai -->")
	case c.AppID == 1144995 && c.UserID == 199175422 && c.User == "chatgpt-codex-connector[bot]":
		return strings.HasPrefix(c.Body, "<!-- codex-pull-request-review-summary -->")
	}
	return false
}

var fullHead = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Only standalone machine markers outside Markdown code/quotes are protocol.
// Multiple markers are ambiguous, so they fail closed.
func summaryPayload(body, prefix string) []byte {
	var payload []byte
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, " -->") {
			continue
		}
		if payload != nil {
			return nil
		}
		payload = []byte(strings.TrimSuffix(strings.TrimPrefix(line, prefix), " -->"))
	}
	return payload
}

func completedAutomatedReview(pr PRInfo) bool {
	if !fullHead.MatchString(pr.HeadSHA) {
		return false
	}
	latest := map[string]AutomatedReviewComment{}
	invalid := map[string]bool{}
	for _, c := range pr.AutomatedReviews {
		if !TrustedAutomatedSummary(c) {
			continue
		}
		stamp, err := time.Parse(time.RFC3339, c.UpdatedAt)
		if err != nil || stamp.After(time.Now().UTC().Add(time.Minute)) {
			invalid[c.User] = true
			continue
		}
		old, ok := latest[c.User]
		oldStamp, _ := time.Parse(time.RFC3339, old.UpdatedAt)
		if !ok || stamp.After(oldStamp) || (stamp.Equal(oldStamp) && c.ID > old.ID) {
			latest[c.User] = c
		}
	}
	for _, c := range latest {
		if invalid[c.User] || strings.EqualFold(c.User, pr.Author) || !authenticSummaryEditor(c) {
			continue
		}
		if c.AppID == 347564 {
			if strings.Contains(c.Body, "<!-- This is an auto-generated comment: review in progress by coderabbit.ai -->") || strings.Contains(c.Body, "<!-- This is an auto-generated comment: rate limited by coderabbit.ai -->") || strings.Contains(c.Body, "<!-- This is an auto-generated comment: skip review by coderabbit.ai -->") {
				continue
			}
			var state struct {
				Source  string `json:"sourceCommitId"`
				Covered string `json:"coveredCommitId"`
				Kind    string `json:"kind"`
			}
			if summaryJSON(summaryPayload(c.Body, "<!-- final_review_risk_coverage:"), &state) == nil && state.Source == pr.HeadSHA && state.Covered == pr.HeadSHA && state.Kind == "reviewed" {
				return true
			}
		} else {
			var state struct {
				Code     json.RawMessage `json:"code_review"`
				Security json.RawMessage `json:"security_review"`
			}
			if summaryJSON(summaryPayload(c.Body, "<!-- codex-review-state:v1 "), &state) == nil && completedCodexComponent(state.Code, pr.HeadSHA) &&
				(len(state.Security) == 0 || bytes.Equal(bytes.TrimSpace(state.Security), []byte("null")) || completedCodexComponent(state.Security, pr.HeadSHA)) {
				return true
			}
		}
	}
	return false
}

// Both review types use the same exact-head completion protocol. A populated
// security review must finish and clear findings even without inline threads.
func completedCodexComponent(raw json.RawMessage, head string) bool {
	var review struct {
		Commit     string `json:"commit_sha"`
		Status     string `json:"status"`
		Findings   *bool  `json:"has_any_findings"`
		Unresolved *bool  `json:"has_unresolved_findings"`
	}
	return summaryJSON(raw, &review) == nil && review.Commit == head && review.Status == "completed" && review.Findings != nil && review.Unresolved != nil && !*review.Unresolved
}

// Original author/App attribution survives edits by repository writers.
func authenticSummaryEditor(c AutomatedReviewComment) bool {
	if c.LastEditedAt == "" {
		return c.EditorID == 0 && c.EditorUser == "" && c.EditorType == ""
	}
	edited, err := time.Parse(time.RFC3339, c.LastEditedAt)
	updated, updateErr := time.Parse(time.RFC3339, c.UpdatedAt)
	return err == nil && updateErr == nil && !edited.After(updated) && c.EditorID == c.UserID && c.EditorType == "Bot" && c.EditorUser == strings.TrimSuffix(c.User, "[bot]")
}

// encoding/json accepts duplicate keys; completion protocols must be unambiguous.
func summaryJSON(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim == '{' {
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate summary key")
				}
				seen[name] = true
				if err := value(); err != nil {
					return err
				}
			}
		} else if delim == '[' {
			for d.More() {
				if err := value(); err != nil {
					return err
				}
			}
		} else {
			return fmt.Errorf("unexpected summary delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	return strictJSON(data, target)
}
