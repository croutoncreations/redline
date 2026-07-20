package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Model struct {
	ID            string `json:"id"`
	Label         string `json:"label,omitempty"`
	Source        string `json:"source"`
	ContextWindow string `json:"context_window,omitempty"`
	MaxOutput     string `json:"max_output,omitempty"`
	Thinking      bool   `json:"thinking,omitempty"`
	Images        bool   `json:"images,omitempty"`
}

type Harness struct {
	ID        string             `json:"id"`
	Label     string             `json:"label"`
	Installed bool               `json:"installed"`
	Version   string             `json:"version,omitempty"`
	Path      string             `json:"-"`
	Error     string             `json:"error,omitempty"`
	Models    map[string][]Model `json:"models,omitempty"`
}

type Catalog struct {
	GeneratedAt time.Time `json:"generated_at"`
	Harnesses   []Harness `json:"harnesses"`
}

type Service struct {
	LookPath        func(string) (string, error)
	Run             func(context.Context, string, ...string) ([]byte, error)
	ReadFile        func(string) ([]byte, error)
	CodexModelsPath string
	PiModelsPath    string
	Now             func() time.Time
}

func (s Service) Discover(ctx context.Context) Catalog {
	specs := []struct{ id, label, binary string }{{"codex-cli", "Codex CLI", "codex"}, {"claude-code", "Claude Code", "claude"}, {"pi", "Pi", "pi"}}
	catalog := Catalog{GeneratedAt: s.now(), Harnesses: make([]Harness, len(specs)+1)}
	var versions sync.WaitGroup
	for index, spec := range specs {
		versions.Add(1)
		go func() {
			defer versions.Done()
			catalog.Harnesses[index] = s.inspect(ctx, spec.id, spec.label, spec.binary, []string{"--version"})
		}()
	}
	versions.Wait()
	catalog.Harnesses[len(specs)] = Harness{ID: "command", Label: "Custom command", Installed: true}
	var piCodexModels, piClaudeModels []Model
	if pi := catalogHarness(catalog, "pi"); pi != nil && pi.Installed {
		piCodexModels = s.configuredPiModels("openai-codex", true)
		piClaudeModels = s.configuredPiModels("anthropic-cli", true)
		if len(piCodexModels) == 0 {
			if output, err := s.run()(ctx, pi.Path, "--offline", "--list-models", "openai-codex"); err == nil {
				piCodexModels = ParsePiModels(string(output), "openai-codex", true)
			}
		}
		if len(piClaudeModels) == 0 {
			if output, err := s.run()(ctx, pi.Path, "--offline", "--list-models", "anthropic-cli"); err == nil {
				piClaudeModels = ParsePiModels(string(output), "anthropic-cli", true)
			}
		}
	}

	for index := range catalog.Harnesses {
		harness := &catalog.Harnesses[index]
		switch harness.ID {
		case "codex-cli":
			harness.Models = map[string][]Model{"codex": s.codexModels()}
		case "pi":
			if harness.Installed {
				harness.Models = map[string][]Model{
					"codex":  piCodexModels,
					"claude": piClaudeModels,
				}
			}
		case "claude-code":
			if len(piClaudeModels) > 0 {
				models := make([]Model, len(piClaudeModels))
				for index, model := range piClaudeModels {
					model.ID = strings.TrimPrefix(model.ID, "anthropic-cli/")
					models[index] = model
				}
				harness.Models = map[string][]Model{"claude": models}
			}
		}
	}
	return catalog
}

func (s Service) inspect(ctx context.Context, id, label, binary string, args []string) Harness {
	harness := Harness{ID: id, Label: label}
	path, err := s.lookPath()(binary)
	if err != nil {
		harness.Error = fmt.Sprintf("%s is not installed or not on PATH", binary)
		return harness
	}
	harness.Installed, harness.Path = true, path
	output, err := s.run()(ctx, path, args...)
	if err != nil {
		harness.Error = fmt.Sprintf("read version: %v", err)
		return harness
	}
	harness.Version = cleanVersion(string(output))
	return harness
}

func ParsePiModels(output, provider string, qualify bool) []Model {
	models := []Model{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(stripANSI(line))
		if len(fields) < 6 || fields[0] != provider {
			continue
		}
		id := fields[1]
		if qualify {
			id = provider + "/" + id
		}
		models = append(models, Model{ID: id, Label: fields[1], Source: "pi_catalog", ContextWindow: fields[2], MaxOutput: fields[3], Thinking: fields[4] == "yes", Images: fields[5] == "yes"})
	}
	return models
}

func (s Service) configuredPiModels(provider string, qualify bool) []Model {
	path := s.PiModelsPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(home, ".pi", "agent", "models.json")
	}
	data, err := s.readFile()(path)
	if err != nil {
		return nil
	}
	var configured struct {
		Providers map[string]struct {
			Models []struct {
				ID, Name                 string
				ContextWindow, MaxTokens int
				Reasoning                bool
				Input                    []string
			} `json:"models"`
		} `json:"providers"`
	}
	if json.Unmarshal(data, &configured) != nil {
		return nil
	}
	models := []Model{}
	for _, configuredModel := range configured.Providers[provider].Models {
		id := configuredModel.ID
		if id == "" {
			continue
		}
		if qualify {
			id = provider + "/" + id
		}
		models = append(models, Model{
			ID: id, Label: configuredModel.Name, Source: "pi_config",
			ContextWindow: formatTokens(configuredModel.ContextWindow), MaxOutput: formatTokens(configuredModel.MaxTokens),
			Thinking: configuredModel.Reasoning, Images: contains(configuredModel.Input, "image"),
		})
	}
	return models
}

func formatTokens(tokens int) string {
	if tokens <= 0 {
		return ""
	}
	if tokens%1_000_000 == 0 {
		return fmt.Sprintf("%dM", tokens/1_000_000)
	}
	if tokens%1_000 == 0 {
		return fmt.Sprintf("%dK", tokens/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s Service) codexModels() []Model {
	path := s.CodexModelsPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(home, ".codex", "models_cache.json")
	}
	data, err := s.readFile()(path)
	if err != nil {
		return nil
	}
	var cache struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return nil
	}
	models := []Model{}
	for _, model := range cache.Models {
		if model.Slug == "" || strings.EqualFold(model.Visibility, "hide") {
			continue
		}
		models = append(models, Model{ID: model.Slug, Label: model.DisplayName, Source: "codex_cache"})
	}
	return models
}

func catalogHarness(catalog Catalog, id string) *Harness {
	for index := range catalog.Harnesses {
		if catalog.Harnesses[index].ID == id {
			return &catalog.Harnesses[index]
		}
	}
	return nil
}

func cleanVersion(output string) string {
	value := strings.TrimSpace(output)
	value = strings.TrimPrefix(value, "codex-cli ")
	value = strings.TrimSuffix(value, " (Claude Code)")
	return strings.TrimSpace(value)
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripANSI(value string) string { return ansiPattern.ReplaceAllString(value, "") }
func (s Service) lookPath() func(string) (string, error) {
	if s.LookPath != nil {
		return s.LookPath
	}
	return exec.LookPath
}
func (s Service) readFile() func(string) ([]byte, error) {
	if s.ReadFile != nil {
		return s.ReadFile
	}
	return os.ReadFile
}
func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}
func (s Service) run() func(context.Context, string, ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		command := exec.CommandContext(commandCtx, name, args...)
		command.Env = append(os.Environ(), "GATEPOST_HOST_DISABLE=1")
		return command.CombinedOutput()
	}
}
