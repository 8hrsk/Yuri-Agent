package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

const (
	pluginCoreVersion      = "0.4.0"
	maxPluginManifestBytes = 1024 * 1024
)

type PluginPermissionDTO struct {
	Capability  string   `json:"capability"`
	Scope       string   `json:"scope,omitempty"`
	Values      []string `json:"values,omitempty"`
	Description string   `json:"description,omitempty"`
	Granted     bool     `json:"granted"`
}

type PluginToolDTO struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Risk         string   `json:"risk"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type PluginDTO struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	Publisher       string                `json:"publisher"`
	Version         string                `json:"version"`
	ProtocolVersion string                `json:"protocol_version"`
	Enabled         bool                  `json:"enabled"`
	RuntimeStatus   string                `json:"runtime_status"`
	Status          string                `json:"status"`
	Running         bool                  `json:"running"`
	SignatureStatus string                `json:"signature_status"`
	Checksum        string                `json:"checksum,omitempty"`
	RepositoryURL   string                `json:"repository_url,omitempty"`
	InstallPath     string                `json:"install_path,omitempty"`
	ReleaseTag      string                `json:"release_tag,omitempty"`
	SourceCommit    string                `json:"source_commit,omitempty"`
	LastError       string                `json:"last_error,omitempty"`
	Permissions     []PluginPermissionDTO `json:"permissions"`
	Tools           []PluginToolDTO       `json:"tools"`
	EventSources    []string              `json:"event_sources"`
	InstalledAt     string                `json:"installed_at,omitempty"`
	UpdatedAt       string                `json:"updated_at,omitempty"`
}

type PluginPackageInspection struct {
	Path            string     `json:"path"`
	Valid           bool       `json:"valid"`
	Compatible      bool       `json:"compatible"`
	Manifest        *PluginDTO `json:"manifest,omitempty"`
	SignatureStatus string     `json:"signature_status"`
	Checksum        string     `json:"checksum,omitempty"`
	Warnings        []string   `json:"warnings"`
	Errors          []string   `json:"errors"`
	ExecutablePath  string     `json:"executable_path,omitempty"`
	Installable     bool       `json:"installable"`
	RequiresDevMode bool       `json:"requires_dev_mode"`
}

type PluginPathRequest struct {
	Path          string `json:"path"`
	DevMode       bool   `json:"devMode,omitempty"`
	AllowUnsigned bool   `json:"allowUnsigned,omitempty"`
}

type PluginIDRequest struct {
	ID       string `json:"id"`
	PluginID string `json:"pluginId,omitempty"`
}

// ListPlugins returns durable metadata. Running status is refreshed by the
// process supervisor when a plugin is started or stopped.
func (b *Bridge) ListPlugins() ([]PluginDTO, error) {
	ctx, cancel := b.context()
	defer cancel()
	records, err := b.repositories.Plugins.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]PluginDTO, 0, len(records))
	for _, record := range records {
		manifest, err := decodeManifest([]byte(record.ManifestJSON))
		if err != nil {
			return nil, fmt.Errorf("decode installed plugin %q: %w", record.ID, err)
		}
		dto := pluginDTO(record, manifest)
		b.applyPluginGrantStatus(ctx, record.ID, &dto)
		result = append(result, dto)
	}
	return result, nil
}

func (b *Bridge) InspectPluginPackage(request PluginPathRequest) (PluginPackageInspection, error) {
	inspection, _, err := b.inspectPluginPackage(request.Path, request.DevMode || request.AllowUnsigned)
	return inspection, err
}

func (b *Bridge) inspectPluginPackage(packagePath string, requestDevMode bool) (PluginPackageInspection, plugins.Manifest, error) {
	root, err := canonicalPluginPackage(packagePath)
	if err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, err
	}
	content, err := readBoundedFile(filepath.Join(root, plugins.ManifestFileName), maxPluginManifestBytes)
	if err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, fmt.Errorf("read plugin manifest: %w", err)
	}
	manifest, err := decodeManifest(content)
	if err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, err
	}
	executable, err := manifest.ResolveExecutable(root)
	if err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, err
	}
	if err := manifest.VerifyChecksum(root); err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, err
	}
	compatible := manifest.CompatibleWithCore(pluginCoreVersion)
	signatureStatus, requiresDevMode, notice := packageTrust(manifest)
	now := time.Now().UTC()
	record := storage.PluginRecord{
		ID: domain.ID(manifest.ID), Name: manifest.Name, Publisher: manifest.Publisher,
		Version: manifest.Version, ProtocolVersion: manifest.ProtocolVersion,
		ManifestJSON: string(content), SignatureStatus: signatureStatus, Checksum: pluginChecksum(manifest),
		RuntimeStatus: "stopped", InstalledAt: now, UpdatedAt: now,
	}
	dto := pluginDTO(record, manifest)
	inspection := PluginPackageInspection{
		Path: root, Valid: true, Manifest: &dto, SignatureStatus: signatureStatus,
		Checksum: pluginChecksum(manifest), Warnings: []string{}, Errors: []string{}, ExecutablePath: executable,
		Compatible: compatible, Installable: compatible && (!requiresDevMode || b.pluginDevMode() || requestDevMode),
		RequiresDevMode: requiresDevMode,
	}
	if notice != "" {
		inspection.Warnings = append(inspection.Warnings, notice)
	}
	if !compatible {
		inspection.Errors = append(inspection.Errors, "Версия плагина несовместима с текущей версией Yuri.")
	}
	return inspection, manifest, nil
}

func (b *Bridge) InstallPlugin(request PluginPathRequest) (PluginDTO, error) {
	requestDevMode := request.DevMode || request.AllowUnsigned
	inspection, manifest, err := b.inspectPluginPackage(request.Path, requestDevMode)
	if err != nil {
		return PluginDTO{}, err
	}
	if !inspection.Installable {
		if inspection.RequiresDevMode && !b.pluginDevMode() && !requestDevMode {
			return PluginDTO{}, errors.New("unsigned or unverified plugins require explicit plugin dev mode")
		}
		return PluginDTO{}, errors.New("plugin package is not compatible with this Yuri version")
	}
	ctx, cancel := b.context()
	defer cancel()
	if existing, getErr := b.repositories.Plugins.Get(ctx, domain.ID(manifest.ID)); getErr == nil {
		return PluginDTO{}, fmt.Errorf("plugin %q is already installed at version %s", manifest.ID, existing.Version)
	} else if !errors.Is(getErr, domain.ErrNotFound) {
		return PluginDTO{}, getErr
	}
	destination := filepath.Join(b.paths.PluginDirectory, manifest.ID, manifest.Version)
	if err := installPluginDirectory(inspection.Path, destination); err != nil {
		return PluginDTO{}, err
	}
	installedManifest, installedContent, err := loadManifestFromDirectory(destination)
	if err != nil {
		_ = removeOwnedPluginDirectory(b.paths.PluginDirectory, manifest.ID, manifest.Version)
		return PluginDTO{}, fmt.Errorf("verify installed plugin: %w", err)
	}
	if err := installedManifest.VerifyChecksum(destination); err != nil {
		_ = removeOwnedPluginDirectory(b.paths.PluginDirectory, manifest.ID, manifest.Version)
		return PluginDTO{}, fmt.Errorf("verify installed checksum: %w", err)
	}
	now := time.Now().UTC()
	installedSignatureStatus := inspection.SignatureStatus
	if inspection.RequiresDevMode && (b.pluginDevMode() || requestDevMode) {
		installedSignatureStatus = "dev"
	}
	record := storage.PluginRecord{
		ID: domain.ID(manifest.ID), Name: manifest.Name, Publisher: manifest.Publisher,
		Version: manifest.Version, ProtocolVersion: manifest.ProtocolVersion, Enabled: false,
		InstallPath: destination, ManifestJSON: string(installedContent),
		SignatureStatus: installedSignatureStatus, Checksum: pluginChecksum(manifest),
		RuntimeStatus: "stopped", InstalledAt: now, UpdatedAt: now,
	}
	source := storage.PluginSource{
		PluginID: record.ID, RepositoryURL: pluginRepositoryURL(manifest),
		ReleaseTag: pluginReleaseTag(manifest), CommitSHA: pluginSourceCommit(manifest),
		Checksum: pluginChecksum(manifest), CheckedAt: now,
	}
	if err := b.repositories.Plugins.Upsert(ctx, record, source); err != nil {
		_ = removeOwnedPluginDirectory(b.paths.PluginDirectory, manifest.ID, manifest.Version)
		return PluginDTO{}, err
	}
	if err := b.appendPluginAudit(ctx, "plugin.install", record.ID, domain.PermissionAllow, installedSignatureStatus); err != nil {
		b.logger.WarnContext(ctx, "append plugin install audit", "plugin_id", record.ID, "error", err)
	}
	return pluginDTO(record, manifest), nil
}

func (b *Bridge) EnablePlugin(request PluginIDRequest) (PluginDTO, error) {
	ctx, cancel := b.context()
	defer cancel()
	record, manifest, err := b.installedPlugin(ctx, request.pluginID())
	if err != nil {
		return PluginDTO{}, err
	}
	grants := make([]domain.PermissionGrant, 0, len(manifest.Permissions))
	for _, declaration := range manifest.Permissions {
		capability := domain.NormalizeCapabilityName(declaration.Capability)
		declarationScope, err := pluginDomainScope(declaration.Scope)
		if err != nil {
			return PluginDTO{}, fmt.Errorf("permission %q: %w", declaration.Capability, err)
		}
		kind := declarationScope.Kind
		if kind == "" {
			kind = domain.ScopeUnrestricted
		}
		id, err := domain.NewID("grant")
		if err != nil {
			return PluginDTO{}, err
		}
		grant := domain.PermissionGrant{
			ID: id, SubjectID: record.ID, Capability: capability,
			Scope:     domain.CapabilityScope{Kind: kind, Values: append([]string(nil), declarationScope.Values...)},
			GrantedAt: time.Now().UTC(),
		}
		if !grant.Valid() {
			return PluginDTO{}, fmt.Errorf("invalid requested permission %q", declaration.Capability)
		}
		grants = append(grants, grant)
	}
	if err := b.repositories.Plugins.ReplaceGrants(ctx, record.ID, grants); err != nil {
		return PluginDTO{}, err
	}
	now := time.Now().UTC()
	if err := b.repositories.Plugins.SetEnabled(ctx, record.ID, true, now); err != nil {
		_ = b.repositories.Plugins.ReplaceGrants(context.Background(), record.ID, nil)
		return PluginDTO{}, err
	}
	record.Enabled, record.UpdatedAt = true, now
	if err := b.appendPluginAudit(ctx, "plugin.enable", record.ID, domain.PermissionAllow, "grants accepted"); err != nil {
		b.logger.WarnContext(ctx, "append plugin enable audit", "plugin_id", record.ID, "error", err)
	}
	return pluginDTO(record, manifest), nil
}

func (b *Bridge) DisablePlugin(request PluginIDRequest) (PluginDTO, error) {
	ctx, cancel := b.context()
	defer cancel()
	record, manifest, err := b.installedPlugin(ctx, request.pluginID())
	if err != nil {
		return PluginDTO{}, err
	}
	if _, err := b.StopPlugin(request); err != nil {
		return PluginDTO{}, err
	}
	if err := b.repositories.Plugins.ReplaceGrants(ctx, record.ID, nil); err != nil {
		return PluginDTO{}, err
	}
	now := time.Now().UTC()
	if err := b.repositories.Plugins.SetEnabled(ctx, record.ID, false, now); err != nil {
		return PluginDTO{}, err
	}
	record.Enabled, record.RuntimeStatus, record.UpdatedAt = false, "stopped", now
	if err := b.appendPluginAudit(ctx, "plugin.disable", record.ID, domain.PermissionAllow, "grants revoked"); err != nil {
		b.logger.WarnContext(ctx, "append plugin disable audit", "plugin_id", record.ID, "error", err)
	}
	return pluginDTO(record, manifest), nil
}

func (b *Bridge) UninstallPlugin(request PluginIDRequest) error {
	ctx, cancel := b.context()
	defer cancel()
	record, _, err := b.installedPlugin(ctx, request.pluginID())
	if err != nil {
		return err
	}
	if _, err := b.StopPlugin(request); err != nil {
		return err
	}
	expected := filepath.Join(b.paths.PluginDirectory, string(record.ID), record.Version)
	if filepath.Clean(record.InstallPath) != filepath.Clean(expected) {
		return errors.New("refusing to remove plugin outside the application-owned plugin directory")
	}
	if err := removeOwnedPluginDirectory(b.paths.PluginDirectory, string(record.ID), record.Version); err != nil {
		return fmt.Errorf("remove installed plugin: %w", err)
	}
	if err := b.repositories.Plugins.Delete(ctx, record.ID); err != nil {
		return err
	}
	_ = os.Remove(filepath.Dir(expected))
	if err := b.appendPluginAudit(ctx, "plugin.uninstall", record.ID, domain.PermissionAllow, "removed"); err != nil {
		b.logger.WarnContext(ctx, "append plugin uninstall audit", "plugin_id", record.ID, "error", err)
	}
	return nil
}

func (b *Bridge) StartPlugin(request PluginIDRequest) (PluginDTO, error) {
	ctx, cancel := b.context()
	defer cancel()
	record, manifest, err := b.installedPlugin(ctx, request.pluginID())
	if err != nil {
		return PluginDTO{}, err
	}
	if !record.Enabled {
		return PluginDTO{}, errors.New("plugin must be enabled before it can start")
	}
	b.mu.Lock()
	supervisor := b.pluginSupervisors[string(record.ID)]
	b.mu.Unlock()
	if supervisor == nil {
		effectiveGrants, grantsErr := b.pluginEffectiveGrants(ctx, record.ID)
		if grantsErr != nil {
			return PluginDTO{}, grantsErr
		}
		supervisor, err = plugins.NewSupervisor(plugins.SupervisorConfig{
			Manifest: manifest, PackageDir: record.InstallPath, CoreVersion: pluginCoreVersion,
			Authorizer: pluginGrantAuthorizer{repository: b.repositories.Plugins}, EffectiveGrants: effectiveGrants,
			DevMode: record.SignatureStatus == "dev" || record.SignatureStatus == "unsigned" || record.SignatureStatus == "unverified",
			Restart: plugins.RestartPolicy{MaxAttempts: 2, InitialBackoff: 250 * time.Millisecond, MaxBackoff: 2 * time.Second},
		})
		if err != nil {
			return PluginDTO{}, err
		}
		b.mu.Lock()
		if existing := b.pluginSupervisors[string(record.ID)]; existing != nil {
			supervisor = existing
		} else {
			b.pluginSupervisors[string(record.ID)] = supervisor
		}
		b.mu.Unlock()
	}
	now := time.Now().UTC()
	_ = b.repositories.Plugins.SetRuntimeStatus(ctx, record.ID, "starting", "", now)
	startCtx, startCancel := context.WithTimeout(ctx, 10*time.Second)
	err = supervisor.Start(startCtx)
	startCancel()
	if err != nil {
		_ = b.repositories.Plugins.SetRuntimeStatus(context.Background(), record.ID, "failed", safeError(err.Error()), time.Now().UTC())
		return PluginDTO{}, err
	}
	record.RuntimeStatus, record.LastError, record.UpdatedAt = "running", "", time.Now().UTC()
	if err := b.repositories.Plugins.SetRuntimeStatus(ctx, record.ID, record.RuntimeStatus, "", record.UpdatedAt); err != nil {
		_ = supervisor.Stop(context.Background())
		return PluginDTO{}, err
	}
	b.watchPlugin(record.ID, supervisor, manifest)
	if err := b.appendPluginAudit(ctx, "plugin.start", record.ID, domain.PermissionAllow, "running"); err != nil {
		b.logger.WarnContext(ctx, "append plugin start audit", "plugin_id", record.ID, "error", err)
	}
	return pluginDTO(record, manifest), nil
}

func (b *Bridge) StopPlugin(request PluginIDRequest) (PluginDTO, error) {
	ctx, cancel := b.context()
	defer cancel()
	record, manifest, err := b.installedPlugin(ctx, request.pluginID())
	if err != nil {
		return PluginDTO{}, err
	}
	b.mu.Lock()
	supervisor := b.pluginSupervisors[string(record.ID)]
	delete(b.pluginSupervisors, string(record.ID))
	b.mu.Unlock()
	if supervisor != nil {
		stopCtx, stopCancel := context.WithTimeout(ctx, 3*time.Second)
		err = supervisor.Stop(stopCtx)
		stopCancel()
		if err != nil && !errors.Is(err, plugins.ErrPluginExited) {
			return PluginDTO{}, err
		}
	}
	record.RuntimeStatus, record.LastError, record.UpdatedAt = "stopped", "", time.Now().UTC()
	if err := b.repositories.Plugins.SetRuntimeStatus(ctx, record.ID, record.RuntimeStatus, "", record.UpdatedAt); err != nil {
		return PluginDTO{}, err
	}
	if err := b.appendPluginAudit(ctx, "plugin.stop", record.ID, domain.PermissionAllow, "stopped"); err != nil {
		b.logger.WarnContext(ctx, "append plugin stop audit", "plugin_id", record.ID, "error", err)
	}
	return pluginDTO(record, manifest), nil
}

func (b *Bridge) restoreEnabledPlugins() {
	b.background.Add(1)
	go func() {
		defer b.background.Done()
		ctx, cancel := context.WithTimeout(b.backgroundCtx, 30*time.Second)
		defer cancel()
		records, err := b.repositories.Plugins.List(ctx)
		if err != nil {
			b.logger.ErrorContext(ctx, "restore enabled plugins", "error", err)
			return
		}
		for _, record := range records {
			if !record.Enabled {
				continue
			}
			if _, err := b.StartPlugin(PluginIDRequest{ID: string(record.ID)}); err != nil {
				b.logger.ErrorContext(ctx, "restore plugin", "plugin_id", record.ID, "error", err)
			}
		}
	}()
}

func (b *Bridge) watchPlugin(id domain.ID, supervisor *plugins.Supervisor, manifest plugins.Manifest) {
	events, err := supervisor.Events()
	if err != nil {
		return
	}
	b.background.Add(1)
	go func() {
		defer b.background.Done()
		for event := range events {
			var payload pluginEventPayload
			if event.Type != plugins.MessageEvent || json.Unmarshal(event.Payload, &payload) != nil {
				b.logger.Warn("drop invalid plugin event", "plugin_id", id)
				continue
			}
			source, declared := declaredPluginEvent(manifest, payload.Source)
			if !declared || !b.pluginEventAllowed(context.Background(), id, source) {
				b.logger.Warn("drop undeclared plugin event", "plugin_id", id, "source", payload.Source)
				continue
			}
			b.logger.Info("plugin event", "plugin_id", id, "source", payload.Source, "event_type", payload.EventType)
			if err := b.appendPluginEventAudit(context.Background(), id, payload); err != nil {
				b.logger.Warn("audit plugin event", "plugin_id", id, "error", err)
			}
		}
		state, stateErr := supervisor.State()
		deadline := time.Now().Add(6 * time.Second)
		for state == plugins.StateCrashed && time.Now().Before(deadline) {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-timer.C:
			case <-b.backgroundCtx.Done():
				timer.Stop()
				return
			}
			state, stateErr = supervisor.State()
		}
		if state == plugins.StateRunning {
			_ = b.repositories.Plugins.SetRuntimeStatus(context.Background(), id, "running", "", time.Now().UTC())
			b.watchPlugin(id, supervisor, manifest)
			return
		}
		if state == plugins.StateCrashed || state == plugins.StateFailed {
			message := "plugin process exited unexpectedly"
			if stateErr != nil {
				message = safeError(stateErr.Error())
			}
			_ = b.repositories.Plugins.SetRuntimeStatus(context.Background(), id, string(state), message, time.Now().UTC())
		}
	}()
}

type pluginEventPayload struct {
	Source     string          `json:"source"`
	EventType  string          `json:"event_type"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

