package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// ApplyDecay moves low-salience records to dormant. The operation is
// reversible through deliberate Recall or an explicit RestoreMemory call.
func (e *Engine) ApplyDecay(ctx context.Context, now time.Time) ([]WriteResult, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if e == nil || e.store == nil {
		return nil, ErrNoStore
	}
	if now.IsZero() {
		now = e.now()
	}
	items, err := e.store.ListMemories(ctx, MemoryFilter{AgentID: e.agentID, Limit: 0})
	if err != nil {
		return nil, err
	}
	results := make([]WriteResult, 0)
	for _, item := range items {
		if (!e.agentID.Empty() && item.AgentID != e.agentID) || item.Lifecycle != domain.MemoryLifecycleActive || item.Pinned || item.HiddenFromCore || item.Retention == domain.MemoryRetentionPermanent {
			continue
		}
		policy := e.decayPolicy(item).normalize(item.Kind)
		if policy.NeverDormant {
			continue
		}
		score := EffectiveSalience(item, now, policy)
		anchor := activityTime(item)
		tooOld := policy.DormantAfter > 0 && now.Sub(anchor) >= policy.DormantAfter
		if score >= policy.DormantThreshold && !tooOld {
			continue
		}
		previous := item
		item.Lifecycle = domain.MemoryLifecycleDormant
		item.DormantAt = now.UTC()
		item.UpdatedAt = now.UTC()
		item.Version++
		result, commitErr := e.commit(ctx, item, &previous, nil, OperationDormant, "natural decay", now, false)
		if commitErr != nil {
			return results, commitErr
		}
		results = append(results, result)
	}
	return results, nil
}

// RestoreMemory explicitly reactivates a dormant record. Deliberate Recall
// calls this same path when configured to restore a hit.
func (e *Engine) RestoreMemory(ctx context.Context, id domain.ID, reason string) (domain.Memory, error) {
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	memory, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	return e.restoreWithReason(ctx, memory, e.now(), reason)
}

// ForgetMemory creates a reversible tombstone for a derived memory. It never
// deletes transcript messages or their provenance.
func (e *Engine) ForgetMemory(ctx context.Context, id domain.ID, reason string) (domain.Memory, error) {
	result, err := e.Remember(ctx, Candidate{Operation: CandidateForget, MatchID: id, Reason: reason})
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}

// EditMemory applies a user- or application-requested content edit as a new
// version while preserving the original transcript and provenance.
func (e *Engine) EditMemory(ctx context.Context, id domain.ID, content, reason string) (domain.Memory, error) {
	if strings.TrimSpace(content) == "" {
		return domain.Memory{}, fmt.Errorf("%w: memory content is required", domain.ErrInvalidArgument)
	}
	existing, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	existing.Content = strings.TrimSpace(content)
	result, err := e.Remember(ctx, Candidate{
		Operation: CandidateUpdate,
		MatchID:   id,
		Memory:    existing,
		Reason:    reason,
	})
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}

// HideMemory controls inclusion in the stable core snapshot while keeping a
// record available to deliberate search and the memory UI.
func (e *Engine) HideMemory(ctx context.Context, id domain.ID, hidden bool, reason string) (domain.Memory, error) {
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	memory, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	previous := memory
	memory.Version++
	memory.HiddenFromCore = hidden
	memory.UpdatedAt = e.now().UTC()
	result, err := e.commit(ctx, memory, &previous, nil, OperationHide, reason, memory.UpdatedAt, false)
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}

// PinMemory marks a memory as explicitly curated for the core snapshot.
func (e *Engine) PinMemory(ctx context.Context, id domain.ID, pinned bool, reason string) (domain.Memory, error) {
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	memory, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	previous := memory
	memory.Version++
	memory.Pinned = pinned
	memory.UpdatedAt = e.now().UTC()
	result, err := e.commit(ctx, memory, &previous, nil, OperationUpdate, reason, memory.UpdatedAt, false)
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}

func (e *Engine) restore(ctx context.Context, memory domain.Memory, now time.Time) (domain.Memory, error) {
	return e.restoreWithReason(ctx, memory, now, "deliberate recall")
}

func (e *Engine) restoreWithReason(ctx context.Context, memory domain.Memory, now time.Time, reason string) (domain.Memory, error) {
	if memory.Lifecycle != domain.MemoryLifecycleDormant {
		return memory, nil
	}
	previous := memory
	memory.Lifecycle = domain.MemoryLifecycleActive
	memory.DormantAt = time.Time{}
	memory.DeletedAt = time.Time{}
	memory.UpdatedAt = now.UTC()
	memory.Version++
	result, err := e.commit(ctx, memory, &previous, nil, OperationRestore, reason, now, false)
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}
