package desktop

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

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
	inspection, _, _, err := b.inspectPluginPackage(request.Path)
	return inspection, err
}

func (b *Bridge) inspectPluginPackage(packagePath string) (PluginPackageInspection, plugins.Manifest, plugins.TrustDecision, error) {
	root, err := canonicalPluginPackage(packagePath)
	if err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, plugins.TrustDecision{}, err
	}
	content, err := readBoundedFile(filepath.Join(root, plugins.ManifestFileName), maxPluginManifestBytes)
	if err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, plugins.TrustDecision{}, fmt.Errorf("read plugin manifest: %w", err)
	}
	manifest, err := decodeManifest(content)
	if err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, plugins.TrustDecision{}, err
	}
	if err := manifest.Validate(); err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, plugins.TrustDecision{}, err
	}
	executable, err := manifest.ResolveExecutable(root)
	if err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, plugins.TrustDecision{}, err
	}
	if err := manifest.VerifyChecksum(root); err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, plugins.TrustDecision{}, err
	}
	compatible := manifest.CompatibleWithCore(pluginCoreVersion)
	decision, err := b.pluginTrustStore().VerifyPackage(content, manifest)
	if err != nil {
		return PluginPackageInspection{}, plugins.Manifest{}, plugins.TrustDecision{}, err
	}
	signatureStatus, requiresDevMode, notice := packageTrust(decision)
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
		Compatible: compatible, Installable: compatible && (!requiresDevMode || b.pluginDevMode()),
		RequiresDevMode: requiresDevMode,
	}
	if notice != "" {
		inspection.Warnings = append(inspection.Warnings, notice)
	}
	if !compatible {
		inspection.Errors = append(inspection.Errors, "Версия плагина несовместима с текущей версией Yuri.")
	}
	return inspection, manifest, decision, nil
}

func (b *Bridge) InstallPlugin(request PluginPathRequest) (PluginDTO, error) {
	inspection, manifest, decision, err := b.inspectPluginPackage(request.Path)
	if err != nil {
		return PluginDTO{}, err
	}
	if !inspection.Installable {
		if inspection.RequiresDevMode && !b.pluginDevMode() {
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
	// Re-verify from the installed copy so the persisted trust state describes
	// the bytes that will actually be executed, not the staging directory.
	installedDecision, err := b.pluginTrustStore().VerifyPackage(installedContent, installedManifest)
	if err != nil {
		_ = removeOwnedPluginDirectory(b.paths.PluginDirectory, manifest.ID, manifest.Version)
		return PluginDTO{}, err
	}
	if installedDecision.Status != decision.Status || installedDecision.KeyID != decision.KeyID {
		_ = removeOwnedPluginDirectory(b.paths.PluginDirectory, manifest.ID, manifest.Version)
		return PluginDTO{}, errors.New("installed plugin trust state differs from the inspected package")
	}
	now := time.Now().UTC()
	installedSignatureStatus := plugins.TrustVerified
	if !installedDecision.Verified() {
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
	if err := b.appendPluginAudit(ctx, "plugin.install", record.ID, domain.PermissionAllow, installedSignatureStatus+" · "+installedDecision.Reason); err != nil {
		b.logger.WarnContext(ctx, "append plugin install audit", "plugin_id", record.ID, "error", err)
	}
	return pluginDTO(record, manifest), nil
}
