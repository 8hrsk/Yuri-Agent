package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/observability"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func TestPluginPackageLifecycleSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	databasePath := filepath.Join(root, "yuri.sqlite3")
	packageDirectory := buildCrashPluginPackage(t, root)
	pluginDirectory := filepath.Join(root, "installed-plugins")

	database, err := storage.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open plugin smoke database: %v", err)
	}
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		_ = database.Close()
		t.Fatalf("construct plugin smoke repositories: %v", err)
	}
	bridge, stopBridge := newPluginSmokeBridge(repositories, pluginDirectory, root)
	defer func() {
		stopBridge()
		_ = database.Close()
	}()
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		stopBridge()
		_ = database.Close()
		t.Fatalf("install crashable plugin package: %v", err)
	}
	if installed.Enabled || installed.RuntimeStatus != "stopped" || installed.SignatureStatus != "dev" {
		t.Fatalf("unexpected installed plugin state: %#v", installed)
	}
	enabled, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityNotificationsSend), AllowUnrestricted: true,
	}}})
	if err != nil || !enabled.Enabled || len(enabled.Permissions) != 1 {
		stopBridge()
		_ = database.Close()
		t.Fatalf("enable plugin and grants: %#v err=%v", enabled, err)
	}
	grants, err := repositories.Plugins.Grants(ctx, domain.ID(installed.ID))
	if err != nil || len(grants) != 1 || grants[0].Capability != domain.CapabilityNotificationsSend {
		stopBridge()
		_ = database.Close()
		t.Fatalf("persisted plugin grants: %#v err=%v", grants, err)
	}
	if _, err := bridge.StartPlugin(PluginIDRequest{ID: installed.ID}); err != nil {
		stopBridge()
		_ = database.Close()
		t.Fatalf("start installed plugin: %v", err)
	}
	supervisor := bridgePluginSupervisor(t, bridge, installed.ID)
	assertPluginEcho(t, ctx, supervisor, "before crash")
	if _, err := supervisor.InvokeTool(ctx, plugins.ToolInvokeParams{
		ToolID: "echo", Arguments: json.RawMessage(`{"message":"crash"}`),
	}); !errors.Is(err, plugins.ErrPluginExited) {
		t.Fatalf("crash invocation error = %v, want ErrPluginExited", err)
	}
	waitForPluginReady(t, ctx, supervisor, 5*time.Second)
	assertPluginEcho(t, ctx, supervisor, "after crash recovery")

	if _, err := bridge.StopPlugin(PluginIDRequest{ID: installed.ID}); err != nil {
		t.Fatalf("stop plugin before app restart: %v", err)
	}
	stopBridge()
	if err := database.Close(); err != nil {
		t.Fatalf("close plugin smoke database: %v", err)
	}

	reopenedDatabase, err := storage.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("reopen plugin smoke database: %v", err)
	}
	defer reopenedDatabase.Close()
	reopenedRepositories, err := storage.NewRepositories(reopenedDatabase)
	if err != nil {
		t.Fatalf("construct reopened plugin repositories: %v", err)
	}
	restartedBridge, stopRestartedBridge := newPluginSmokeBridge(reopenedRepositories, pluginDirectory, root)
	defer stopRestartedBridge()
	restartedBridge.restoreEnabledPlugins()
	restartedSupervisor := waitForBridgePlugin(t, restartedBridge, installed.ID, 8*time.Second)
	assertPluginEcho(t, ctx, restartedSupervisor, "after app restart")
	grants, err = reopenedRepositories.Plugins.Grants(ctx, domain.ID(installed.ID))
	if err != nil || len(grants) != 1 || grants[0].Capability != domain.CapabilityNotificationsSend {
		t.Fatalf("grants after app restart: %#v err=%v", grants, err)
	}
	if _, err := restartedBridge.StopPlugin(PluginIDRequest{ID: installed.ID}); err != nil {
		t.Fatalf("stop restored plugin: %v", err)
	}
}

