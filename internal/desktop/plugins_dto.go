package desktop

import (
	"context"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func pluginDTO(record storage.PluginRecord, manifest plugins.Manifest) PluginDTO {
	permissions := make([]PluginPermissionDTO, 0, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		scope, _ := pluginDomainScope(permission.Scope)
		permissions = append(permissions, PluginPermissionDTO{
			Capability: permission.Capability, Scope: string(scope.Kind),
			Values: append([]string(nil), scope.Values...), Description: permission.Reason,
			// Granted is filled in from the persisted grants by
			// applyPluginGrantStatus; being enabled no longer implies consent.
			Granted: false,
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