func declaredPluginEvent(manifest plugins.Manifest, sourceID string) (plugins.EventSource, bool) {
	for _, source := range manifest.EventSources {
		if sourceID == source.ID {
			return source, true
		}
	}
	return plugins.EventSource{}, false
}

func (b *Bridge) pluginEventAllowed(ctx context.Context, id domain.ID, source plugins.EventSource) bool {
	if len(source.Permissions) == 0 {
		return true
	}
	grants, err := b.repositories.Plugins.Grants(ctx, id)
	if err != nil {
		return false
	}
	now := time.Now().UTC()
	for _, required := range source.Permissions {
		found := false
		for _, grant := range grants {
			if grant.Capability == domain.NormalizeCapabilityName(required) && (grant.ExpiresAt.IsZero() || now.Before(grant.ExpiresAt)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (b *Bridge) appendPluginEventAudit(ctx context.Context, id domain.ID, event pluginEventPayload) error {
	auditID, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"source": event.Source, "event_type": event.EventType})
	return b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: auditID, Actor: domain.ActorPlugin, Action: "plugin.event", Target: string(id),
		Decision: domain.PermissionAllow, PayloadRedacted: string(payload), CreatedAt: time.Now().UTC(),
	})
}

type pluginGrantAuthorizer struct{ repository *storage.PluginRepository }

