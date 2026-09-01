package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/personality"
)

func TestRunAcceptsVersionedFixtureAndWritesReport(t *testing.T) {
	root := repositoryRoot(t)
	reportPath := filepath.Join(t.TempDir(), "report.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-input", filepath.Join(root, "docs", "dogfood", "personality-suite.fixture.json"),
		"-report", reportPath,
	}, &stdout, &stderr, func() time.Time { return time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC) })
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("run code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"passed": true`) || !strings.Contains(stdout.String(), `"evaluated_at": "2026-08-31T10:00:00Z"`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
	written, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != stdout.String() {
		t.Fatal("report file differs from stdout")
	}
}

func TestRunRejectsUnknownJSONFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, []byte(`{"format":"yuri.personality-dogfood-suite","version":1,"contracts":[],"runs":[],"credential":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-input", path}, &stdout, &stderr, time.Now); code != 2 || !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunReturnsOneForBehavioralFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	data := `{"format":"yuri.personality-dogfood-suite","version":1,"contracts":[{"profile":"one","signal_groups":[["x"]]},{"profile":"two","signal_groups":[["y"]]}],"runs":[]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-input", path}, &stdout, &stderr, time.Now); code != 1 || !strings.Contains(stdout.String(), `"passed": false`) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunValidatesLiveCaptureFlagsBeforeProviderAccess(t *testing.T) {
	for _, args := range [][]string{
		{"-live-codex"},
		{"-live-codex", "-suite", "out.json", "-input", "in.json"},
		{"-live-codex", "-suite", "out.json", "-provider-id", "openrouter"},
		{"-live-openai-compatible", "-suite", "out.json"},
		{"-live-openai-compatible", "-suite", "out.json", "-provider-id", "openrouter", "-input", "in.json"},
		{"-live-codex", "-live-openai-compatible", "-suite", "out.json", "-provider-id", "openrouter"},
		{"-live-openrouter", "-suite", "out.json"},
		{"-input", "in.json", "-model", "model"},
		{"-input", "in.json", "-resume"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, time.Now); code != 2 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestCloneOpenAICompatibleConfigUsesOnlySelectedRoute(t *testing.T) {
	owner := config.Config{
		Version:       1,
		Locale:        "ru-RU",
		LogLevel:      "info",
		DataDirectory: filepath.Join(t.TempDir(), "owner-data"),
		AllowedDirectories: []string{
			filepath.Join(t.TempDir(), "private-documents"),
		},
		Providers: []config.ProviderConfig{
			{
				ID: "unused", Kind: config.ProviderOpenAICompatible, DisplayName: "Unused", BaseURL: "https://unused.example/v1",
				Model: "unused-model", CredentialRef: "provider.unused.api-key", Enabled: true,
			},
			{
				ID: "openrouter", Kind: config.ProviderOpenAICompatible, DisplayName: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1",
				Model: "openrouter/free", APIStyle: config.ProviderAPIStyleChatCompletions, FavoriteModels: []string{"openrouter/free"},
				CredentialRef: "provider.openrouter.api-key", Enabled: false,
			},
		},
	}
	isolated := config.Paths{DataDirectory: filepath.Join(t.TempDir(), "isolated-data")}
	clone, err := cloneOpenAICompatibleConfig(owner, isolated, "openrouter", "openrouter/auto")
	if err != nil {
		t.Fatalf("cloneOpenAICompatibleConfig() error = %v", err)
	}
	if clone.DataDirectory != isolated.DataDirectory {
		t.Fatalf("clone data directory = %q, want %q", clone.DataDirectory, isolated.DataDirectory)
	}
	if len(clone.Providers) != 1 {
		t.Fatalf("clone providers = %#v, want only selected provider", clone.Providers)
	}
	provider := clone.Providers[0]
	if provider.ID != "openrouter" || provider.Model != "openrouter/auto" || !provider.Enabled {
		t.Fatalf("cloned route = %#v", provider)
	}
	if provider.CredentialRef != "provider.openrouter.api-key" {
		t.Fatalf("credential reference = %q, want opaque owner reference", provider.CredentialRef)
	}
	if len(clone.AllowedDirectories) != 0 || clone.WebSearch.Enabled || clone.PluginDevMode {
		t.Fatalf("clone retained owner-local access/configuration: %#v", clone)
	}
	if clone.Persona.AutoEvolution {
		t.Fatal("isolated capture must disable post-turn evolution")
	}
	encoded, err := json.Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "api_key") || strings.Contains(string(encoded), "sk-owner-secret") {
		t.Fatalf("clone serialized secret-like material: %s", encoded)
	}
}

func TestCloneOpenAICompatibleConfigRejectsMissingRouteData(t *testing.T) {
	paths := config.Paths{DataDirectory: filepath.Join(t.TempDir(), "isolated-data")}
	owner := config.Default(paths)
	owner.Providers = []config.ProviderConfig{{
		ID: "codex", Kind: config.ProviderCodexAppServer, DisplayName: "Codex", Enabled: true,
	}}
	for _, test := range []struct {
		name       string
		providerID string
		model      string
	}{
		{name: "unknown provider", providerID: "missing", model: "model"},
		{name: "wrong provider kind", providerID: "codex", model: "model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := cloneOpenAICompatibleConfig(owner, paths, test.providerID, test.model); err == nil {
				t.Fatal("cloneOpenAICompatibleConfig() accepted invalid route")
			}
		})
	}
}

func TestDogfoodSuiteHasSamples(t *testing.T) {
	if dogfoodSuiteHasSamples(personality.DogfoodSuite{}) {
		t.Fatal("empty suite reported samples")
	}
	if !dogfoodSuiteHasSamples(personality.DogfoodSuite{Runs: []personality.DogfoodRun{{Samples: []personality.DogfoodSample{{Response: "ok"}}}}}) {
		t.Fatal("suite sample was not detected")
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
