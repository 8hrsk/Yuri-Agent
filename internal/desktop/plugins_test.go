package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func TestPluginPackageInspectionAndInstallRequireDevMode(t *testing.T) {
	root := t.TempDir()
	database, err := storage.Open(context.Background(), filepath.Join(root, "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	packageDirectory := filepath.Join(root, "package")
	if err := os.Mkdir(packageDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := []byte("#!/bin/sh\nexit 0\n")
	executablePath := filepath.Join(packageDirectory, "echo")
	if err := os.WriteFile(executablePath, executable, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(executable)
	manifest := plugins.Manifest{
		SchemaVersion: plugins.ManifestSchemaVersion, ID: "dev.yuri.echo", Name: "Echo", Version: "0.1.0", Publisher: "OrdoAI",
		Executable: "echo", SupportedOS: []string{runtime.GOOS}, SupportedArch: []string{runtime.GOARCH},
		ProtocolVersion: plugins.ProtocolVersion,
		Checksum:        &plugins.ChecksumMetadata{Algorithm: "sha256", Value: hex.EncodeToString(digest[:])},
		Tools: []plugins.ToolDeclaration{{
			ID: "echo", Name: "Echo", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`),
			Risk: domain.RiskLow,
		}},
	}
	manifestContent, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDirectory, plugins.ManifestFileName), manifestContent, 0o600); err != nil {
		t.Fatal(err)
	}
	pluginRoot := filepath.Join(root, "plugins")
	bridge := &Bridge{
		repositories: repositories,
		paths:        config.Paths{PluginDirectory: pluginRoot},
		config:       config.Config{Version: 1, Locale: "ru-RU", LogLevel: "info", DataDirectory: root},
		appCtx:       context.Background(),
	}
	inspection, err := bridge.InspectPluginPackage(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Installable || !inspection.RequiresDevMode || inspection.SignatureStatus != "unsigned" || inspection.Manifest == nil {
		t.Fatalf("unexpected production inspection: %#v", inspection)
	}
	if _, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory}); err == nil {
		t.Fatal("unsigned package installed without dev mode")
	}
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory, DevMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if installed.Enabled || installed.RuntimeStatus != "stopped" {
		t.Fatalf("installed plugin must remain disabled and stopped: %#v", installed)
	}
	if installed.SignatureStatus != "dev" {
		t.Fatalf("installed dev package signature status = %q", installed.SignatureStatus)
	}
	enabled, err := bridge.EnablePlugin(PluginIDRequest{PluginID: manifest.ID})
	if err != nil || !enabled.Enabled {
		t.Fatalf("enable plugin = %#v, %v", enabled, err)
	}
	disabled, err := bridge.DisablePlugin(PluginIDRequest{ID: manifest.ID})
	if err != nil || disabled.Enabled {
		t.Fatalf("disable plugin = %#v, %v", disabled, err)
	}
	if _, err := os.Stat(filepath.Join(pluginRoot, manifest.ID, manifest.Version, "echo")); err != nil {
		t.Fatalf("installed executable: %v", err)
	}
	listed, err := bridge.ListPlugins()
	if err != nil || len(listed) != 1 || listed[0].ID != manifest.ID {
		t.Fatalf("listed plugins = %#v, %v", listed, err)
	}
	events, err := repositories.Audit.List(context.Background(), 10)
	if err != nil || len(events) != 4 || events[3].Action != "plugin.install" {
		t.Fatalf("audit events = %#v, %v", events, err)
	}
	if err := bridge.UninstallPlugin(PluginIDRequest{ID: manifest.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pluginRoot, manifest.ID, manifest.Version)); !os.IsNotExist(err) {
		t.Fatalf("plugin directory still exists after uninstall: %v", err)
	}
}

func TestRemoveOwnedPluginDirectoryRejectsSymlinkedParent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "0.1.0"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "0.1.0", "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dev.yuri.escape")); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedPluginDirectory(root, "dev.yuri.escape", "0.1.0"); err == nil {
		t.Fatal("symlinked plugin parent was accepted")
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("outside marker changed: %q, %v", content, err)
	}
}