func (authorizer pluginGrantAuthorizer) Authorize(ctx context.Context, request plugins.AuthorizationRequest) (plugins.AuthorizationResult, error) {
	if authorizer.repository == nil {
		return plugins.AuthorizationResult{Allowed: false, Reason: "permission store is unavailable"}, nil
	}
	grants, err := authorizer.repository.Grants(ctx, domain.ID(request.PluginID))
	if err != nil {
		return plugins.AuthorizationResult{}, err
	}
	requestedScope, err := pluginDomainScope(request.Scope)
	if err != nil {
		return plugins.AuthorizationResult{Allowed: false, Reason: "invalid requested scope"}, nil
	}
	requestedKind := requestedScope.Kind
	if requestedKind == "" {
		requestedKind = domain.ScopeUnrestricted
	}
	for _, grant := range grants {
		if !grant.ExpiresAt.IsZero() && !time.Now().UTC().Before(grant.ExpiresAt) {
			continue
		}
		if grant.Capability != domain.NormalizeCapabilityName(request.Capability) || grant.Scope.Kind != requestedKind {
			continue
		}
		if sameStringSet(grant.Scope.Values, requestedScope.Values) {
			return plugins.AuthorizationResult{Allowed: true, Reason: "active persisted grant matches manifest scope"}, nil
		}
	}
	return plugins.AuthorizationResult{Allowed: false, Reason: "no active grant matches the declared capability scope"}, nil
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func (b *Bridge) pluginEffectiveGrants(ctx context.Context, id domain.ID) ([]plugins.PermissionGrant, error) {
	grants, err := b.repositories.Plugins.Grants(ctx, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result := make([]plugins.PermissionGrant, 0, len(grants))
	for _, grant := range grants {
		if !grant.ExpiresAt.IsZero() && !now.Before(grant.ExpiresAt) {
			continue
		}
		scope, err := json.Marshal(grant.Scope)
		if err != nil {
			return nil, err
		}
		result = append(result, plugins.PermissionGrant{
			Capability: string(grant.Capability), Scope: scope, ExpiresAt: grant.ExpiresAt,
		})
	}
	return result, nil
}

func pluginDomainScope(raw json.RawMessage) (domain.CapabilityScope, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return domain.UnrestrictedScope(), nil
	}
	var scope domain.CapabilityScope
	if err := json.Unmarshal(raw, &scope); err != nil {
		return domain.CapabilityScope{}, fmt.Errorf("decode capability scope: %w", err)
	}
	if !scope.Valid() {
		return domain.CapabilityScope{}, errors.New("capability scope is invalid")
	}
	return scope, nil
}

type pluginAgentTool struct {
	pluginID    string
	declaration plugins.ToolDeclaration
	supervisor  *plugins.Supervisor
}

func (tool pluginAgentTool) Descriptor() agent.ToolDescriptor {
	capabilities := make(domain.CapabilitySet, 0, len(tool.declaration.Permissions))
	for _, capability := range tool.declaration.Permissions {
		capabilities = append(capabilities, domain.NormalizeCapabilityName(capability))
	}
	return agent.ToolDescriptor{
		Name: pluginToolName(tool.pluginID, tool.declaration.ID), Description: tool.declaration.Description,
		InputSchema: append(json.RawMessage(nil), tool.declaration.InputSchema...),
		Risk:        tool.declaration.Risk, Capabilities: capabilities,
	}
}

func (tool pluginAgentTool) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	result, err := tool.supervisor.InvokeTool(ctx, plugins.ToolInvokeParams{
		ToolID: tool.declaration.ID, Arguments: append(json.RawMessage(nil), call.Arguments...),
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	content := string(result.Output)
	if content == "" {
		content = "{}"
	}
	return agent.ToolResult{Content: content, IsError: !result.OK || result.Error != nil, Metadata: map[string]any{"plugin_id": tool.pluginID}}, nil
}

func pluginToolName(pluginID, toolID string) string {
	sanitize := func(value string, limit int) string {
		var builder strings.Builder
		for _, char := range value {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
				builder.WriteRune(char)
			} else {
				builder.WriteByte('_')
			}
			if builder.Len() >= limit {
				break
			}
		}
		return strings.Trim(builder.String(), "_")
	}
	digest := sha256.Sum256([]byte(pluginID + "\x00" + toolID))
	return "plugin_" + sanitize(pluginID, 20) + "_" + sanitize(toolID, 20) + "_" + hex.EncodeToString(digest[:4])
}

func (request PluginIDRequest) pluginID() domain.ID {
	value := strings.TrimSpace(request.PluginID)
	if value == "" {
		value = strings.TrimSpace(request.ID)
	}
	return domain.ID(value)
}

func (b *Bridge) installedPlugin(ctx context.Context, id domain.ID) (storage.PluginRecord, plugins.Manifest, error) {
	if id.Empty() {
		return storage.PluginRecord{}, plugins.Manifest{}, errors.New("plugin id is required")
	}
	record, err := b.repositories.Plugins.Get(ctx, id)
	if err != nil {
		return storage.PluginRecord{}, plugins.Manifest{}, err
	}
	manifest, err := decodeManifest([]byte(record.ManifestJSON))
	if err != nil {
		return storage.PluginRecord{}, plugins.Manifest{}, err
	}
	return record, manifest, nil
}

func (b *Bridge) PluginDevMode() bool { return b.pluginDevMode() }

func (b *Bridge) SetPluginDevMode(enabled bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	candidate := b.config
	candidate.PluginDevMode = enabled
	if err := config.Save(b.paths, candidate); err != nil {
		return err
	}
	b.config = candidate
	return nil
}

func (b *Bridge) pluginDevMode() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.config.PluginDevMode
}

