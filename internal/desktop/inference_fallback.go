package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// inferenceRoutePlan is the safe hand-off between profile routing and the
// orchestration loop. Primary is always resolved first. Fallback is present
// only when the owner explicitly enabled a complete route and that route is
// currently configured. The plan itself never chooses fallback implicitly.
type inferenceRoutePlan struct {
	Primary         domain.RunInferenceRoute
	Fallback        domain.RunInferenceRoute
	FallbackEnabled bool
}

// resolveInferenceRoutePlan resolves both routes without starting a provider
// request. It is the integration seam for the runtime: before creating a
// durable run, it should use Primary; if and only if a retryable failure occurs
// before visible output or any tool side effect, it may choose Fallback. The
// caller must then create the durable run with the chosen route, so immutable
// run attribution remains correct.
func (b *Bridge) resolveInferenceRoutePlan(ctx context.Context, agentID domain.ID) (inferenceRoutePlan, error) {
	if b == nil || b.repositories == nil || b.repositories.Agents == nil {
		return inferenceRoutePlan{}, fmt.Errorf("agent inference route plan is unavailable")
	}
	profile, err := b.repositories.Agents.Get(ctx, agentID)
	if err != nil {
		return inferenceRoutePlan{}, err
	}
	primary, err := b.resolveInferenceRoute(profile.ProviderID, profile.Model)
	if err != nil {
		return inferenceRoutePlan{}, err
	}
	plan := inferenceRoutePlan{Primary: primary}
	fallback, enabled, err := profile.FallbackRoute()
	if err != nil {
		return inferenceRoutePlan{}, err
	}
	if !enabled {
		return plan, nil
	}
	resolvedFallback, err := b.resolveInferenceRoute(fallback.ProviderID, fallback.Model)
	if err != nil {
		return inferenceRoutePlan{}, fmt.Errorf("fallback model route for agent %s: %w", profile.Name, err)
	}
	// A global-primary change can make an older fallback configuration point
	// back to the same route. Treat it as inert instead of retrying the exact
	// failed provider/model or blocking an otherwise healthy primary request.
	if resolvedFallback == primary {
		return plan, nil
	}
	plan.Fallback = resolvedFallback
	plan.FallbackEnabled = true
	return plan, nil
}

// appendInferenceFallbackAudit records a route switch without provider
// payloads, credentials, or model output. It is intentionally a separate
// helper so every orchestration path (foreground, background, peer, and
// reflection) can share the same audit contract.
func (b *Bridge) appendInferenceFallbackAudit(ctx context.Context, runID, agentID domain.ID, from, to domain.RunInferenceRoute, reason string) error {
	if b == nil || b.repositories == nil || b.repositories.Audit == nil {
		return fmt.Errorf("inference fallback audit is unavailable")
	}
	if runID.Empty() || agentID.Empty() || !from.Valid() || !to.Valid() || to.ProviderID == "" || to.Model == "" {
		return fmt.Errorf("%w: invalid inference fallback audit route", domain.ErrInvalidArgument)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "provider failure before visible output"
	}
	payload, err := json.Marshal(map[string]any{
		"agent_id":              string(agentID),
		"from_provider_id":      strings.TrimSpace(from.ProviderID),
		"from_model":            strings.TrimSpace(from.Model),
		"to_provider_id":        strings.TrimSpace(to.ProviderID),
		"to_model":              strings.TrimSpace(to.Model),
		"reason":                reason,
		"before_visible_output": true,
	})
	if err != nil {
		return err
	}
	auditID, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	return b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: auditID, RunID: runID, Actor: domain.ActorSystem,
		Action: "inference.fallback", Target: string(runID), Decision: domain.PermissionAllow,
		PayloadRedacted: string(payload), CreatedAt: time.Now().UTC(),
	})
}
