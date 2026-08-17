package activity

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"

	"github.com/jfox/redline/internal/domain"
)

const maxOutputBytes = 512 * 1024

var urlPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

type Input struct {
	State      domain.RunState
	Error      string
	OutputFile string
	ResultFile string
	Workspace  domain.Workspace
	Metadata   map[string]any
	Provider   string
	Model      string
}

type Result struct {
	Summary        string
	Outcome        string
	Artifacts      []domain.RunArtifact
	Warnings       []string
	ActualProvider string
	ActualModel    string
}

type manifest struct {
	Summary        string               `json:"summary"`
	Outcome        string               `json:"outcome"`
	Artifacts      []domain.RunArtifact `json:"artifacts"`
	Warnings       []string             `json:"warnings"`
	ActualProvider string               `json:"actual_provider"`
	ActualModel    string               `json:"actual_model"`
}

// Build creates a small durable description of a run. A structured result manifest
// is authoritative when present; otherwise common JSONL harness output is summarized.
func Build(input Input) Result {
	result := Result{}
	if input.ResultFile != "" {
		if data, err := readTail(input.ResultFile, maxOutputBytes); err == nil {
			var value manifest
			if json.Unmarshal(data, &value) == nil {
				result = Result{
					Summary: value.Summary, Outcome: value.Outcome, Artifacts: value.Artifacts,
					Warnings: value.Warnings, ActualProvider: value.ActualProvider, ActualModel: value.ActualModel,
				}
			}
		}
	}
	if result.Summary == "" {
		result.Summary = outputSummary(input.OutputFile)
	}
	if result.ActualProvider == "" {
		result.ActualProvider, _ = input.Metadata["actual_provider"].(string)
		if result.ActualProvider == "" {
			result.ActualProvider = input.Provider
		}
	}
	if result.ActualModel == "" {
		result.ActualModel, _ = input.Metadata["actual_model"].(string)
		if result.ActualModel == "" && input.Model != "default" {
			result.ActualModel = input.Model
		}
	}
	if input.State == domain.RunFailed {
		result.Outcome = "failed"
		if result.Summary == "" {
			result.Summary = input.Error
		}
		if input.Error != "" {
			result.Warnings = appendUnique(result.Warnings, input.Error)
		}
	} else {
		if result.Outcome == "" {
			result.Outcome = "completed"
		}
		if result.Summary == "" {
			result.Summary = "Run completed successfully."
		}
	}
	result.Artifacts = append(result.Artifacts, links(result.Summary)...)
	if input.Workspace.Directory != "" {
		result.Artifacts = appendUniqueArtifact(result.Artifacts, domain.RunArtifact{
			Type: "workspace", Label: "Workspace", Path: input.Workspace.Directory,
		})
	}
	if input.Workspace.Branch != "" {
		result.Artifacts = appendUniqueArtifact(result.Artifacts, domain.RunArtifact{
			Type: "branch", Label: input.Workspace.Branch,
		})
	}
	return result
}

func outputSummary(path string) string {
	data, err := readTail(path, maxOutputBytes)
	if err != nil {
		return ""
	}
	var last string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 64*1024), maxOutputBytes+1)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var value any
		if json.Unmarshal([]byte(line), &value) == nil {
			if text := summaryFromJSON(value); text != "" {
				last = text
			}
		} else {
			last = line
		}
	}
	return strings.TrimSpace(last)
}

func summaryFromJSON(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"result", "output", "summary"} {
		if text, ok := object[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	if item, ok := object["item"]; ok {
		if text := summaryFromJSON(item); text != "" {
			return text
		}
	}
	if message, ok := object["message"]; ok {
		if text := summaryFromJSON(message); text != "" {
			return text
		}
	}
	for _, key := range []string{"text", "content"} {
		switch value := object[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		case []any:
			for index := len(value) - 1; index >= 0; index-- {
				if text := summaryFromJSON(value[index]); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func links(summary string) []domain.RunArtifact {
	var result []domain.RunArtifact
	for _, raw := range urlPattern.FindAllString(summary, -1) {
		url := strings.TrimRight(raw, ".,;:!?)]}")
		artifactType, label := "link", "Link"
		switch {
		case strings.Contains(url, "github.com/") && strings.Contains(url, "/pull/"):
			artifactType, label = "pull_request", "Pull request"
		case strings.Contains(url, "github.com/") && strings.Contains(url, "/issues/"):
			artifactType, label = "issue", "Issue"
		case strings.Contains(url, "/artifacts/"):
			artifactType, label = "report", "Report"
		}
		result = appendUniqueArtifact(result, domain.RunArtifact{Type: artifactType, Label: label, URL: url})
	}
	return result
}

func readTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := max(int64(0), info.Size()-limit)
	data := make([]byte, info.Size()-offset)
	_, err = file.ReadAt(data, offset)
	if err != nil && len(data) == 0 {
		return nil, err
	}
	return data, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueArtifact(values []domain.RunArtifact, value domain.RunArtifact) []domain.RunArtifact {
	for _, existing := range values {
		if value.URL != "" && existing.URL == value.URL {
			return values
		}
		if value.Path != "" && existing.Path == value.Path {
			return values
		}
		if existing.Type == value.Type && existing.Label == value.Label &&
			existing.URL == value.URL && existing.Path == value.Path {
			return values
		}
	}
	return append(values, value)
}
