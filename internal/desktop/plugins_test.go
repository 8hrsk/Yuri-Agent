package desktop

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/observability"
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
	bridge.config.PluginDevMode = true
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if installed.Enabled || installed.RuntimeStatus != "stopped" {
		t.Fatalf("installed plugin must remain disabled and stopped: %#v", installed)
	}
	if installed.SignatureStatus != "dev" {
		t.Fatalf("installed dev package signature status = %q", installed.SignatureStatus)
	}
	enabled, err := bridge.EnablePlugin(PluginEnableRequest{PluginID: manifest.ID})
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
	actions := make(map[string]int, len(events))
	for _, event := range events {
		actions[event.Action]++
	}
	if err != nil || len(events) != 4 || actions["plugin.install"] != 1 || actions["plugin.enable"] != 1 ||
		actions["plugin.stop"] != 1 || actions["plugin.disable"] != 1 {
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

// signedPluginPackage writes a package whose manifest is signed by key with
// the given key id. It returns the package directory.
func signedPluginPackage(t *testing.T, root, keyID string, private ed25519.PrivateKey, permissions []plugins.PermissionDeclaration) string {
	t.Helper()
	packageDirectory := filepath.Join(root, "signed-package")
	if err := os.MkdirAll(packageDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(filepath.Join(packageDirectory, "run"), executable, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(executable)
	manifest := plugins.Manifest{
		SchemaVersion: plugins.ManifestSchemaVersion, ID: "dev.yuri.signed", Name: "Signed", Version: "0.1.0",
		Publisher: "OrdoAI", Executable: "run", SupportedOS: []string{runtime.GOOS}, SupportedArch: []string{runtime.GOARCH},
		ProtocolVersion: plugins.ProtocolVersion,
		Checksum:        &plugins.ChecksumMetadata{Algorithm: "sha256", Value: hex.EncodeToString(digest[:])},
		Tools: []plugins.ToolDeclaration{{
			ID: "run", Name: "Run", InputSchema: json.RawMessage(`{"type":"object"}`),
			OutputSchema: json.RawMessage(`{"type":"object"}`), Risk: domain.RiskLow,
		}},
		Permissions: permissions,
	}
	unsigned, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if private != nil {
		signature, signErr := plugins.SignManifest(private, unsigned)
		if signErr != nil {
			t.Fatal(signErr)
		}
		manifest.Signature = &plugins.SignatureMetadata{Algorithm: "ed25519", KeyID: keyID, Value: signature}
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDirectory, plugins.ManifestFileName), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return packageDirectory
}

func newPluginTestBridge(t *testing.T, root string) *Bridge {
	t.Helper()
	database, err := storage.Open(context.Background(), filepath.Join(root, "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	return &Bridge{
		logger:       observability.NewLogger(observability.LoggerOptions{Level: slog.LevelError, Format: "json", Output: io.Discard}),
		repositories: repositories,
		paths:        config.Paths{PluginDirectory: filepath.Join(root, "plugins")},
		config:       config.Config{Version: 1, Locale: "ru-RU", LogLevel: "info", DataDirectory: root},
		appCtx:       context.Background(),
		// Initialized so a StartPlugin assertion fails on the security gate
		// rather than on an uninitialized supervisor map: a test that only
		// proves "StartPlugin returned some error" would pass even with the
		// verification gate removed.
		backgroundCtx:     context.Background(),
		pluginSupervisors: map[string]*plugins.Supervisor{},
	}
}

// The renderer used to be able to bypass the global dev-mode switch with a
// single field on the install request. The bridge request type no longer has
// that field, so the same payload is now inert.
func TestInstallPluginIgnoresPerRequestUnsignedBypass(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	packageDirectory := signedPluginPackage(t, root, "", nil, nil)

	var request PluginPathRequest
	payload := []byte(`{"path":` + strconv.Quote(packageDirectory) + `,"devMode":true,"allowUnsigned":true}`)
	if err := json.Unmarshal(payload, &request); err != nil {
		t.Fatal(err)
	}
	inspection, err := bridge.InspectPluginPackage(request)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Installable || !inspection.RequiresDevMode || inspection.SignatureStatus != plugins.TrustUnsigned {
		t.Fatalf("allowUnsigned still influences inspection: %#v", inspection)
	}
	if _, err := bridge.InstallPlugin(request); err == nil {
		t.Fatal("allowUnsigned bypassed the global plugin dev-mode switch")
	}
}

func TestSignedPluginPackageTrustLifecycle(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	packageDirectory := signedPluginPackage(t, root, "publisher-1", private, nil)

	// Signed, but the publisher key is not in the local trust store yet.
	inspection, err := bridge.InspectPluginPackage(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SignatureStatus != plugins.TrustUnverified || inspection.Installable {
		t.Fatalf("untrusted publisher inspection = %#v", inspection)
	}
	if _, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory}); err == nil {
		t.Fatal("package signed by an untrusted publisher was installed")
	}

	if _, err := bridge.TrustPluginPublisher(PluginPublisherKeyRequest{
		KeyID: "publisher-1", PublicKey: base64.StdEncoding.EncodeToString(public), Publisher: "OrdoAI",
	}); err != nil {
		t.Fatal(err)
	}
	publishers, err := bridge.ListPluginPublishers()
	if err != nil || len(publishers) != 1 || publishers[0].KeyID != "publisher-1" {
		t.Fatalf("trusted publishers = %#v, %v", publishers, err)
	}

	inspection, err = bridge.InspectPluginPackage(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SignatureStatus != plugins.TrustVerified || !inspection.Installable || inspection.RequiresDevMode {
		t.Fatalf("trusted publisher inspection = %#v", inspection)
	}
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatalf("install verified package without dev mode: %v", err)
	}
	if installed.SignatureStatus != plugins.TrustVerified {
		t.Fatalf("installed signature status = %q", installed.SignatureStatus)
	}

	// The installed package must still verify at start time.
	record, err := bridge.repositories.Plugins.Get(context.Background(), domain.ID(installed.ID))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := bridge.verifyInstalledPlugin(record)
	if err != nil || !decision.Verified() {
		t.Fatalf("verify installed plugin = %#v, %v", decision, err)
	}

	// Swapping the payload after installation breaks the checksum the
	// signature commits to, so the plugin refuses to start.
	installedExecutable := filepath.Join(record.InstallPath, "run")
	if err := os.WriteFile(installedExecutable, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.verifyInstalledPlugin(record); err == nil {
		t.Fatal("tampered payload passed start-time verification")
	}
	if _, err := bridge.StartPlugin(PluginIDRequest{ID: installed.ID}); err == nil {
		t.Fatal("tampered payload was started")
	}
}

func TestPluginPackageSignedByUntrustedKeyStaysUnverified(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	trustedPublic, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, attackerPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.TrustPluginPublisher(PluginPublisherKeyRequest{
		KeyID: "publisher-1", PublicKey: base64.StdEncoding.EncodeToString(trustedPublic), Publisher: "OrdoAI",
	}); err != nil {
		t.Fatal(err)
	}
	// The attacker signs with their own key but claims the trusted key id.
	packageDirectory := signedPluginPackage(t, root, "publisher-1", attackerPrivate, nil)
	inspection, err := bridge.InspectPluginPackage(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SignatureStatus != plugins.TrustUnverified || inspection.Installable {
		t.Fatalf("forged signature inspection = %#v", inspection)
	}
}

// The key M-31 assertion: a capability the manifest declares but the owner did
// not consent to must be denied at invocation time.
func TestEnablePluginGrantsOnlyConsentedCapabilities(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	bridge.config.PluginDevMode = true
	packageDirectory := signedPluginPackage(t, root, "", nil, []plugins.PermissionDeclaration{
		{Capability: string(domain.CapabilityNotificationsSend), Scope: json.RawMessage(`{"kind":"unrestricted"}`), Reason: "notify"},
		{Capability: string(domain.CapabilityExternalSend), Scope: json.RawMessage(`{"kind":"unrestricted"}`), Reason: "send"},
	})
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityNotificationsSend), AllowUnrestricted: true,
	}}})
	if err != nil || !enabled.Enabled {
		t.Fatalf("enable with partial consent = %#v, %v", enabled, err)
	}
	for _, permission := range enabled.Permissions {
		wantGranted := permission.Capability == string(domain.CapabilityNotificationsSend)
		if permission.Granted != wantGranted {
			t.Fatalf("permission %q granted=%v, want %v", permission.Capability, permission.Granted, wantGranted)
		}
	}
	authorizer := pluginGrantAuthorizer{repository: bridge.repositories.Plugins}
	allowed, err := authorizer.Authorize(context.Background(), plugins.AuthorizationRequest{
		PluginID: installed.ID, ToolID: "run", Capability: string(domain.CapabilityNotificationsSend),
		Scope: json.RawMessage(`{"kind":"unrestricted"}`),
	})
	if err != nil || !allowed.Allowed {
		t.Fatalf("consented capability = %#v, %v", allowed, err)
	}
	denied, err := authorizer.Authorize(context.Background(), plugins.AuthorizationRequest{
		PluginID: installed.ID, ToolID: "run", Capability: string(domain.CapabilityExternalSend),
		Scope: json.RawMessage(`{"kind":"unrestricted"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Allowed {
		t.Fatal("manifest declaration alone authorized external.send without owner consent")
	}
}

func TestEnablePluginRequiresExplicitUnrestrictedConfirmation(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	bridge.config.PluginDevMode = true
	packageDirectory := signedPluginPackage(t, root, "", nil, []plugins.PermissionDeclaration{
		{Capability: string(domain.CapabilityExternalSend), Scope: json.RawMessage(`{"kind":"unrestricted"}`), Reason: "send"},
	})
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityExternalSend),
	}}}); err == nil {
		t.Fatal("unrestricted grant was accepted without explicit confirmation")
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityMemoryWrite), AllowUnrestricted: true,
	}}}); err == nil {
		t.Fatal("consent for an undeclared capability was accepted")
	}
}

func TestScopeCoversRejectsBroaderRequest(t *testing.T) {
	granted := domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{"/tmp/project"}}
	if !scopeCovers(granted, domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{"/tmp/project/notes.md"}}) {
		t.Fatal("a path inside the granted directory must be covered")
	}
	if scopeCovers(granted, domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{"/tmp"}}) {
		t.Fatal("a narrower grant must not satisfy a broader request")
	}
	if scopeCovers(granted, domain.CapabilityScope{Kind: domain.ScopeUnrestricted}) {
		t.Fatal("a scoped grant must not satisfy an unrestricted request")
	}
	if scopeCovers(granted, domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"example.com"}}) {
		t.Fatal("scope kinds must match")
	}
	if !scopeCovers(domain.UnrestrictedScope(), domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"example.com"}}) {
		t.Fatal("an unrestricted grant covers any request")
	}
	network := domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"*.example.com"}}
	if !scopeCovers(network, domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"api.example.com"}}) {
		t.Fatal("wildcard host must cover a subdomain")
	}
	if scopeCovers(network, domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"example.com.evil.test"}}) {
		t.Fatal("wildcard host must not cover a suffix impostor")
	}
}

func TestEnablePluginNarrowedScopeConstrainsAuthorization(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	bridge.config.PluginDevMode = true
	packageDirectory := signedPluginPackage(t, root, "", nil, []plugins.PermissionDeclaration{
		{Capability: string(domain.CapabilityFilesystemRead), Scope: json.RawMessage(`{"kind":"filesystem","values":["/tmp"]}`), Reason: "read"},
	})
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityFilesystemRead), ScopeKind: "filesystem", ScopeValues: []string{"/"},
	}}}); err == nil {
		t.Fatal("consent widened the manifest declaration")
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityFilesystemRead), ScopeKind: "filesystem", ScopeValues: []string{"/tmp/project"},
	}}}); err != nil {
		t.Fatal(err)
	}
	authorizer := pluginGrantAuthorizer{repository: bridge.repositories.Plugins}
	denied, err := authorizer.Authorize(context.Background(), plugins.AuthorizationRequest{
		PluginID: installed.ID, Capability: string(domain.CapabilityFilesystemRead),
		Scope: json.RawMessage(`{"kind":"filesystem","values":["/tmp"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Allowed {
		t.Fatal("the full declared scope was authorized by a narrowed grant")
	}
	allowed, err := authorizer.Authorize(context.Background(), plugins.AuthorizationRequest{
		PluginID: installed.ID, Capability: string(domain.CapabilityFilesystemRead),
		Scope: json.RawMessage(`{"kind":"filesystem","values":["/tmp/project/sub"]}`),
	})
	if err != nil || !allowed.Allowed {
		t.Fatalf("a request inside the granted scope = %#v, %v", allowed, err)
	}
}

func TestEnablePluginExpiringGrantStopsAuthorizing(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	bridge.config.PluginDevMode = true
	packageDirectory := signedPluginPackage(t, root, "", nil, []plugins.PermissionDeclaration{
		{Capability: string(domain.CapabilityNotificationsSend), Scope: json.RawMessage(`{"kind":"unrestricted"}`), Reason: "notify"},
	})
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityNotificationsSend), AllowUnrestricted: true, ExpiresInHours: 1,
	}}}); err != nil {
		t.Fatal(err)
	}
	grants, err := bridge.repositories.Plugins.Grants(context.Background(), domain.ID(installed.ID))
	if err != nil || len(grants) != 1 || grants[0].ExpiresAt.IsZero() {
		t.Fatalf("expiring grant = %#v, %v", grants, err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityNotificationsSend), AllowUnrestricted: true, ExpiresInHours: maxPluginGrantHours + 1,
	}}}); err == nil {
		t.Fatal("an unbounded grant lifetime was accepted")
	}
}

