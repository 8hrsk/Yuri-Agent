package desktop

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
)

// packageTrust maps a trust-store decision onto the installation gate. Only a
// signature verified against a publisher key the owner added explicitly clears
// the gate; everything else stays behind the global developer-mode switch.
func packageTrust(decision plugins.TrustDecision) (status string, devMode bool, notice string) {
	switch decision.Status {
	case plugins.TrustVerified:
		return plugins.TrustVerified, false, ""
	case plugins.TrustUnverified:
		return plugins.TrustUnverified, true, "Подпись не подтверждена локальным trust store: " + decision.Reason
	default:
		return plugins.TrustUnsigned, true, "Неподписанный локальный пакет разрешён только в plugin dev mode."
	}
}

// pluginTrustStore resolves the owner-controlled publisher key store. It lives
// beside the installed packages in a dot-directory, which no plugin id can
// collide with because ids must start with [a-z0-9].
func (b *Bridge) pluginTrustStore() *plugins.TrustStore {
	return plugins.OpenTrustStore(filepath.Join(b.paths.PluginDirectory, ".trust", plugins.TrustStoreFileName))
}

// ListPluginPublishers returns the publisher keys the owner has trusted.
func (b *Bridge) ListPluginPublishers() ([]PluginPublisherKeyDTO, error) {
	keys, err := b.pluginTrustStore().Keys()
	if err != nil {
		return nil, err
	}
	result := make([]PluginPublisherKeyDTO, 0, len(keys))
	for _, key := range keys {
		result = append(result, PluginPublisherKeyDTO{
			KeyID: key.KeyID, Algorithm: key.Algorithm, PublicKey: key.PublicKey,
			Publisher: key.Publisher, Comment: key.Comment,
			AddedAt: key.AddedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return result, nil
}

// TrustPluginPublisher adds one ed25519 publisher key. This is the only way a
// package can ever become "verified", and it is always a deliberate owner
// action recorded in the audit log — never a side effect of an install.
func (b *Bridge) TrustPluginPublisher(request PluginPublisherKeyRequest) (PluginPublisherKeyDTO, error) {
	stored, err := b.pluginTrustStore().Add(plugins.PublisherKey{
		KeyID: request.KeyID, Algorithm: "ed25519", PublicKey: request.PublicKey,
		Publisher: request.Publisher, Comment: request.Comment,
	})
	if err != nil {
		return PluginPublisherKeyDTO{}, err
	}
	ctx, cancel := b.context()
	defer cancel()
	if err := b.appendPluginAudit(ctx, "plugin.publisher.trust", domain.ID(stored.KeyID), domain.PermissionAllow, stored.Publisher); err != nil {
		b.logger.WarnContext(ctx, "append publisher trust audit", "key_id", stored.KeyID, "error", err)
	}
	return PluginPublisherKeyDTO{
		KeyID: stored.KeyID, Algorithm: stored.Algorithm, PublicKey: stored.PublicKey,
		Publisher: stored.Publisher, Comment: stored.Comment,
		AddedAt: stored.AddedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// RevokePluginPublisher removes a publisher key. Packages already installed
// keep their recorded status but fail the start-time re-verification, so a
// revoked publisher stops running plugins at the next start.
func (b *Bridge) RevokePluginPublisher(request PluginPublisherKeyRequest) error {
	removed, err := b.pluginTrustStore().Remove(request.KeyID)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("publisher key %q is not trusted", strings.TrimSpace(request.KeyID))
	}
	ctx, cancel := b.context()
	defer cancel()
	if err := b.appendPluginAudit(ctx, "plugin.publisher.revoke", domain.ID(strings.TrimSpace(request.KeyID)), domain.PermissionDeny, "revoked"); err != nil {
		b.logger.WarnContext(ctx, "append publisher revoke audit", "key_id", request.KeyID, "error", err)
	}
	return nil
}
