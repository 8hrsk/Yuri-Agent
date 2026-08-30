package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	"github.com/OrdoAI/yuri-agent/internal/security"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func trimmedValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result = append(result, value)
	}
	return result
}

func describeGrants(grants []domain.PermissionGrant) string {
	if len(grants) == 0 {
		return "enabled with no capability grants"
	}
	parts := make([]string, 0, len(grants))
	for _, grant := range grants {
		part := string(grant.Capability) + ":" + string(grant.Scope.Kind)
		if len(grant.Scope.Values) > 0 {
			part += "(" + strings.Join(grant.Scope.Values, ",") + ")"
		}
		if !grant.ExpiresAt.IsZero() {
			part += "@" + grant.ExpiresAt.Format(time.RFC3339)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " · ")
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
	dto := pluginDTO(record, manifest)
	b.applyPluginGrantStatus(ctx, record.ID, &dto)
	return dto, nil
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
	// Re-verify on every start: the recorded status describes the package at
	// install time, and a publisher key can be revoked or a payload swapped
	// after that.
	decision, err := b.verifyInstalledPlugin(record)
	if err != nil {
		_ = b.repositories.Plugins.SetRuntimeStatus(ctx, record.ID, "failed", safeError(err.Error()), time.Now().UTC())
		return PluginDTO{}, err
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
			DevMode: !decision.Verified(), SignatureVerified: decision.Verified(),
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
	dto := pluginDTO(record, manifest)
	b.applyPluginGrantStatus(ctx, record.ID, &dto)
	return dto, nil
}

// verifyInstalledPlugin re-checks the installed package immediately before it
// is launched. A package whose signature no longer verifies may run only while
// the owner keeps plugin dev mode on; nothing else can lift that gate.
func (b *Bridge) verifyInstalledPlugin(record storage.PluginRecord) (plugins.TrustDecision, error) {
	expected := filepath.Join(b.paths.PluginDirectory, string(record.ID), record.Version)
	if filepath.Clean(record.InstallPath) != filepath.Clean(expected) {
		return plugins.TrustDecision{}, errors.New("refusing to start a plugin outside the application-owned plugin directory")
	}
	manifest, content, err := loadManifestFromDirectory(record.InstallPath)
	if err != nil {
		return plugins.TrustDecision{}, fmt.Errorf("read installed plugin manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return plugins.TrustDecision{}, err
	}
	if err := manifest.VerifyChecksum(record.InstallPath); err != nil {
		return plugins.TrustDecision{}, fmt.Errorf("verify installed plugin payload: %w", err)
	}
	decision, err := b.pluginTrustStore().VerifyPackage(content, manifest)
	if err != nil {
		return plugins.TrustDecision{}, err
	}
	if !decision.Verified() && !b.pluginDevMode() {
		return plugins.TrustDecision{}, fmt.Errorf("plugin signature is not verified (%s); enable plugin dev mode to run it anyway", decision.Reason)
	}
	return decision, nil
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
	dto := pluginDTO(record, manifest)
	b.applyPluginGrantStatus(ctx, record.ID, &dto)
	return dto, nil
}

func (b *Bridge) restoreEnabledPlugins() {
	b.background.Add(1)
	go func() {
		defer b.background.Done()
		// Registered after background.Done so it unwinds first: a panic in the
		// startup restore must be turned into owner-visible plugin state before
		// the shutdown wait group is released.
		defer b.recoverBridgeGoroutine("restore_enabled_plugins", func(recovered error) {
			b.failUnrestoredPlugins(recovered)
		})
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
		// This loop consumes frames a third-party process chose to send, so it
		// is the one bridge goroutine reachable from untrusted input. Without
		// this guard a single fault while handling an event would take the
		// whole desktop process, UI included, down with it.
		defer b.recoverBridgeGoroutine("plugin_events", func(recovered error) {
			b.failPluginSupervision(id, supervisor, recovered)
		})
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
		if dropped := supervisor.DroppedEvents(); dropped > 0 {
			// The host drops events rather than tearing down the session when
			// the consumer falls behind. Without this line the loss would be
			// entirely invisible.
			b.logger.Warn("plugin events dropped by a slow consumer", "plugin_id", id, "dropped", dropped)
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

// failPluginSupervision moves a plugin whose event watcher died into a terminal
// state instead of leaving it marked running with nobody watching it.
//
// Nothing the goroutine captured at start may be written back unchecked. By the
// time a panic unwinds, StopPlugin may have taken the plugin down, or a restart
// may have installed a newer supervisor whose own watcher is healthy; the live
// supervisor map is therefore re-read and the failure is recorded only while
// this goroutine's supervisor is still the plugin's current one. The durable row
// is re-read too, so a plugin uninstalled in the meantime is not resurrected as
// a failed one.
//
// The process is stopped either way. A plugin left running after its watcher
// died keeps a third-party process alive with the owner's granted capabilities
// and no consumer for its events, which is exactly the unsupervised state this
// recovery exists to prevent.
func (b *Bridge) failPluginSupervision(id domain.ID, supervisor *plugins.Supervisor, cause error) {
	b.mu.Lock()
	tracked, exists := b.pluginSupervisors[string(id)]
	current := exists && tracked == supervisor
	if current {
		delete(b.pluginSupervisors, string(id))
	}
	b.mu.Unlock()

	if supervisor != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := supervisor.Stop(stopCtx)
		stopCancel()
		if err != nil && !errors.Is(err, plugins.ErrPluginExited) {
			b.logger.Error("stop plugin after its event watcher panicked", "plugin_id", id, "error", safeError(err.Error()))
		}
	}
	if !current {
		// A newer supervisor, or a deliberate stop, already owns this plugin's
		// status. Overwriting it would report a healthy session as failed.
		b.logger.Warn("plugin event watcher panicked after the plugin was restarted or stopped", "plugin_id", id)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.repositories.Plugins.Get(ctx, id); err != nil {
		b.logger.Warn("plugin row is unavailable after its event watcher panicked", "plugin_id", id, "error", err)
		return
	}
	if err := b.repositories.Plugins.SetRuntimeStatus(ctx, id, string(plugins.StateFailed), safeError(cause.Error()), time.Now().UTC()); err != nil {
		b.logger.Error("record plugin supervision failure", "plugin_id", id, "error", err)
	}
}

// failUnrestoredPlugins closes out a startup restore that panicked part way
// through. The pass aborts wherever the panic happened, so every enabled plugin
// it had not reached yet is left exactly as the previous session left it —
// including rows still reading "running" after an unclean shutdown, which the
// UI presents as healthy while no process exists.
//
// The goroutine's own listing is stale by then, and if the panic happened while
// producing that listing there is none at all, so the rows are re-read here.
// Only a row that still claims a live session while nothing supervises it is
// corrected: a plugin that really did restore keeps its running status, and a
// row that already reads stopped is honest and left alone.
func (b *Bridge) failUnrestoredPlugins(cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	records, err := b.repositories.Plugins.List(ctx)
	if err != nil {
		b.logger.ErrorContext(ctx, "reload plugins after the restore pass panicked", "error", err)
		return
	}
	message, now := safeError(cause.Error()), time.Now().UTC()
	for _, record := range records {
		if !record.Enabled || !pluginStatusClaimsALiveSession(record.RuntimeStatus) {
			continue
		}
		b.mu.RLock()
		supervisor := b.pluginSupervisors[string(record.ID)]
		b.mu.RUnlock()
		if supervisor != nil {
			if state, _ := supervisor.State(); state == plugins.StateRunning {
				continue
			}
		}
		if err := b.repositories.Plugins.SetRuntimeStatus(ctx, record.ID, string(plugins.StateFailed), message, now); err != nil {
			b.logger.ErrorContext(ctx, "record unrestored plugin", "plugin_id", record.ID, "error", err)
		}
	}
}

// pluginStatusClaimsALiveSession reports whether a stored runtime status tells
// the owner that a plugin process is up or coming up.
func pluginStatusClaimsALiveSession(status string) bool {
	switch strings.TrimSpace(status) {
	case string(plugins.StateRunning), string(plugins.StateStarting):
		return true
	default:
		return false
	}
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
	requested := domain.CapabilityScope{Kind: requestedKind, Values: append([]string(nil), requestedScope.Values...)}
	for _, grant := range grants {
		if !grant.ExpiresAt.IsZero() && !time.Now().UTC().Before(grant.ExpiresAt) {
			continue
		}
		if grant.Capability != domain.NormalizeCapabilityName(request.Capability) {
			continue
		}
		// The grant must cover the request, not merely equal it. Set equality
		// made a narrowed grant indistinguishable from a missing one and made
		// an unrestricted grant fail against a narrower request.
		if scopeCovers(grant.Scope, requested) {
			return plugins.AuthorizationResult{Allowed: true, Reason: "active owner grant covers the requested scope"}, nil
		}
	}
	return plugins.AuthorizationResult{Allowed: false, Reason: "no active owner grant covers the requested capability scope"}, nil
}

// scopeCovers and scopeHasWildcardValue are the desktop
// layer's names for the one scope rule that lives in internal/security. N-9:
// this file used to carry a second, subtly different implementation of the
// same rule — a bare hostname was an exact match here and a subdomain licence
// in the policy evaluator. There is now exactly one definition, and both
// layers call it.
func scopeCovers(outer, inner domain.CapabilityScope) bool {
	return security.ScopeCovers(outer, inner)
}

func scopeHasWildcardValue(scope domain.CapabilityScope) bool {
	return security.ScopeHasWildcardValue(scope)
}
