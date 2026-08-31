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
	suitePath := flags.String("suite", "", "path for a live captured suite (required with -live-codex)")
	model := flags.String("model", "", "optional Codex model; empty uses codex-default")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *liveCodex && *inputPath != "" {
		fmt.Fprintln(stderr, "-input and -live-codex are mutually exclusive")
		return 2
	}
	if !*liveCodex && *inputPath == "" {
		fmt.Fprintln(stderr, "-input is required")
		return 2
	}

	var suite personality.DogfoodSuite
	var err error
	if *liveCodex {
		if *suitePath == "" {
			fmt.Fprintln(stderr, "-suite is required with -live-codex")
			return 2
		}
		suite, err = captureCodexSuite(strings.TrimSpace(*model), stderr)
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

func captureCodexSuite(model string, progress io.Writer) (personality.DogfoodSuite, error) {
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
	return desktop.RunPersonalityDogfood(ctx, bridge, "codex-app-server", func(completed, total int, surface, profile, scenario string) {
		fmt.Fprintf(progress, "[%02d/%02d] %s %s/%s\n", completed, total, surface, profile, scenario)
	})
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
