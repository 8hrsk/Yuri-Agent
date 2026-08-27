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
}

func (adapter sqliteMemoryAdapter) GetMemory(ctx context.Context, id domain.ID) (domain.Memory, error) {
	return adapter.repositories.Memories.Get(ctx, id)
}

func (adapter sqliteMemoryAdapter) ListMemories(ctx context.Context, filter memory.MemoryFilter) ([]domain.Memory, error) {
	items, err := adapter.repositories.Memories.List(ctx, storage.MemoryListOptions{
		IncludeDormant: filter.IncludeDormant, IncludeDeleted: filter.IncludeDeleted, Limit: filter.Limit,
	})
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
	current, err := adapter.repositories.Memories.Get(ctx, change.Memory.ID)
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
	_, err := adapter.repositories.Memories.RecordRecall(ctx, id, at)
	return err
}

func (adapter sqliteMemoryAdapter) ListMemorySources(ctx context.Context, id domain.ID) ([]domain.MemorySource, error) {
	return adapter.repositories.Memories.ListSources(ctx, id)
}

func (adapter sqliteMemoryAdapter) ListMemoryVersions(ctx context.Context, id domain.ID, limit int) ([]memory.MemoryRevision, error) {
	versions, err := adapter.repositories.Memories.ListVersions(ctx, id, limit)
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
	hits, err := adapter.repositories.Memories.Search(ctx, query, storage.MemorySearchOptions{
		IncludeDormant: filter.IncludeDormant, IncludeDeleted: filter.IncludeDeleted,
		Limit: limit,
	})
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
	hits, err := adapter.repositories.Archive.Search(ctx, query, storage.ArchiveSearchOptions{
		IncludeArchived: options.IncludeArchived, Limit: options.Limit, MaxTokens: options.Budget.MaxTokens,
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
