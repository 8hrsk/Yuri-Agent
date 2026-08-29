package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const uiSmokeSecretCanary = "ui-smoke-secret-canary"

var uiSmokeSteps = map[string][]string{
	uiSmokeFlowOnboarding: {
		"welcome-visible",
		"provider-form-visible",
		"provider-submit-dispatched",
		"success-visible",
		"chat-visible",
		"second-agent-created",
		"first-agent-restored",
	},
	uiSmokeFlowVoice: {
		"chat-visible",
		"voice-boundaries-ready",
		"recording-visible",
		"transcribing-visible",
		"transcript-visible",
		"agent-thinking-visible",
		"assistant-response-visible",
		"tts-speaking-visible",
		"barge-in-visible",
	},
}

// UISmokeResult is deliberately narrow: the injected WebKit flow may report
// fixed checkpoints and a bounded diagnostic, but cannot write arbitrary data.
type UISmokeResult struct {
	Flow  string   `json:"flow"`
	State string   `json:"state"`
	Steps []string `json:"steps"`
	Error string   `json:"error,omitempty"`
}

// UISmokeReporter is bound only in explicit, isolated test mode. Production
// launches never expose this bridge to the renderer.
type UISmokeReporter struct {
	mu         sync.Mutex
	flow       string
	resultFile string
	autoExit   bool
	ctx        context.Context
	reported   bool
}

func newUISmokeReporter(options launchSmokeOptions) *UISmokeReporter {
	return &UISmokeReporter{
		flow:       options.uiFlow,
		resultFile: options.uiResultFile,
		autoExit:   options.autoExit,
	}
}

func (reporter *UISmokeReporter) attach(ctx context.Context) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	reporter.ctx = ctx
}

// Report persists one terminal UI result and optionally closes the smoke app.
func (reporter *UISmokeReporter) Report(result UISmokeResult) error {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()

	if reporter.reported {
		return fmt.Errorf("UI smoke result was already reported")
	}
	expectedSteps, knownFlow := uiSmokeSteps[result.Flow]
	if result.Flow != reporter.flow || !knownFlow {
		return fmt.Errorf("unexpected UI smoke flow %q", result.Flow)
	}
	if result.State != "passed" && result.State != "failed" {
		return fmt.Errorf("unexpected UI smoke state %q", result.State)
	}
	if len(result.Steps) > len(expectedSteps) {
		return fmt.Errorf("unexpected UI smoke checkpoint count")
	}
	for index, step := range result.Steps {
		if step != expectedSteps[index] {
			return fmt.Errorf("unexpected UI smoke checkpoint %q", step)
		}
	}
	if result.State == "passed" && len(result.Steps) != len(expectedSteps) {
		return fmt.Errorf("passed UI smoke is missing checkpoints")
	}
	if result.State == "failed" && strings.TrimSpace(result.Error) == "" {
		return fmt.Errorf("failed UI smoke requires a diagnostic")
	}
	if len(result.Error) > 512 {
		result.Error = result.Error[:512]
	}
	result.Error = strings.ReplaceAll(result.Error, uiSmokeSecretCanary, "[redacted]")
	if result.State == "passed" {
		result.Error = ""
	}

	if err := writeUISmokeResult(reporter.resultFile, result); err != nil {
		return err
	}
	reporter.reported = true
	if reporter.autoExit && reporter.ctx != nil {
		ctx := reporter.ctx
		time.AfterFunc(100*time.Millisecond, func() { wailsruntime.Quit(ctx) })
	}
	return nil
}

func writeUISmokeResult(path string, result UISmokeResult) error {
	content, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode UI smoke result: %w", err)
	}
	content = append(content, '\n')

	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".yuri-ui-smoke-*.tmp")
	if err != nil {
		return fmt.Errorf("create UI smoke result: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set UI smoke result permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write UI smoke result: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync UI smoke result: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close UI smoke result: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish UI smoke result: %w", err)
	}
	return nil
}
