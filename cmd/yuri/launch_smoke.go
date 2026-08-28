package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/desktop"
)

const (
	launchSmokeReadyFileEnv = "YURI_TEST_READY_FILE"
	launchSmokeAutoExitEnv  = "YURI_TEST_AUTO_EXIT"
)

// launchSmokeOptions is deliberately only available behind config.TestModeEnv.
// It gives an external macOS harness a readiness boundary after WebKit's DOM
// is ready, while keeping ordinary launches free of test-specific behavior.
type launchSmokeOptions struct {
	readyFile string
	autoExit  bool
}

func (options launchSmokeOptions) enabled() bool {
	return options.readyFile != "" || options.autoExit
}

func (options launchSmokeOptions) writeReady(status desktop.Status) error {
	if options.readyFile == "" {
		return nil
	}
	return writeLaunchSmokeReady(options.readyFile, status)
}

func launchSmokeOptionsFromEnvironment() (launchSmokeOptions, error) {
	mode, modeSet := os.LookupEnv(config.TestModeEnv)
	readyValue, readySet := os.LookupEnv(launchSmokeReadyFileEnv)
	autoExitValue, autoExitSet := os.LookupEnv(launchSmokeAutoExitEnv)
	mode = strings.TrimSpace(mode)
	readyFile := strings.TrimSpace(readyValue)

	if mode == "" {
		if modeSet || readySet || autoExitSet {
			return launchSmokeOptions{}, fmt.Errorf("%s, %s, and %s require %s=1", launchSmokeReadyFileEnv, launchSmokeAutoExitEnv, config.TestProfileRootEnv, config.TestModeEnv)
		}
		return launchSmokeOptions{}, nil
	}
	if mode != "1" {
		return launchSmokeOptions{}, fmt.Errorf("%s must be exactly 1 for launch smoke", config.TestModeEnv)
	}
	if strings.TrimSpace(os.Getenv(config.TestProfileRootEnv)) == "" {
		return launchSmokeOptions{}, fmt.Errorf("%s is required when %s=1", config.TestProfileRootEnv, config.TestModeEnv)
	}
	if readySet && readyFile == "" {
		return launchSmokeOptions{}, fmt.Errorf("%s must not be empty", launchSmokeReadyFileEnv)
	}
	if readyFile != "" && !filepath.IsAbs(readyFile) {
		return launchSmokeOptions{}, fmt.Errorf("%s must be an absolute path", launchSmokeReadyFileEnv)
	}
	if readyFile != "" {
		profileRoot := filepath.Clean(strings.TrimSpace(os.Getenv(config.TestProfileRootEnv)))
		readyFile = filepath.Clean(readyFile)
		relative, err := filepath.Rel(profileRoot, readyFile)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return launchSmokeOptions{}, fmt.Errorf("%s must be a file inside %s", launchSmokeReadyFileEnv, config.TestProfileRootEnv)
		}
	}

	options := launchSmokeOptions{readyFile: readyFile}
	if autoExitSet {
		if strings.TrimSpace(autoExitValue) != "1" {
			return launchSmokeOptions{}, fmt.Errorf("%s must be exactly 1 when configured", launchSmokeAutoExitEnv)
		}
		options.autoExit = true
	}
	if options.autoExit && options.readyFile == "" {
		return launchSmokeOptions{}, fmt.Errorf("%s requires %s", launchSmokeAutoExitEnv, launchSmokeReadyFileEnv)
	}
	return options, nil
}

type launchSmokeReadyMarker struct {
	State    string `json:"state"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

// writeLaunchSmokeReady atomically publishes a non-secret health marker. The
// parent directory must already exist; the launch harness owns its temporary
// root, and this prevents a malformed path from creating arbitrary directories.
func writeLaunchSmokeReady(path string, status desktop.Status) error {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("launch smoke ready file must be an absolute path")
	}
	if status.State != "ready" {
		return fmt.Errorf("launch smoke bridge is not ready: %q", status.State)
	}
	content, err := json.Marshal(launchSmokeReadyMarker{
		State: status.State, Version: status.Version, Platform: status.Platform,
	})
	if err != nil {
		return fmt.Errorf("encode launch smoke marker: %w", err)
	}
	content = append(content, '\n')

	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("inspect launch smoke marker directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("launch smoke marker parent is not a directory: %s", parent)
	}
	temporary, err := os.CreateTemp(parent, ".yuri-launch-ready-*.tmp")
	if err != nil {
		return fmt.Errorf("create launch smoke marker: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set launch smoke marker permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write launch smoke marker: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync launch smoke marker: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close launch smoke marker: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish launch smoke marker: %w", err)
	}
	return nil
}
