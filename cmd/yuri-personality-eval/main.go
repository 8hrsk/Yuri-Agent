package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/desktop"
	"github.com/OrdoAI/yuri-agent/internal/personality"
)

const maxSuiteBytes = 8 << 20

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}

func run(args []string, stdout, stderr io.Writer, now func() time.Time) int {
	flags := flag.NewFlagSet("yuri-personality-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "", "path to a yuri.personality-dogfood-suite JSON file")
	reportPath := flags.String("report", "", "optional path for the JSON report; stdout is always printed")
	liveCodex := flags.Bool("live-codex", false, "capture the full matrix from an isolated Codex-backed Yuri profile")
	liveOpenAI := flags.Bool("live-openai-compatible", false, "capture the full matrix from an isolated OpenAI-compatible profile")
	// Keep the provider-specific spelling convenient for the documented
	// OpenRouter workflow while using the same OpenAI-compatible capture path.
	liveOpenRouter := flags.Bool("live-openrouter", false, "alias for -live-openai-compatible")
	resume := flags.Bool("resume", false, "resume a live capture from the compatible checkpoint at -suite")
	providerID := flags.String("provider-id", "", "existing OpenAI-compatible provider ID (required with -live-openai-compatible)")
	suitePath := flags.String("suite", "", "path for a live captured suite (required with a live capture flag)")
	model := flags.String("model", "", "optional live-capture model override; empty uses the configured provider model")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *liveOpenRouter {
		*liveOpenAI = true
	}
	liveCaptures := 0
	if *liveCodex {
		liveCaptures++
	}
	if *liveOpenAI {
		liveCaptures++
	}
	if liveCaptures > 1 {
		fmt.Fprintln(stderr, "-live-codex and -live-openai-compatible are mutually exclusive")
		return 2
	}
	if liveCaptures > 0 && *inputPath != "" {
		fmt.Fprintln(stderr, "-input and live capture flags are mutually exclusive")
		return 2
	}
	if liveCaptures == 0 && *inputPath == "" {
		fmt.Fprintln(stderr, "-input is required")
		return 2
	}
	if liveCaptures == 0 && (*suitePath != "" || strings.TrimSpace(*providerID) != "" || strings.TrimSpace(*model) != "") {
		fmt.Fprintln(stderr, "-suite, -provider-id, and -model require a live capture flag")
		return 2
	}
	if *resume && liveCaptures == 0 {
		fmt.Fprintln(stderr, "-resume requires a live capture flag")
		return 2
	}
	if *liveCodex && strings.TrimSpace(*providerID) != "" {
		fmt.Fprintln(stderr, "-provider-id requires -live-openai-compatible")
		return 2
	}
	if *liveOpenAI && strings.TrimSpace(*providerID) == "" {
		fmt.Fprintln(stderr, "-provider-id is required with -live-openai-compatible")
		return 2
	}
	if liveCaptures > 0 && *suitePath == "" {
		fmt.Fprintln(stderr, "-suite is required with a live capture flag")
		return 2
	}

	var suite personality.DogfoodSuite
	var err error
	var checkpoint personality.DogfoodSuite
	if *resume {
		checkpoint, err = readSuite(*suitePath)
		if err != nil {
			fmt.Fprintf(stderr, "read resume checkpoint: %v\n", err)
			return 2
		}
	}
	if *liveCodex {
		suite, err = captureCodexSuite(strings.TrimSpace(*model), checkpoint, stderr)
		if dogfoodSuiteHasSamples(suite) {
			if checkpointErr := writeJSONFile(*suitePath, suite); checkpointErr != nil {
				err = errors.Join(err, checkpointErr)
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "capture live suite: %v", err)
			if dogfoodSuiteHasSamples(suite) {
				fmt.Fprintf(stderr, " (partial suite saved to %s)", *suitePath)
			}
			fmt.Fprintln(stderr)
			return 2
		}
	} else if *liveOpenAI {
		suite, err = captureOpenAICompatibleSuite(strings.TrimSpace(*providerID), strings.TrimSpace(*model), checkpoint, stderr)
		if dogfoodSuiteHasSamples(suite) {
			if checkpointErr := writeJSONFile(*suitePath, suite); checkpointErr != nil {
				err = errors.Join(err, checkpointErr)
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "capture live suite: %v", err)
			if dogfoodSuiteHasSamples(suite) {
				fmt.Fprintf(stderr, " (partial suite saved to %s)", *suitePath)
			}
			fmt.Fprintln(stderr)
			return 2
		}
	} else {
		suite, err = readSuite(*inputPath)
		if err != nil {
			fmt.Fprintf(stderr, "read suite: %v\n", err)
			return 2
		}
	}
	report := personality.EvaluateDogfoodSuite(suite, now())
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "encode report: %v\n", err)
		return 2
	}
	encoded = append(encoded, '\n')
	if _, err := stdout.Write(encoded); err != nil {
		fmt.Fprintf(stderr, "write stdout: %v\n", err)
		return 2
	}
	if *reportPath != "" {
		if err := os.WriteFile(*reportPath, encoded, 0o600); err != nil {
			fmt.Fprintf(stderr, "write report: %v\n", err)
			return 2
		}
	}
	if !report.Passed {
		return 1
	}
	return 0
}