// A manifest declaring a wildcard value is an unrestricted grant wearing a
// scoped costume. M-31 requires an unbounded grant to be its own decision, so
// the wildcard must hit the same confirmation gate as kind "unrestricted".
func TestEnablePluginRequiresUnrestrictedConsentForWildcardValues(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		declaration json.RawMessage
	}{
		{name: "network", declaration: json.RawMessage(`{"kind":"network","values":["*"]}`)},
		{name: "resource", declaration: json.RawMessage(`{"kind":"resource","values":["*"]}`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			bridge := newPluginTestBridge(t, root)
			bridge.config.PluginDevMode = true
			packageDirectory := signedPluginPackage(t, root, "", nil, []plugins.PermissionDeclaration{
				{Capability: string(domain.CapabilityNotificationsSend), Scope: testCase.declaration, Reason: "notify"},
			})
			installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
				Capability: string(domain.CapabilityNotificationsSend),
			}}}); err == nil {
				t.Fatal("a wildcard scope was granted without an explicit unrestricted confirmation")
			}
			if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
				Capability: string(domain.CapabilityNotificationsSend), AllowUnrestricted: true,
			}}}); err != nil {
				t.Fatalf("confirmed wildcard consent was rejected: %v", err)
			}
		})
	}
}

// The resource kind is an opaque identifier. internal/security/policy.go
// compares it for equality; the plugin authorizer must not be more permissive
// than the policy evaluator for the same scope, and an unknown kind must fail
// closed rather than fall into a wildcard branch.
func TestScopeCoversDoesNotInventAResourceWildcard(t *testing.T) {
	wildcard := domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"*"}}
	if scopeCovers(wildcard, domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"calendar.write"}}) {
		t.Fatal(`a literal "*" resource grant must not cover an unrelated resource`)
	}
	exact := domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"calendar.write"}}
	if !scopeCovers(exact, domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"calendar.write"}}) {
		t.Fatal("an exact resource grant must cover the same resource")
	}
	if scopeCovers(exact, domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"calendar.delete"}}) {
		t.Fatal("a resource grant must not cover a different resource")
	}
	unknown := domain.CapabilityScope{Kind: domain.ScopeKind("device"), Values: []string{"camera"}}
	if scopeCovers(unknown, domain.CapabilityScope{Kind: domain.ScopeKind("device"), Values: []string{"camera"}}) {
		t.Fatal("an unrecognized scope kind must fail closed")
	}
}

