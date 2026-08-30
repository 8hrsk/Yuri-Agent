package desktop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// sqliteMemoryAdapter is the application boundary between the provider-neutral
// memory engine and authoritative SQLite repositories.
type sqliteMemoryAdapter struct {
	repositories *storage.Repositories
	agentID      domain.ID
}

func (adapter sqliteMemoryAdapter) GetMemory(ctx context.Context, id domain.ID) (domain.Memory, error) {
	if !adapter.agentID.Empty() {
		return adapter.repositories.Memories.GetForAgent(ctx, adapter.agentID, id)
	}
	return adapter.repositories.Memories.Get(ctx, id)
}

func (adapter sqliteMemoryAdapter) ListMemories(ctx context.Context, filter memory.MemoryFilter) ([]domain.Memory, error) {
	if !adapter.agentID.Empty() && !filter.AgentID.Empty() && filter.AgentID != adapter.agentID {
		return nil, domain.ErrConflict
	}
	options := storage.MemoryListOptions{
		AgentID: adapter.agentID, Scope: domain.MemoryScopeAgentPrivate,
		IncludeDormant: filter.IncludeDormant, IncludeDeleted: filter.IncludeDeleted, Limit: filter.Limit,
	}
	if filter.IncludeShared {
		options.AgentID = ""
		options.Scope = ""
		options.VisibleToAgentID = adapter.agentID
	}
	items, err := adapter.repositories.Memories.List(ctx, options)
	if err != nil {
		return nil, err
	}
	kinds := make(map[domain.MemoryKind]struct{}, len(filter.Kinds))
	for _, kind := range filter.Kinds {
		kinds[kind] = struct{}{}
	}
	states := make(map[domain.MemoryLifecycle]struct{}, len(filter.States))
	for _, state := range filter.States {
		states[state] = struct{}{}
	}
	result := make([]domain.Memory, 0, len(items))
	for _, item := range items {
		if len(kinds) > 0 {
			if _, ok := kinds[item.Kind]; !ok {
				continue
			}
		}
		if len(states) > 0 {
			if _, ok := states[item.Lifecycle]; !ok {
				continue
			}
		}
		if item.HiddenFromCore && !filter.IncludeHidden {
			continue
		}
		result = append(result, item)
	}
	return result, nil
}

func (adapter sqliteMemoryAdapter) ApplyMemoryChange(ctx context.Context, change memory.MemoryChange) error {
	if change.Memory.ID.Empty() {
		return fmt.Errorf("memory id is required")
	}
	if change.Memory.Scope == "" {
		change.Memory.Scope = domain.MemoryScopeAgentPrivate
	}
	if !adapter.agentID.Empty() {
		if change.Memory.AgentID.Empty() {
			change.Memory.AgentID = adapter.agentID
		} else if change.Memory.AgentID != adapter.agentID {
			return domain.ErrConflict
		}
	}
	current, err := adapter.GetMemory(ctx, change.Memory.ID)
	if errors.Is(err, domain.ErrNotFound) {
		if change.Memory.Version != 1 {
			return domain.ErrConflict
		}
		return adapter.repositories.Memories.Create(ctx, change.Memory, change.Sources)
	}
	if err != nil {
		return err
	}
	metadata := storage.MemoryVersionMetadata{ParentVersion: current.Version, Operation: "update", Reason: change.Memory.Reason}
	if change.Revision != nil {
		metadata.RevisionID = change.Revision.ID
		metadata.ParentVersion = change.Revision.ParentVersion
		metadata.Operation = string(change.Revision.Operation)
		metadata.Reason = change.Revision.Reason
	}
	_, err = adapter.repositories.Memories.AppendVersionWithMetadata(
		ctx, change.Memory, current.Version, metadata, change.Sources,
	)
	return err
}

func (adapter sqliteMemoryAdapter) TouchMemory(ctx context.Context, id domain.ID, at time.Time) error {
	if !adapter.agentID.Empty() {
		item, err := adapter.repositories.Memories.Get(ctx, id)
		if err != nil {
			return err
		}
		if item.AgentID != adapter.agentID && !item.Scope.Shared() {
			return domain.ErrNotFound
		}
		_, err = adapter.repositories.Memories.RecordRecall(ctx, id, at)
		return err
	}
	_, err := adapter.repositories.Memories.RecordRecall(ctx, id, at)
	return err
}

