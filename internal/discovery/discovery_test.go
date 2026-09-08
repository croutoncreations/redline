package discovery_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jfox/redline/internal/discovery"
)

func TestCatalogDiscoversInstalledHarnessVersionsAndModels(t *testing.T) {
	service := discovery.Service{
		LookPath: func(name string) (string, error) { return filepath.Join("/bin", name), nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name + " " + strings.Join(args, " ") {
			case "/bin/codex --version":
				return []byte("codex-cli 0.144.6\n"), nil
			case "/bin/claude --version":
				return []byte("2.1.211 (Claude Code)\n"), nil
			case "/bin/pi --version":
				return []byte("0.80.10\n"), nil
			case "/bin/hermes --version":
				return []byte("Hermes Agent v0.18.2 (2026.7.7.2)\nInstall directory: /opt/hermes\nUpdate available\n"), nil
			case "/bin/codex login status", "/bin/claude auth status":
				return []byte("authenticated\n"), nil
			case "/bin/pi --offline --list-models openai-codex":
				return []byte(piCodexModels), nil
			case "/bin/pi --offline --list-models anthropic-cli":
				return []byte(piClaudeModels), nil
			default:
				return nil, errors.New("unexpected command")
			}
		},
		ReadFile: func(path string) ([]byte, error) {
			if path != "/state/models_cache.json" {
				return nil, errors.New("unexpected path")
			}
			return []byte(`{"fetched_at":"2026-07-20T20:19:43Z","models":[{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list"},{"slug":"hidden","display_name":"Hidden","visibility":"hide"}]}`), nil
		},
		CodexModelsPath: "/state/models_cache.json",
	}

	catalog := service.Discover(t.Context())
	if len(catalog.Harnesses) != 5 {
		t.Fatalf("harnesses = %#v", catalog.Harnesses)
	}
	assertHarness(t, catalog, "codex-cli", true, "0.144.6", "codex", "gpt-5.5")
	assertHarness(t, catalog, "claude-code", true, "2.1.211", "claude", "claude-opus-4-8")
	assertHarness(t, catalog, "pi", true, "0.80.10", "codex", "openai-codex/gpt-5.6-sol")
	assertHarness(t, catalog, "pi", true, "0.80.10", "claude", "anthropic-cli/claude-fable-5")
	hermesHarness := findHarness(t, catalog, "hermes")
	if !hermesHarness.Installed || hermesHarness.Version != "0.18.2" {
		t.Fatalf("hermes = %#v", hermesHarness)
	}
	if findHarness(t, catalog, "codex-cli").Authentication != "authenticated" ||
		findHarness(t, catalog, "claude-code").Authentication != "authenticated" {
		t.Fatalf("direct harness authentication was not detected: %#v", catalog.Harnesses)
	}
}

func TestCatalogReportsDirectHarnessAuthenticationFailureWithoutHidingInstallation(t *testing.T) {
	signedOutErr := exec.Command("sh", "-c", "exit 1").Run()
	service := discovery.Service{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if len(args) == 1 && args[0] == "--version" {
				return []byte("1.2.3"), nil
			}
			if name == "/bin/codex" && strings.Join(args, " ") == "login status" {
				return []byte("Not logged in"), signedOutErr
			}
			if name == "/bin/claude" && strings.Join(args, " ") == "auth status" {
				return []byte("authenticated"), nil
			}
			return nil, errors.New("not available")
		},
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("missing") },
	}
	catalog := service.Discover(t.Context())
	codex := findHarness(t, catalog, "codex-cli")
	if !codex.Installed || codex.Authentication != "signed_out" {
		t.Fatalf("codex = %#v", codex)
	}
	claude := findHarness(t, catalog, "claude-code")
	if claude.Authentication != "authenticated" {
		t.Fatalf("claude = %#v", claude)
	}
}

func TestCatalogLeavesAuthenticationUnknownAfterOperationalFailure(t *testing.T) {
	service := discovery.Service{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) == 1 && args[0] == "--version" {
				return []byte("1.2.3"), nil
			}
			return nil, context.DeadlineExceeded
		},
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("missing") },
	}
	catalog := service.Discover(t.Context())
	if authentication := findHarness(t, catalog, "codex-cli").Authentication; authentication != "" {
		t.Fatalf("codex authentication = %q, want unknown", authentication)
	}
	if authentication := findHarness(t, catalog, "claude-code").Authentication; authentication != "" {
		t.Fatalf("claude authentication = %q, want unknown", authentication)
	}
}