// H-14: verification is re-run from the installed bytes at every start, so
// revoking the publisher key stops the plugin at the next start even though
// the record still says "verified" from install time.
func TestRevokedPublisherStopsAVerifiedPluginFromStarting(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	packageDirectory := signedPluginPackage(t, root, "publisher-1", private, nil)
	if _, err := bridge.TrustPluginPublisher(PluginPublisherKeyRequest{
		KeyID: "publisher-1", PublicKey: base64.StdEncoding.EncodeToString(public), Publisher: "OrdoAI",
	}); err != nil {
		t.Fatal(err)
	}
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.RevokePluginPublisher(PluginPublisherKeyRequest{KeyID: "publisher-1"}); err != nil {
		t.Fatal(err)
	}
	_, err = bridge.StartPlugin(PluginIDRequest{ID: installed.ID})
	if err == nil {
		t.Fatal("a plugin whose publisher key was revoked was started")
	}
	if !strings.Contains(err.Error(), "signature is not verified") {
		t.Fatalf("start was refused for the wrong reason: %v", err)
	}
	record, err := bridge.repositories.Plugins.Get(context.Background(), domain.ID(installed.ID))
	if err != nil {
		t.Fatal(err)
	}
	if record.RuntimeStatus != "failed" {
		t.Fatalf("runtime status after a failed verification = %q, want %q", record.RuntimeStatus, "failed")
	}
	// The install-time record still claims "verified"; only the live
	// re-verification may be trusted to gate a start.
	if record.SignatureStatus != plugins.TrustVerified {
		t.Fatalf("install-time signature status = %q", record.SignatureStatus)
	}
}