func buildCrashPluginPackage(t *testing.T, root string) string {
	t.Helper()
	packageDirectory := filepath.Join(root, "plugin-package")
	if err := os.MkdirAll(packageDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(packageDirectory, "crash-plugin")
	command := exec.Command("go", "build", "-o", executable, "./testdata/crashplugin")
	command.Dir = filepath.Dir(executableSourcePath(t))
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build crashable plugin fixture: %v\n%s", err, output)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	manifest := plugins.Manifest{
		SchemaVersion: plugins.ManifestSchemaVersion, ID: "test.yuri.crashable", Name: "Yuri Crash Recovery Fixture",
		Version: "0.1.0", Publisher: "OrdoAI", Executable: "crash-plugin",
		SupportedOS: []string{runtime.GOOS}, SupportedArch: []string{runtime.GOARCH},
		ProtocolVersion: plugins.ProtocolVersion, MinCoreVersion: "0.1.0", MaxCoreVersion: pluginCoreVersion,
		Checksum: &plugins.ChecksumMetadata{Algorithm: "sha256", Value: hex.EncodeToString(digest[:])},
		Tools: []plugins.ToolDeclaration{{
			ID: "echo", Name: "Echo with grant", Risk: domain.RiskMedium,
			InputSchema:  json.RawMessage(`{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}`),
			OutputSchema: json.RawMessage(`{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}`),
			Permissions:  []string{string(domain.CapabilityNotificationsSend)},
		}},
		Permissions: []plugins.PermissionDeclaration{{
			Capability: string(domain.CapabilityNotificationsSend), Scope: json.RawMessage(`{"kind":"unrestricted"}`), Reason: "exercise persisted grants",
		}},
	}
	manifestContent, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDirectory, plugins.ManifestFileName), manifestContent, 0o600); err != nil {
		t.Fatal(err)
	}
	return packageDirectory
}

func executableSourcePath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve desktop test source")
	}
	return filename
}

func newPluginSmokeBridge(repositories *storage.Repositories, pluginDirectory, dataDirectory string) (*Bridge, func()) {
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	bridge := &Bridge{
		logger:       observability.NewLogger(observability.LoggerOptions{Level: slog.LevelInfo, Format: "json", Output: io.Discard}),
		repositories: repositories, paths: config.Paths{PluginDirectory: pluginDirectory},
		config: config.Config{Version: 1, Locale: "ru-RU", LogLevel: "info", DataDirectory: dataDirectory, PluginDevMode: true},
		appCtx: context.Background(), backgroundCtx: backgroundCtx, backgroundCancel: backgroundCancel,
		pluginSupervisors: make(map[string]*plugins.Supervisor),
	}
	return bridge, func() {
		bridge.mu.Lock()
		supervisors := make([]*plugins.Supervisor, 0, len(bridge.pluginSupervisors))
		for _, supervisor := range bridge.pluginSupervisors {
			supervisors = append(supervisors, supervisor)
		}
		bridge.pluginSupervisors = make(map[string]*plugins.Supervisor)
		bridge.mu.Unlock()
		for _, supervisor := range supervisors {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = supervisor.Stop(stopCtx)
			cancel()
		}
		backgroundCancel()
		bridge.background.Wait()
	}
}

func bridgePluginSupervisor(t *testing.T, bridge *Bridge, pluginID string) *plugins.Supervisor {
	t.Helper()
	bridge.mu.RLock()
	supervisor := bridge.pluginSupervisors[pluginID]
	bridge.mu.RUnlock()
	if supervisor == nil {
		t.Fatalf("plugin %s has no supervisor", pluginID)
	}
	return supervisor
}

func waitForBridgePlugin(t *testing.T, bridge *Bridge, pluginID string, timeout time.Duration) *plugins.Supervisor {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		bridge.mu.RLock()
		supervisor := bridge.pluginSupervisors[pluginID]
		bridge.mu.RUnlock()
		if supervisor != nil {
			state, _ := supervisor.State()
			if state == plugins.StateRunning {
				return supervisor
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("plugin %s did not restore after app restart", pluginID)
	return nil
}

func waitForPluginReady(t *testing.T, ctx context.Context, supervisor *plugins.Supervisor, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := supervisor.Health(ctx); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	state, stateErr := supervisor.State()
	t.Fatalf("plugin did not become healthy after crash: state=%s err=%v", state, stateErr)
}

func assertPluginEcho(t *testing.T, ctx context.Context, supervisor *plugins.Supervisor, message string) {
	t.Helper()
	arguments, _ := json.Marshal(map[string]string{"message": message})
	result, err := supervisor.InvokeTool(ctx, plugins.ToolInvokeParams{ToolID: "echo", Arguments: arguments})
	if err != nil {
		t.Fatalf("invoke plugin echo %q: %v", message, err)
	}
	expected, _ := json.Marshal(map[string]string{"message": message})
	if !result.OK || string(result.Output) != string(expected) {
		t.Fatalf("plugin echo result = %#v, want %s", result, expected)
	}
}
