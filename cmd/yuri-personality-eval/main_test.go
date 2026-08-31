package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	for _, args := range [][]string{{"-live-codex"}, {"-live-codex", "-suite", "out.json", "-input", "in.json"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, time.Now); code != 2 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
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