// H-14: the start-time check must read the manifest off disk. Editing the
// installed plugin.json leaves the executable checksum intact, so only a
// signature computed over the loaded manifest bytes can catch it.
func TestTamperedInstalledManifestFailsStartVerification(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	packageDirectory := signedPluginPackage(t, root, "publisher-1", private, nil)
	if _, err := bridge.TrustPluginPublisher(PluginPublisherKeyRequest{
		KeyID: "publisher-1", PublicKey: base64.StdEncoding.EncodeToString(public), Publisher: "OrdoAI",
	}); err != nil {
		t.Fatal(err)
	}
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID}); err != nil {
		t.Fatal(err)
	}
	record, err := bridge.repositories.Plugins.Get(context.Background(), domain.ID(installed.ID))
	if err != nil {
		t.Fatal(err)
	}
	if decision, decisionErr := bridge.verifyInstalledPlugin(record); decisionErr != nil || !decision.Verified() {
		t.Fatalf("baseline installed verification = %#v, %v", decision, decisionErr)
	}

	manifestPath := filepath.Join(record.InstallPath, plugins.ManifestFileName)
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	document["name"] = "Renamed After Install"
	tampered, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	// The executable is untouched, so the checksum still matches and only the
	// signature over the manifest bytes can reject this package.
	if err := plugins.Manifest.VerifyChecksum(mustLoadInstalledManifest(t, record.InstallPath), record.InstallPath); err != nil {
		t.Fatalf("the payload checksum must still pass for this assertion to mean anything: %v", err)
	}
	if _, err := bridge.verifyInstalledPlugin(record); err == nil {
		t.Fatal("a tampered installed manifest passed start-time verification")
	}
	_, err = bridge.StartPlugin(PluginIDRequest{ID: installed.ID})
	if err == nil {
		t.Fatal("a plugin with a tampered manifest was started")
	}
	if !strings.Contains(err.Error(), "signature is not verified") {
		t.Fatalf("start was refused for the wrong reason: %v", err)
	}
}

func mustLoadInstalledManifest(t *testing.T, directory string) plugins.Manifest {
	t.Helper()
	manifest, _, err := loadManifestFromDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}