func (adapter sqliteMemoryAdapter) ListMemorySources(ctx context.Context, id domain.ID) ([]domain.MemorySource, error) {
	if !adapter.agentID.Empty() {
		item, err := adapter.repositories.Memories.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if item.AgentID != adapter.agentID && !item.Scope.Shared() {
			return nil, domain.ErrNotFound
		}
		return adapter.repositories.Memories.ListSources(ctx, id)
	}
	return adapter.repositories.Memories.ListSources(ctx, id)
}

func (adapter sqliteMemoryAdapter) ListMemoryVersions(ctx context.Context, id domain.ID, limit int) ([]memory.MemoryRevision, error) {
	var versions []storage.MemoryVersionRecord
	var err error
	if !adapter.agentID.Empty() {
		versions, err = adapter.repositories.Memories.ListVersionsForAgent(ctx, adapter.agentID, id, limit)
	} else {
		versions, err = adapter.repositories.Memories.ListVersions(ctx, id, limit)
	}
	if err != nil {
		return nil, err
	}
	result := make([]memory.MemoryRevision, 0, len(versions))
	for _, version := range versions {
		result = append(result, memory.MemoryRevision{
			ID: version.RevisionID, MemoryID: version.Memory.ID,
			Operation: memory.MemoryOperation(version.Operation), Snapshot: version.Memory,
			ParentVersion: version.ParentVersion, Reason: version.Reason, CreatedAt: version.Memory.UpdatedAt,
		})
	}
	return result, nil
}

func (adapter sqliteMemoryAdapter) SearchMemoryLexical(ctx context.Context, query string, filter memory.MemoryFilter, limit int) ([]memory.LexicalHit, error) {
	if !adapter.agentID.Empty() && !filter.AgentID.Empty() && filter.AgentID != adapter.agentID {
		return nil, domain.ErrConflict
	}
	searchOptions := storage.MemorySearchOptions{
		AgentID: adapter.agentID, Scope: domain.MemoryScopeAgentPrivate,
		IncludeDormant: filter.IncludeDormant, IncludeDeleted: filter.IncludeDeleted,
		Limit: limit,
	}
	if filter.IncludeShared {
		searchOptions.AgentID = ""
		searchOptions.Scope = ""
		searchOptions.VisibleToAgentID = adapter.agentID
	}
	hits, err := adapter.repositories.Memories.Search(ctx, query, searchOptions)
	if err != nil {
		return nil, err
	}
	result := make([]memory.LexicalHit, 0, len(hits))
	for _, hit := range hits {
		result = append(result, memory.LexicalHit{
			MemoryID: hit.Memory.ID, Score: boundedScore(hit.Score), Snippet: hit.Snippet,
		})
	}
	return result, nil
}

func (adapter sqliteMemoryAdapter) SearchArchive(ctx context.Context, query string, options memory.ArchiveSearchOptions) ([]memory.ArchiveHit, error) {
	if !adapter.agentID.Empty() && !options.AgentID.Empty() && options.AgentID != adapter.agentID {
		return nil, domain.ErrConflict
	}
	agentID := firstNonEmptyID(options.AgentID, adapter.agentID)
	hits, err := adapter.repositories.Archive.Search(ctx, query, storage.ArchiveSearchOptions{
		AgentID: agentID, IncludeArchived: options.IncludeArchived, Limit: options.Limit, MaxTokens: options.Budget.MaxTokens,
	})
	if err != nil {
		return nil, err
	}
	result := make([]memory.ArchiveHit, 0, len(hits))
	for _, hit := range hits {
		result = append(result, memory.ArchiveHit{
			MessageID: hit.Message.ID, ConversationID: hit.ConversationID, Role: hit.Message.Role,
			Content: hit.Message.Content, CreatedAt: hit.Message.CreatedAt,
			Score: boundedScore(hit.Score), Snippet: strings.TrimSpace(hit.Snippet),
		})
	}
	return result, nil
}

var _ memory.Store = sqliteMemoryAdapter{}
var _ memory.LexicalSearcher = sqliteMemoryAdapter{}
var _ memory.ArchiveSearcher = sqliteMemoryAdapter{}
