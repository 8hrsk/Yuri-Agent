package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/desktop"
)

func TestLaunchSmokeEnvironmentRequiresExplicitTestMode(t *testing.T) {
	t.Setenv(config.TestModeEnv, "")
	t.Setenv(config.TestProfileRootEnv, t.TempDir())
	t.Setenv(launchSmokeReadyFileEnv, filepath.Join(t.TempDir(), "ready.json"))
	t.Setenv(launchSmokeAutoExitEnv, "1")

	if _, err := launchSmokeOptionsFromEnvironment(); err == nil {
		t.Fatal("launch smoke accepted configuration outside explicit test mode")
	}
}

func TestLaunchSmokeEnvironmentRequiresIsolatedRootAndMarkerForAutoExit(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.TestModeEnv, "1")
	t.Setenv(config.TestProfileRootEnv, root)
	t.Setenv(launchSmokeReadyFileEnv, filepath.Join(root, "ready.json"))
	t.Setenv(launchSmokeAutoExitEnv, "1")

	options, err := launchSmokeOptionsFromEnvironment()
	if err != nil {
		t.Fatalf("launchSmokeOptionsFromEnvironment() error = %v", err)
	}
	if !options.enabled() || !options.autoExit || options.readyFile == "" {
		t.Fatalf("launch smoke options = %#v", options)
	}

	t.Setenv(launchSmokeReadyFileEnv, "")
	if _, err := launchSmokeOptionsFromEnvironment(); err == nil {
		t.Fatal("launch smoke auto-exit accepted a missing readiness marker")
	}
}

func TestLaunchSmokeEnvironmentRejectsMarkerOutsideIsolatedRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.TestModeEnv, "1")
	t.Setenv(config.TestProfileRootEnv, root)
	t.Setenv(launchSmokeReadyFileEnv, filepath.Join(t.TempDir(), "ready.json"))
	t.Setenv(launchSmokeAutoExitEnv, "1")

	if _, err := launchSmokeOptionsFromEnvironment(); err == nil {
		t.Fatal("launch smoke accepted a readiness marker outside its isolated profile root")
	}
}

func TestWriteLaunchSmokeReadyPublishesOnlyHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready.json")
	status := desktop.Status{State: "ready", Version: "0.7.0-stage7", Platform: "darwin/arm64"}
	if err := writeLaunchSmokeReady(path, status); err != nil {
		t.Fatalf("writeLaunchSmokeReady() error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var marker launchSmokeReadyMarker
	if err := json.Unmarshal(content, &marker); err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	if marker.State != status.State || marker.Version != status.Version || marker.Platform != status.Platform {
		t.Fatalf("marker = %#v, want status %#v", marker, status)
	}
	if string(content) == "" || string(content) == "sk-secret" {
		t.Fatal("marker unexpectedly contains credential material")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("marker permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteLaunchSmokeReadyRejectsNonReadyBridge(t *testing.T) {
	err := writeLaunchSmokeReady(filepath.Join(t.TempDir(), "ready.json"), desktop.Status{State: "starting"})
	if err == nil {
		t.Fatal("writeLaunchSmokeReady() accepted a non-ready status")
	}
}
