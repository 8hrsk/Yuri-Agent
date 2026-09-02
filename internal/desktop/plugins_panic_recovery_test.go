package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// TestPluginEventWatcherPanicStopsAndFailsThePlugin pins the M-9 guard on the
// only bridge goroutine reachable from untrusted input: the loop that consumes
// a third-party plugin's event frames.
//
// Three properties are asserted together on purpose. The process survives; the
// plugin's durable row reaches a terminal failed state; and the now-unsupervised
// process is stopped and its supervisor slot released. Surviving alone is what a
// bare recover() would buy, and it is worth nothing here — it leaves a plugin
// marked running, with the owner's granted capabilities, and nobody reading its
// events.
func TestPluginEventWatcherPanicStopsAndFailsThePlugin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	packageDirectory := buildEventPluginPackage(t, root)
	bridge, repositories, stop := newEventPluginBridge(t, root)
	defer stop()

	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatalf("install event plugin package: %v", err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID}); err != nil {
		t.Fatalf("enable event plugin: %v", err)
	}

	// The supervisor is built the way StartPlugin builds it, rather than by
	// calling StartPlugin, so that the fault below is injected before any
	// watcher goroutine exists to observe it.
	id := domain.ID(installed.ID)
	record, err := repositories.Plugins.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := loadManifestFromDirectory(record.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := plugins.NewSupervisor(plugins.SupervisorConfig{
		Manifest: manifest, PackageDir: record.InstallPath, CoreVersion: pluginCoreVersion,
		Authorizer: pluginGrantAuthorizer{repository: repositories.Plugins}, DevMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("start event plugin: %v", err)
	}
	bridge.mu.Lock()
	bridge.pluginSupervisors[installed.ID] = supervisor
	bridge.mu.Unlock()
	if err := repositories.Plugins.SetRuntimeStatus(ctx, id, string(plugins.StateRunning), "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	// Fault injection: handling an accepted event ends in an audit append, so a
	// nil audit repository turns the next event the plugin sends into the same
	// nil-pointer fault a corrupt dependency would raise while parsing one.
	bridge.repositories.Audit = nil

	bridge.watchPlugin(id, supervisor, manifest)

	// The hostile input itself: a real event frame, published by the real child
	// process over the real protocol.
	_, invokeErr := supervisor.InvokeTool(ctx, plugins.ToolInvokeParams{
		ToolID: "emit", Arguments: json.RawMessage(`{"event_type":"tick"}`),
	})

	stored := waitForPluginRuntimeStatus(t, ctx, repositories, id, string(plugins.StateFailed), 20*time.Second, invokeErr)
	if stored.LastError == "" {
		t.Fatalf("failed plugin recorded no last error: %#v", stored)
	}
	bridge.mu.RLock()
	leftover := bridge.pluginSupervisors[installed.ID]
	bridge.mu.RUnlock()
	if leftover != nil {
		t.Fatal("a plugin whose event watcher died is still registered as supervised")
	}
	if state, _ := supervisor.State(); state == plugins.StateRunning {
		t.Fatal("the plugin process is still running after its event watcher died")
	}
}

// TestRestoreEnabledPluginsPanicFailsTheUnrestoredPlugins pins the guard on the
// startup restore pass. The pass aborts wherever the panic lands, so plugins it
// had not reached keep whatever the previous session left behind — including a
// row still reading "running" after an unclean shutdown, which the UI presents
// as healthy while no process exists. Recovering without correcting those rows
// would leave exactly that lie in place, so the assertion is on the stored
// status and not on the process staying alive.
//
// The plugin that really did restore must keep its running status: a recovery
// that failed everything indiscriminately would be wrong in the other direction.
func TestRestoreEnabledPluginsPanicFailsTheUnrestoredPlugins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	packageDirectory := buildEventPluginPackage(t, root)
	bridge, repositories, stop := newEventPluginBridge(t, root)
	defer stop()

	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatalf("install event plugin package: %v", err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID}); err != nil {
		t.Fatalf("enable event plugin: %v", err)
	}

	// A second enabled plugin the restore pass will never reach. It sorts after
	// the real one, and its row still claims a live session the way an unclean
	// shutdown leaves it.
	now := time.Now().UTC()
	stale := storage.PluginRecord{
		ID: "zz.stale.plugin", Name: "Zz Stale Plugin", Publisher: "OrdoAI", Version: "0.1.0",
		ProtocolVersion: plugins.ProtocolVersion, Enabled: true,
		InstallPath:  filepath.Join(root, "installed-plugins", "zz.stale.plugin", "0.1.0"),
		ManifestJSON: "{}", SignatureStatus: "dev", Checksum: "sha256:stale",
		RuntimeStatus: string(plugins.StateRunning), InstalledAt: now, UpdatedAt: now,
	}
	if err := repositories.Plugins.Upsert(ctx, stale, storage.PluginSource{PluginID: stale.ID, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}

	// Fault injection: StartPlugin finishes by appending its audit event, so a
	// nil audit repository faults the restore pass after the first plugin is
	// genuinely up and before the second one is ever attempted.
	bridge.repositories.Audit = nil

	bridge.restoreEnabledPlugins()

	stored := waitForPluginRuntimeStatus(t, ctx, repositories, stale.ID, string(plugins.StateFailed), 30*time.Second, nil)
	if stored.LastError == "" {
		t.Fatalf("unrestored plugin recorded no last error: %#v", stored)
	}
	restored, err := repositories.Plugins.Get(ctx, domain.ID(installed.ID))
	if err != nil {
		t.Fatal(err)
	}
	if restored.RuntimeStatus != string(plugins.StateRunning) {
		t.Fatalf("the plugin that did restore was reported as %q, want running", restored.RuntimeStatus)
	}
}

func waitForPluginRuntimeStatus(
	t *testing.T, ctx context.Context, repositories *storage.Repositories,
	id domain.ID, want string, timeout time.Duration, cause error,
) storage.PluginRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last storage.PluginRecord
	for time.Now().Before(deadline) {
		record, err := repositories.Plugins.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if record.RuntimeStatus == want {
			return record
		}
		last = record
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("plugin %s runtime status = %q, want %q (context: %v)", id, last.RuntimeStatus, want, cause)
	return storage.PluginRecord{}
}

func newEventPluginBridge(t *testing.T, root string) (*Bridge, *storage.Repositories, func()) {
	t.Helper()
	database, err := storage.Open(context.Background(), filepath.Join(root, "yuri.sqlite3"))
	if err != nil {
		t.Fatalf("open event plugin database: %v", err)
	}
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		_ = database.Close()
		t.Fatalf("construct event plugin repositories: %v", err)
	}
	bridge, stopBridge := newPluginSmokeBridge(repositories, filepath.Join(root, "installed-plugins"), root)
	stopped := false
	return bridge, repositories, func() {
		if stopped {
			return
		}
		stopped = true
		stopBridge()
		_ = database.Close()
	}
}