func pluginDTO(record storage.PluginRecord, manifest plugins.Manifest) PluginDTO {
	permissions := make([]PluginPermissionDTO, 0, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		scope, _ := pluginDomainScope(permission.Scope)
		permissions = append(permissions, PluginPermissionDTO{
			Capability: permission.Capability, Scope: string(scope.Kind),
			Values: append([]string(nil), scope.Values...), Description: permission.Reason,
			Granted: record.Enabled,
		})
	}
	tools := make([]PluginToolDTO, 0, len(manifest.Tools))
	for _, tool := range manifest.Tools {
		tools = append(tools, PluginToolDTO{
			ID: tool.ID, Name: tool.Name, Description: tool.Description,
			Risk: string(tool.Risk), Capabilities: append([]string(nil), tool.Permissions...),
		})
	}
	events := make([]string, 0, len(manifest.EventSources))
	for _, event := range manifest.EventSources {
		events = append(events, event.ID)
	}
	status := record.RuntimeStatus
	if !record.Enabled {
		status = "disabled"
	}
	return PluginDTO{
		ID: string(record.ID), Name: record.Name, Publisher: record.Publisher,
		Version: record.Version, ProtocolVersion: record.ProtocolVersion,
		Enabled: record.Enabled, RuntimeStatus: record.RuntimeStatus,
		Status: status, Running: record.RuntimeStatus == "running",
		SignatureStatus: record.SignatureStatus, Checksum: record.Checksum,
		RepositoryURL: pluginRepositoryURL(manifest), InstallPath: record.InstallPath,
		ReleaseTag: pluginReleaseTag(manifest), SourceCommit: pluginSourceCommit(manifest), LastError: record.LastError,
		Permissions: permissions, Tools: tools, EventSources: events,
		InstalledAt: record.InstalledAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (b *Bridge) applyPluginGrantStatus(ctx context.Context, id domain.ID, dto *PluginDTO) {
	if dto == nil {
		return
	}
	grants, err := b.repositories.Plugins.Grants(ctx, id)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for index := range dto.Permissions {
		dto.Permissions[index].Granted = false
		for _, grant := range grants {
			if grant.Capability == domain.NormalizeCapabilityName(dto.Permissions[index].Capability) &&
				(grant.ExpiresAt.IsZero() || now.Before(grant.ExpiresAt)) {
				dto.Permissions[index].Granted = true
				break
			}
		}
	}
}

func decodeManifest(content []byte) (plugins.Manifest, error) {
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var manifest plugins.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return plugins.Manifest{}, fmt.Errorf("decode plugin manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return plugins.Manifest{}, errors.New("plugin manifest contains trailing JSON")
	}
	return manifest, nil
}

func loadManifestFromDirectory(directory string) (plugins.Manifest, []byte, error) {
	content, err := readBoundedFile(filepath.Join(directory, plugins.ManifestFileName), maxPluginManifestBytes)
	if err != nil {
		return plugins.Manifest{}, nil, err
	}
	manifest, err := decodeManifest(content)
	return manifest, content, err
}

func canonicalPluginPackage(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("plugin package path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve plugin package: %w", err)
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve plugin package symlinks: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", errors.New("plugin package path must be a directory")
	}
	return real, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return content, nil
}

func packageTrust(manifest plugins.Manifest) (status string, devMode bool, notice string) {
	// A manifest cannot attest to its own signature. A future trust-store
	// verifier can upgrade this state to verified; until then local packages
	// are accepted only behind the explicit developer-mode boundary.
	if manifest.Signature != nil && strings.TrimSpace(manifest.Signature.Value) != "" {
		return "unverified", true, "Подпись указана, но издатель ещё не добавлен в локальное trust store."
	}
	return "unsigned", true, "Неподписанный локальный пакет разрешён только в plugin dev mode."
}

func pluginChecksum(manifest plugins.Manifest) string {
	if manifest.Checksum == nil {
		return ""
	}
	return manifest.Checksum.Value
}

func pluginRepositoryURL(manifest plugins.Manifest) string {
	if manifest.Repository == nil {
		return ""
	}
	return manifest.Repository.URL
}

func pluginReleaseTag(manifest plugins.Manifest) string {
	if manifest.Repository == nil {
		return ""
	}
	return manifest.Repository.ReleaseTag
}

func pluginSourceCommit(manifest plugins.Manifest) string {
	if manifest.Repository == nil {
		return ""
	}
	return manifest.Repository.Commit
}

func installPluginDirectory(source, destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create plugin parent directory: %w", err)
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("plugin version is already installed")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect plugin destination: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".installing-*")
	if err != nil {
		return fmt.Errorf("create plugin staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := copyPluginTree(source, staging); err != nil {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		return fmt.Errorf("commit plugin installation: %w", err)
	}
	return nil
}

func removeOwnedPluginDirectory(root, pluginID, version string) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(pluginID) == "" || strings.TrimSpace(version) == "" {
		return errors.New("plugin removal path is incomplete")
	}
	if filepath.Base(pluginID) != pluginID || filepath.Base(version) != version || pluginID == "." || version == "." {
		return errors.New("plugin removal identifiers are not path-safe")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve plugin root: %w", err)
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil {
		return fmt.Errorf("inspect plugin root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("plugin root must be a real directory")
	}
	pluginRoot := filepath.Join(rootAbs, pluginID)
	pluginInfo, err := os.Lstat(pluginRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect plugin directory: %w", err)
	}
	if !pluginInfo.IsDir() || pluginInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to remove through a symlinked plugin directory")
	}
	target := filepath.Join(pluginRoot, version)
	targetInfo, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect plugin version directory: %w", err)
	}
	if !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to remove a non-directory plugin installation")
	}
	quarantineID, err := domain.NewID("removing")
	if err != nil {
		return err
	}
	quarantine := filepath.Join(rootAbs, "."+string(quarantineID))
	if err := os.Rename(target, quarantine); err != nil {
		return fmt.Errorf("quarantine plugin directory: %w", err)
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return fmt.Errorf("remove quarantined plugin directory: %w", err)
	}
	return nil
}

func copyPluginTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin package contains forbidden symlink %q", relative)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin package contains unsupported file %q", relative)
		}
		return copyPluginFile(path, target, info.Mode())
	})
}

func copyPluginFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	permissions := fs.FileMode(0o600)
	if mode.Perm()&0o111 != 0 {
		permissions = 0o700
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permissions)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func (b *Bridge) appendPluginAudit(ctx context.Context, action string, pluginID domain.ID, decision domain.PermissionDecision, status string) error {
	id, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"status": status})
	return b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: id, Actor: domain.ActorUser, Action: action, Target: string(pluginID),
		Decision: decision, PayloadRedacted: string(payload), CreatedAt: time.Now().UTC(),
	})
}