func dogfoodSuiteHasSamples(suite personality.DogfoodSuite) bool {
	for _, run := range suite.Runs {
		if len(run.Samples) > 0 {
			return true
		}
	}
	return false
}

func captureCodexSuite(model string, checkpoint personality.DogfoodSuite, progress io.Writer) (personality.DogfoodSuite, error) {
	profileRoot, err := os.MkdirTemp("", "yuri-personality-dogfood-")
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	defer os.RemoveAll(profileRoot)
	restoreTestMode := preserveEnvironment(config.TestModeEnv)
	defer restoreTestMode()
	restoreProfileRoot := preserveEnvironment(config.TestProfileRootEnv)
	defer restoreProfileRoot()
	if err := os.Setenv(config.TestModeEnv, "1"); err != nil {
		return personality.DogfoodSuite{}, err
	}
	if err := os.Setenv(config.TestProfileRootEnv, profileRoot); err != nil {
		return personality.DogfoodSuite{}, err
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	value := config.Default(paths)
	value.Providers = []config.ProviderConfig{{
		ID: "codex-dogfood", Kind: config.ProviderCodexAppServer, DisplayName: "Codex App Server",
		Model: model, Binary: "codex", Enabled: true,
	}}
	value.Onboarding = config.OnboardingConfig{Completed: true, ProviderTested: true, AgentConfigured: true}
	value.Persona.AutoEvolution = false
	if err := config.Save(paths, value); err != nil {
		return personality.DogfoodSuite{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), desktop.PersonalityDogfoodTimeout)
	defer cancel()
	bridge, err := desktop.NewBridge(ctx)
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	defer bridge.Shutdown(context.Background())
	return desktop.RunPersonalityDogfoodResume(ctx, bridge, "codex-app-server", checkpoint, func(completed, total int, surface, profile, scenario string) {
		fmt.Fprintf(progress, "[%02d/%02d] %s %s/%s\n", completed, total, surface, profile, scenario)
	})
}

// captureOpenAICompatibleSuite runs the authenticated matrix against one
// owner-configured OpenAI-compatible provider. It reads only the owner's
// non-secret config metadata and then writes a new disposable test profile.
// The provider's opaque CredentialRef is intentionally retained: Bridge's
// production wiring resolves it from the system keyring at the adapter
// boundary. No owner database, conversations, agents, or memories are opened.
func captureOpenAICompatibleSuite(providerID, model string, checkpoint personality.DogfoodSuite, progress io.Writer) (personality.DogfoodSuite, error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return personality.DogfoodSuite{}, errors.New("provider ID is required")
	}
	ownerPaths, err := config.DefaultPaths()
	if err != nil {
		return personality.DogfoodSuite{}, fmt.Errorf("resolve owner config: %w", err)
	}
	ownerConfig, err := config.Load(ownerPaths)
	if err != nil {
		return personality.DogfoodSuite{}, fmt.Errorf("load owner config: %w", err)
	}

	selected, found := configuredOpenAIProvider(ownerConfig, providerID)
	if !found {
		return personality.DogfoodSuite{}, fmt.Errorf("OpenAI-compatible provider %q is not configured", providerID)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(selected.Model)
	}
	if model == "" {
		return personality.DogfoodSuite{}, fmt.Errorf("provider %q has no configured model; pass -model", providerID)
	}
	if strings.TrimSpace(selected.CredentialRef) == "" {
		return personality.DogfoodSuite{}, fmt.Errorf("provider %q has no system-keyring credential reference", providerID)
	}

	profileRoot, err := os.MkdirTemp("", "yuri-personality-dogfood-")
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	defer os.RemoveAll(profileRoot)
	restoreTestMode := preserveEnvironment(config.TestModeEnv)
	defer restoreTestMode()
	restoreProfileRoot := preserveEnvironment(config.TestProfileRootEnv)
	defer restoreProfileRoot()
	if err := os.Setenv(config.TestModeEnv, "1"); err != nil {
		return personality.DogfoodSuite{}, err
	}
	if err := os.Setenv(config.TestProfileRootEnv, profileRoot); err != nil {
		return personality.DogfoodSuite{}, err
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	value, err := cloneOpenAICompatibleConfig(ownerConfig, paths, providerID, model)
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	if err := config.Save(paths, value); err != nil {
		return personality.DogfoodSuite{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), desktop.PersonalityDogfoodTimeout)
	defer cancel()
	bridge, err := desktop.NewBridge(ctx)
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	defer bridge.Shutdown(context.Background())
	return desktop.RunPersonalityDogfoodResume(ctx, bridge, "OpenAI-compatible · "+providerID, checkpoint, func(completed, total int, surface, profile, scenario string) {
		fmt.Fprintf(progress, "[%02d/%02d] %s %s/%s\n", completed, total, surface, profile, scenario)
	})
}

func configuredOpenAIProvider(value config.Config, providerID string) (config.ProviderConfig, bool) {
	providerID = strings.TrimSpace(providerID)
	for _, provider := range value.Providers {
		if strings.TrimSpace(provider.ID) == providerID && provider.Kind == config.ProviderOpenAICompatible {
			return provider, true
		}
	}
	return config.ProviderConfig{}, false
}

// cloneOpenAICompatibleConfig deliberately starts from isolated defaults.
// Only the selected provider's non-secret route metadata and opaque keyring
// reference cross the owner-profile boundary. In particular, allowed paths,
// web-search settings, plugins, persona state, and other owner-local values
// are not copied into the disposable profile.
func cloneOpenAICompatibleConfig(owner config.Config, isolatedPaths config.Paths, providerID, model string) (config.Config, error) {
	selected, found := configuredOpenAIProvider(owner, providerID)
	if !found {
		return config.Config{}, fmt.Errorf("OpenAI-compatible provider %q is not configured", strings.TrimSpace(providerID))
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(selected.Model)
	}
	if model == "" {
		return config.Config{}, fmt.Errorf("provider %q has no configured model; pass -model", strings.TrimSpace(providerID))
	}
	if strings.TrimSpace(selected.CredentialRef) == "" {
		return config.Config{}, fmt.Errorf("provider %q has no system-keyring credential reference", strings.TrimSpace(providerID))
	}
	selected.Model = model
	selected.Enabled = true
	selected.FavoriteModels = append([]string(nil), selected.FavoriteModels...)

	clone := config.Default(isolatedPaths)
	clone.Providers = []config.ProviderConfig{selected}
	clone.Onboarding = config.OnboardingConfig{Completed: true, ProviderTested: true, AgentConfigured: true}
	clone.Persona.AutoEvolution = false
	if err := clone.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("validate isolated provider config: %w", err)
	}
	return clone, nil
}

func preserveEnvironment(key string) func() {
	value, existed := os.LookupEnv(key)
	return func() {
		if existed {
			_ = os.Setenv(key, value)
			return
		}
		_ = os.Unsetenv(key)
	}
}

func writeJSONFile(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(path, encoded, 0o600)
}

func readSuite(path string) (personality.DogfoodSuite, error) {
	file, err := os.Open(path)
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSuiteBytes+1))
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	if len(data) > maxSuiteBytes {
		return personality.DogfoodSuite{}, fmt.Errorf("suite exceeds %d bytes", maxSuiteBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var suite personality.DogfoodSuite
	if err := decoder.Decode(&suite); err != nil {
		return personality.DogfoodSuite{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return personality.DogfoodSuite{}, errors.New("suite contains multiple JSON values")
		}
		return personality.DogfoodSuite{}, err
	}
	return suite, nil
}