func buildEventPluginPackage(t *testing.T, root string) string {
	t.Helper()
	packageDirectory := filepath.Join(root, "event-plugin-package")
	if err := os.MkdirAll(packageDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := testExecutablePath(filepath.Join(packageDirectory, "event-plugin"))
	command := exec.Command("go", "build", "-o", executable, "./testdata/eventplugin")
	command.Dir = filepath.Dir(executableSourcePath(t))
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build event plugin fixture: %v\n%s", err, output)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	manifest := plugins.Manifest{
		SchemaVersion: plugins.ManifestSchemaVersion, ID: "test.yuri.eventful", Name: "Yuri Event Watcher Fixture",
		Version: "0.1.0", Publisher: "OrdoAI", Executable: "event-plugin",
		SupportedOS: []string{runtime.GOOS}, SupportedArch: []string{runtime.GOARCH},
		ProtocolVersion: plugins.ProtocolVersion, MinCoreVersion: "0.1.0", MaxCoreVersion: pluginCoreVersion,
		Checksum: &plugins.ChecksumMetadata{Algorithm: "sha256", Value: hex.EncodeToString(digest[:])},
		Tools: []plugins.ToolDeclaration{{
			ID: "emit", Name: "Publish an event", Risk: domain.RiskLow,
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"event_type":{"type":"string"}}}`),
			OutputSchema: json.RawMessage(`{"type":"object","required":["emitted"],"properties":{"emitted":{"type":"boolean"}}}`),
		}},
		EventSources: []plugins.EventSource{{
			ID: "heartbeat", Name: "Heartbeat", Schema: json.RawMessage(`{"type":"object"}`),
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