func TestCatalogToleratesMissingHarnessesAndDiscoveryFailures(t *testing.T) {
	service := discovery.Service{
		LookPath: func(name string) (string, error) {
			if name == "pi" {
				return "", errors.New("not found")
			}
			return "/bin/" + name, nil
		},
		Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "/bin/claude" {
				return nil, errors.New("broken")
			}
			return []byte("codex-cli 1.0.0"), nil
		},
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("missing") },
	}
	catalog := service.Discover(t.Context())
	pi := findHarness(t, catalog, "pi")
	if pi.Installed || pi.Error == "" {
		t.Fatalf("pi = %#v", pi)
	}
	claude := findHarness(t, catalog, "claude-code")
	if !claude.Installed || claude.Error == "" {
		t.Fatalf("claude = %#v", claude)
	}
}

func TestParsePiModelsFiltersExactProviderAndKeepsVersionMetadata(t *testing.T) {
	models := discovery.ParsePiModels(piCodexModels, "openai-codex", true)
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	if models[0].ID != "openai-codex/gpt-5.4" || models[0].ContextWindow != "272K" || !models[0].Thinking || !models[0].Images {
		t.Fatalf("first = %#v", models[0])
	}
}

func TestCatalogPrefersConfiguredPiModelFileWithoutLaunchingModelDiscovery(t *testing.T) {
	service := discovery.Service{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if len(args) == 1 && args[0] == "--version" {
				return []byte("1.2.3"), nil
			}
			return nil, errors.New("model command should not run")
		},
		ReadFile: func(path string) ([]byte, error) {
			switch path {
			case "/state/pi-models.json":
				return []byte(piConfiguredModels), nil
			case "/state/codex-models.json":
				return []byte(`{"models":[]}`), nil
			default:
				return nil, errors.New("unexpected path")
			}
		},
		PiModelsPath: "/state/pi-models.json", CodexModelsPath: "/state/codex-models.json",
	}
	catalog := service.Discover(t.Context())
	assertHarness(t, catalog, "pi", true, "1.2.3", "claude", "anthropic-cli/claude-opus-4-8")
	assertHarness(t, catalog, "claude-code", true, "1.2.3", "claude", "claude-opus-4-8")
	pi := findHarness(t, catalog, "pi")
	model := pi.Models["codex"][0]
	if model.ContextWindow != "1M" || model.MaxOutput != "128K" || !model.Thinking || !model.Images {
		t.Fatalf("model = %#v", model)
	}
}

func assertHarness(t *testing.T, catalog discovery.Catalog, id string, installed bool, version, provider, model string) {
	t.Helper()
	harness := findHarness(t, catalog, id)
	if harness.Installed != installed || harness.Version != version {
		t.Fatalf("%s = %#v", id, harness)
	}
	for _, got := range harness.Models[provider] {
		if got.ID == model {
			return
		}
	}
	t.Fatalf("%s model %q not found in %#v", id, model, harness.Models[provider])
}

func findHarness(t *testing.T, catalog discovery.Catalog, id string) discovery.Harness {
	t.Helper()
	for _, harness := range catalog.Harnesses {
		if harness.ID == id {
			return harness
		}
	}
	t.Fatalf("harness %q not found", id)
	return discovery.Harness{}
}

const piCodexModels = `provider      model          context max-out thinking images
openai        gpt-5.4        272K    128K    yes      yes
openai-codex  gpt-5.4        272K    128K    yes      yes
openai-codex  gpt-5.6-sol    1M      128K    yes      yes
`

const piClaudeModels = `provider       model                         context max-out thinking images
anthropic-cli  claude-fable-5                200K    64K     yes      yes
anthropic-cli  claude-opus-4-8               200K    64K     yes      yes
`

const piConfiguredModels = `{"providers":{"openai-codex":{"models":[{"id":"gpt-5.6-sol","name":"GPT-5.6 Sol","contextWindow":1000000,"maxTokens":128000,"reasoning":true,"input":["text","image"]}]},"anthropic-cli":{"models":[{"id":"claude-opus-4-8","name":"Claude Opus 4.8","contextWindow":200000,"maxTokens":32000,"reasoning":false,"input":["text","image"]}]}}}`
