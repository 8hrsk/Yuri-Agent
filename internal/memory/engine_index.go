package memory

import (
	"context"
	"fmt"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// IndexMemory updates only the rebuildable semantic projection. Durable
// memory state is unaffected if no embedder/vector index is configured.
func (e *Engine) IndexMemory(ctx context.Context, memory domain.Memory) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return e.indexMemory(ctx, memory)
}

// RebuildVectorIndex recreates the derived semantic projection from current
// authoritative records. Deleted records are omitted; active, dormant and
// hidden memories remain searchable when policy permits them.
func (e *Engine) RebuildVectorIndex(ctx context.Context) (int, error) {
	if err := contextErr(ctx); err != nil {
		return 0, err
	}
	if e == nil || e.store == nil {
		return 0, ErrNoStore
	}
	if e.embedder == nil || e.vectors == nil {
		return 0, fmt.Errorf("%w: vector index and embedder are required", ErrNoEmbedder)
	}
	items, err := e.store.ListMemories(ctx, MemoryFilter{AgentID: e.agentID, IncludeDormant: true, IncludeHidden: true, Limit: 0})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, item := range items {
		if (!e.agentID.Empty() && item.AgentID != e.agentID) || item.Lifecycle == domain.MemoryLifecycleDeleted {
			continue
		}
		if err := e.indexMemory(ctx, item); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (e *Engine) indexMemory(ctx context.Context, memory domain.Memory) error {
	if e == nil || e.embedder == nil || e.vectors == nil {
		return nil
	}
	vectors, err := e.embedder.Embed(ctx, []string{memory.Content})
	if err != nil {
		return err
	}
	if len(vectors) == 0 {
		return fmt.Errorf("%w: embedder returned no vector", ErrNoEmbedder)
	}
	return e.vectors.Upsert(ctx, VectorDocument{ID: memory.ID, Vector: vectors[0], Version: e.embedder.Version()})
}
