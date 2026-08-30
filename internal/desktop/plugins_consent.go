package desktop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
)

// EnablePlugin enables a plugin and persists exactly the capabilities the
// owner consented to. A manifest declaration is a request, never a grant:
// capabilities that are not in the consent list are not granted, and the
// runtime authorizer denies them even though the manifest declares them.
// Calling EnablePlugin again with a shorter list is how a single capability
// is revoked without disabling the plugin.
func (b *Bridge) EnablePlugin(request PluginEnableRequest) (PluginDTO, error) {
	ctx, cancel := b.context()
	defer cancel()
	record, manifest, err := b.installedPlugin(ctx, request.pluginID())
	if err != nil {
		return PluginDTO{}, err
	}
	grants, err := pluginConsentGrants(record.ID, manifest, request.Capabilities)
	if err != nil {
		return PluginDTO{}, err
	}
	previous, err := b.repositories.Plugins.Grants(ctx, record.ID)
	if err != nil {
		return PluginDTO{}, err
	}
	if err := b.repositories.Plugins.ReplaceGrants(ctx, record.ID, grants); err != nil {
		return PluginDTO{}, err
	}
	now := time.Now().UTC()
	if err := b.repositories.Plugins.SetEnabled(ctx, record.ID, true, now); err != nil {
		_ = b.repositories.Plugins.ReplaceGrants(context.Background(), record.ID, previous)
		return PluginDTO{}, err
	}
	record.Enabled, record.UpdatedAt = true, now
	if err := b.appendPluginAudit(ctx, "plugin.enable", record.ID, domain.PermissionAllow, describeGrants(grants)); err != nil {
		b.logger.WarnContext(ctx, "append plugin enable audit", "plugin_id", record.ID, "error", err)
	}
	dto := pluginDTO(record, manifest)
	b.applyPluginGrantStatus(ctx, record.ID, &dto)
	return dto, nil
}

// maxPluginGrantHours caps an owner-chosen grant lifetime at one year so an
// "expiring" grant cannot quietly become permanent.
const maxPluginGrantHours = 24 * 366

// pluginConsentGrants turns the owner's explicit consent list into grants.
// Every consent must match a manifest declaration, may only narrow it, and an
// unrestricted result requires its own confirmation flag.
func pluginConsentGrants(subject domain.ID, manifest plugins.Manifest, consents []PluginCapabilityConsent) ([]domain.PermissionGrant, error) {
	declared := make(map[domain.Capability]domain.CapabilityScope, len(manifest.Permissions))
	for _, declaration := range manifest.Permissions {
		capability := domain.NormalizeCapabilityName(declaration.Capability)
		scope, err := pluginDomainScope(declaration.Scope)
		if err != nil {
			return nil, fmt.Errorf("permission %q: %w", declaration.Capability, err)
		}
		if scope.Kind == "" {
			scope = domain.UnrestrictedScope()
		}
		declared[capability] = scope
	}
	now := time.Now().UTC()
	grants := make([]domain.PermissionGrant, 0, len(consents))
	seen := make(map[domain.Capability]struct{}, len(consents))
	for _, consent := range consents {
		capability := domain.NormalizeCapabilityName(consent.Capability)
		declaredScope, isDeclared := declared[capability]
		if !isDeclared {
			return nil, fmt.Errorf("plugin does not declare capability %q", capability)
		}
		if _, duplicate := seen[capability]; duplicate {
			return nil, fmt.Errorf("duplicate consent for capability %q", capability)
		}
		seen[capability] = struct{}{}
		granted := domain.CapabilityScope{Kind: declaredScope.Kind, Values: append([]string(nil), declaredScope.Values...)}
		if strings.TrimSpace(consent.ScopeKind) != "" || len(consent.ScopeValues) > 0 {
			granted = domain.CapabilityScope{
				Kind:   domain.ScopeKind(strings.ToLower(strings.TrimSpace(consent.ScopeKind))),
				Values: trimmedValues(consent.ScopeValues),
			}
			if !granted.Valid() {
				return nil, fmt.Errorf("consented scope for %q is invalid", capability)
			}
			if !scopeCovers(declaredScope, granted) {
				return nil, fmt.Errorf("consented scope for %q is broader than the manifest declaration", capability)
			}
		}
		if (granted.Kind == domain.ScopeUnrestricted || scopeHasWildcardValue(granted)) && !consent.AllowUnrestricted {
			return nil, fmt.Errorf("capability %q would be granted without any effective scope restriction and needs an explicit unrestricted confirmation", capability)
		}
		id, err := domain.NewID("grant")
		if err != nil {
			return nil, err
		}
		grant := domain.PermissionGrant{
			ID: id, SubjectID: subject, Capability: capability, Scope: granted, GrantedAt: now,
		}
		if consent.ExpiresInHours > 0 {
			if consent.ExpiresInHours > maxPluginGrantHours {
				return nil, fmt.Errorf("grant lifetime for %q exceeds the maximum of %d hours", capability, maxPluginGrantHours)
			}
			grant.ExpiresAt = now.Add(time.Duration(consent.ExpiresInHours) * time.Hour)
		}
		if !grant.Valid() {
			return nil, fmt.Errorf("invalid consented permission %q", consent.Capability)
		}
		grants = append(grants, grant)
	}
	return grants, nil
}
