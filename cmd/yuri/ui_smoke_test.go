package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUISmokeReporterPublishesPassedOnboardingFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui-result.json")
	reporter := newUISmokeReporter(launchSmokeOptions{
		uiFlow:       uiSmokeFlowOnboarding,
		uiResultFile: path,
	})
	result := UISmokeResult{
		Flow:  uiSmokeFlowOnboarding,
		State: "passed",
		Steps: append([]string(nil), uiSmokeSteps[uiSmokeFlowOnboarding]...),
	}
	if err := reporter.Report(result); err != nil {
		t.Fatalf("Report() error = %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored UISmokeResult
	if err := json.Unmarshal(content, &stored); err != nil {
		t.Fatalf("decode UI result: %v", err)
	}
	if stored.State != "passed" || len(stored.Steps) != len(uiSmokeSteps[uiSmokeFlowOnboarding]) {
		t.Fatalf("stored UI result = %#v", stored)
	}
	if strings.Contains(string(content), uiSmokeSecretCanary) {
		t.Fatal("UI result contains the secret canary")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("result permissions = %o, want 600", info.Mode().Perm())
	}
	if err := reporter.Report(result); err == nil {
		t.Fatal("reporter accepted a second terminal result")
	}
}

func TestUISmokeReporterPublishesPassedVoiceFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui-result.json")
	reporter := newUISmokeReporter(launchSmokeOptions{
		uiFlow:       uiSmokeFlowVoice,
		uiResultFile: path,
	})
	if err := reporter.Report(UISmokeResult{
		Flow:  uiSmokeFlowVoice,
		State: "passed",
		Steps: append([]string(nil), uiSmokeSteps[uiSmokeFlowVoice]...),
	}); err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), `"flow":"voice"`) || !strings.Contains(string(content), `"barge-in-visible"`) {
		t.Fatalf("voice UI result is incomplete: %s", content)
	}
}

func TestUISmokeReporterRedactsFailedDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui-result.json")
	reporter := newUISmokeReporter(launchSmokeOptions{
		uiFlow:       uiSmokeFlowOnboarding,
		uiResultFile: path,
	})
	if err := reporter.Report(UISmokeResult{
		Flow:  uiSmokeFlowOnboarding,
		State: "failed",
		Steps: []string{"welcome-visible"},
		Error: "unexpected value " + uiSmokeSecretCanary,
	}); err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), uiSmokeSecretCanary) || !strings.Contains(string(content), "[redacted]") {
		t.Fatalf("diagnostic was not safely redacted: %s", content)
	}
}

func TestUISmokeReporterRejectsIncompletePassAndInvalidFailure(t *testing.T) {
	newReporter := func() *UISmokeReporter {
		return newUISmokeReporter(launchSmokeOptions{
			uiFlow:       uiSmokeFlowOnboarding,
			uiResultFile: filepath.Join(t.TempDir(), "ui-result.json"),
		})
	}
	if err := newReporter().Report(UISmokeResult{
		Flow: uiSmokeFlowOnboarding, State: "passed", Steps: []string{"welcome-visible"},
	}); err == nil {
		t.Fatal("reporter accepted an incomplete passed flow")
	}
	if err := newReporter().Report(UISmokeResult{
		Flow: uiSmokeFlowOnboarding, State: "failed",
	}); err == nil {
		t.Fatal("reporter accepted a failed flow without a diagnostic")
	}
	if err := newReporter().Report(UISmokeResult{
		Flow: uiSmokeFlowOnboarding, State: "failed", Steps: []string{"chat-visible"}, Error: "out of order",
	}); err == nil {
		t.Fatal("reporter accepted an out-of-order checkpoint")
	}
}
