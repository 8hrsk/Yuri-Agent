package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestPluginRepositoryPersistsLifecycleAndGrants(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	plugin := PluginRecord{
		ID: "dev.yuri.echo", Name: "Echo", Publisher: "OrdoAI", Version: "0.1.0",
		ProtocolVersion: "1.0", InstallPath: "/tmp/yuri/plugins/dev.yuri.echo/0.1.0",
		ManifestJSON: `{"id":"dev.yuri.echo"}`, SignatureStatus: "verified",
		Checksum: "sha256:test", RuntimeStatus: "stopped", InstalledAt: now, UpdatedAt: now,
	}
	source := PluginSource{PluginID: plugin.ID, RepositoryURL: "https://github.com/OrdoAI/yuri-plugin-echo", Checksum: plugin.Checksum, CheckedAt: now}
	if err := repositories.Plugins.Upsert(ctx, plugin, source); err != nil {
		t.Fatalf("upsert plugin: %v", err)
	}
	grant := domain.PermissionGrant{
		ID: "grant-1", SubjectID: plugin.ID, Capability: domain.CapabilityNetworkHTTP,
		Scope: domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"example.com"}}, GrantedAt: now,
	}
	if err := repositories.Plugins.ReplaceGrants(ctx, plugin.ID, []domain.PermissionGrant{grant}); err != nil {
		t.Fatalf("replace grants: %v", err)
	}
	if err := repositories.Plugins.SetEnabled(ctx, plugin.ID, true, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Plugins.SetRuntimeStatus(ctx, plugin.ID, "running", "", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Plugins.Get(ctx, plugin.ID)
	if err != nil || !stored.Enabled || stored.RuntimeStatus != "running" {
		t.Fatalf("stored plugin = %#v, %v", stored, err)
	}
	grants, err := repositories.Plugins.Grants(ctx, plugin.ID)
	if err != nil || len(grants) != 1 || grants[0].Capability != domain.CapabilityNetworkHTTP {
		t.Fatalf("stored grants = %#v, %v", grants, err)
	}
	if err := repositories.Plugins.Delete(ctx, plugin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Plugins.Get(ctx, plugin.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get deleted plugin error = %v", err)
	}
	grants, err = repositories.Plugins.Grants(ctx, plugin.ID)
	if err != nil || len(grants) != 0 {
		t.Fatalf("grants after cascade = %#v, %v", grants, err)
	}
}
