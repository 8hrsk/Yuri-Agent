package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func (r *MemoryRepository) transitionLifecycle(ctx context.Context, id domain.ID, expectedVersion uint64, state domain.MemoryLifecycle, at time.Time, reason string) (domain.Memory, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if !state.Valid() {
		return domain.Memory{}, fmt.Errorf("%w: invalid memory lifecycle %q", domain.ErrInvalidArgument, state)
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != current.Version {
		return domain.Memory{}, domain.ErrConflict
	}
	current.Version++
	current.Lifecycle = state
	current.UpdatedAt = at.UTC()
	current.Reason = reason
	current.DormantAt = time.Time{}
	current.DeletedAt = time.Time{}
	switch state {
	case domain.MemoryLifecycleDormant:
		current.DormantAt = at.UTC()
	case domain.MemoryLifecycleDeleted:
		current.DeletedAt = at.UTC()
	}
	operation := "restore"
	if state == domain.MemoryLifecycleDormant {
		operation = "dormant"
	} else if state == domain.MemoryLifecycleDeleted {
		operation = "forget"
	}
	if err := r.saveWithOperation(ctx, current, operation); err != nil {
		return domain.Memory{}, err
	}
	return current, nil
}

func (r *MemoryRepository) transitionLifecycleForAgent(ctx context.Context, agentID, id domain.ID, expectedVersion uint64, state domain.MemoryLifecycle, at time.Time, reason string) (domain.Memory, error) {
	if agentID.Empty() {
		return domain.Memory{}, fmt.Errorf("%w: memory agent id is required", domain.ErrInvalidArgument)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if !state.Valid() {
		return domain.Memory{}, fmt.Errorf("%w: invalid memory lifecycle %q", domain.ErrInvalidArgument, state)
	}
	current, err := r.GetForAgent(ctx, agentID, id)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != current.Version {
		return domain.Memory{}, domain.ErrConflict
	}
	current.Version++
	current.Lifecycle = state
	current.UpdatedAt = at.UTC()
	current.Reason = reason
	current.DormantAt = time.Time{}
	current.DeletedAt = time.Time{}
	switch state {
	case domain.MemoryLifecycleDormant:
		current.DormantAt = at.UTC()
	case domain.MemoryLifecycleDeleted:
		current.DeletedAt = at.UTC()
	}
	operation := "restore"
	if state == domain.MemoryLifecycleDormant {
		operation = "dormant"
	} else if state == domain.MemoryLifecycleDeleted {
		operation = "forget"
	}
	if err := r.saveWithOperation(ctx, current, operation); err != nil {
		return domain.Memory{}, err
	}
	return current, nil
}

func (r *MemoryRepository) appendVersionTx(ctx context.Context, tx *sql.Tx, memory domain.Memory, sources []domain.MemorySource, previousVersion uint64, operation string, metadata *MemoryVersionMetadata, copySources func([]domain.MemorySource) []domain.MemorySource) error {
	if previousVersion > 0 {
		if copySources == nil {
			previous, err := listSources(ctx, tx, memory.ID, previousVersion)
			if err != nil {
				return err
			}
			// Source IDs identify a link to a particular revision. Reusing an
			// old ID would violate the append-only source journal, so copied
			// links receive deterministic IDs during normalization below.
			for index := range previous {
				previous[index].ID = ""
			}
			copySources = func(_ []domain.MemorySource) []domain.MemorySource { return previous }
		}
		sources = append(copySources(nil), sources...)
	}
	if strings.TrimSpace(operation) == "" {
		operation = "update"
	}
	if err := insertMemoryVersion(ctx, tx, memory, operation, previousVersion, metadata); err != nil {
		return err
	}
	for index, source := range sources {
		normalized, err := normalizeSource(memory, source, memory.Version, index)
		if err != nil {
			return err
		}
		if err := insertMemorySource(ctx, tx, normalized); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_heads(memory_id, version, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(memory_id) DO UPDATE SET version = excluded.version, updated_at = excluded.updated_at`,
		string(memory.ID), memory.Version, formatTime(memory.UpdatedAt)); err != nil {
		return wrappedSQLError("update memory head", err)
	}
	// memory_fts holds one row per live memory, not one per revision. The
	// superseded row carries nothing that memory_versions does not already
	// hold, and leaving it behind made every MATCH scan the postings of every
	// dead copy and compute bm25 over an inflated index.
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE memory_id = ?`, string(memory.ID)); err != nil {
		return wrappedSQLError("prune memory index", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_fts(memory_id, memory_version, kind, nature, content, summary)
		VALUES (?, ?, ?, ?, ?, ?)`, string(memory.ID), memory.Version, string(memory.Kind), string(memory.Nature), memory.Content, memory.Summary); err != nil {
		return wrappedSQLError("index memory", err)
	}
	return nil
}

func insertMemoryVersion(ctx context.Context, tx *sql.Tx, memory domain.Memory, operation string, parentVersion uint64, metadata *MemoryVersionMetadata) error {
	revisionID := fmt.Sprintf("%s:v%d", memory.ID, memory.Version)
	reason := memory.Reason
	if metadata != nil {
		if !metadata.RevisionID.Empty() {
			revisionID = string(metadata.RevisionID)
		}
		if strings.TrimSpace(metadata.Operation) != "" {
			operation = metadata.Operation
		}
		if strings.TrimSpace(metadata.Reason) != "" {
			reason = metadata.Reason
		}
		if metadata.ParentVersion > 0 || parentVersion == 0 {
			parentVersion = metadata.ParentVersion
		}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO memory_versions(
			memory_id, agent_id, scope, version, revision_id, operation, parent_version, kind, nature, content_text, content_json, summary,
			confidence, salience, valence, sensitivity, retention_policy, lifecycle_state,
			pinned, hidden_from_core, canonical_key, embedding_version, access_count,
			last_accessed_at, last_recalled_at, created_at, updated_at, dormant_at,
			deleted_at, reason, source_run_id, source_conversation_id, source_message_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(memory.ID), string(memory.AgentID), string(memory.Scope), memory.Version, revisionID, operation, parentVersion, string(memory.Kind), string(memory.Nature), memory.Content,
		memory.ContentJSON, memory.Summary, memory.Confidence, memory.Salience, memory.Valence,
		string(memory.Sensitivity), string(memory.Retention), string(memory.Lifecycle), boolInt(memory.Pinned),
		boolInt(memory.HiddenFromCore), memory.CanonicalKey, memory.EmbeddingVersion, memory.AccessCount,
		nullableTimeValue(memory.LastAccessedAt), nullableTimeValue(memory.LastRecalledAt),
		formatTime(memory.CreatedAt), formatTime(memory.UpdatedAt),
		nullableTimeValue(memory.DormantAt), nullableTimeValue(memory.DeletedAt), reason,
		nullableID(memory.SourceRunID), nullableID(memory.SourceConversationID), nullableID(memory.SourceMessageID))
	return wrappedSQLError("insert memory version", err)
}

func insertMemorySource(ctx context.Context, tx *sql.Tx, source domain.MemorySource) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO memory_sources(
			id, memory_id, memory_version, source_type, source_id, run_id,
			conversation_id, message_id, excerpt_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(source.ID), string(source.MemoryID), source.MemoryVersion, source.SourceType,
		nullableID(source.SourceID), nullableID(source.RunID), nullableID(source.ConversationID),
		nullableID(source.MessageID), source.ExcerptHash, formatTime(source.CreatedAt))
	return wrappedSQLError("insert memory source", err)
}

func normalizeMemoryForCreate(memory domain.Memory) domain.Memory {
	if memory.Version == 0 {
		memory.Version = 1
	}
	if memory.Kind == "" {
		memory.Kind = domain.MemoryKindSemantic
	}
	if memory.AgentID.Empty() {
		// Legacy callers had no agent boundary. Keep them functional while all
		// desktop/runtime writes provide the active named agent explicitly.
		memory.AgentID = domain.ID("owner")
	}
	if memory.Scope == "" {
		memory.Scope = domain.MemoryScopeAgentPrivate
	}
	if memory.Nature == "" {
		memory.Nature = domain.MemoryNatureFact
	}
	if memory.Sensitivity == "" {
		memory.Sensitivity = domain.MemorySensitivityPrivate
	}
	if memory.Retention == "" {
		memory.Retention = domain.MemoryRetentionDecay
	}
	if memory.Lifecycle == "" {
		memory.Lifecycle = domain.MemoryLifecycleActive
	}
	if memory.CreatedAt.IsZero() && !memory.UpdatedAt.IsZero() {
		memory.CreatedAt = memory.UpdatedAt
	}
	if memory.UpdatedAt.IsZero() && !memory.CreatedAt.IsZero() {
		memory.UpdatedAt = memory.CreatedAt
	}
	if memory.Lifecycle == domain.MemoryLifecycleDormant && memory.DormantAt.IsZero() {
		memory.DormantAt = memory.UpdatedAt
	}
	if memory.Lifecycle == domain.MemoryLifecycleDeleted && memory.DeletedAt.IsZero() {
		memory.DeletedAt = memory.UpdatedAt
	}
	memory.Content = strings.TrimSpace(memory.Content)
	if memory.Content == "" && strings.TrimSpace(memory.ContentJSON) != "" {
		// Keep structured-only records searchable without discarding their
		// canonical JSON representation.
		memory.Content = strings.TrimSpace(memory.ContentJSON)
	}
	memory.Summary = strings.TrimSpace(memory.Summary)
	return memory
}

func validateMemoryForStorage(memory domain.Memory) error {
	if err := memory.Validate(); err != nil {
		return err
	}
	if memory.AgentID.Empty() {
		return fmt.Errorf("%w: memory agent id is required", domain.ErrInvalidArgument)
	}
	if memory.Scope == "" || !memory.Scope.Valid() {
		return fmt.Errorf("%w: unsupported memory scope %q", domain.ErrInvalidArgument, memory.Scope)
	}
	if memory.Scope.Shared() && memory.Sensitivity == domain.MemorySensitivityHighlySensitive {
		return fmt.Errorf("%w: highly sensitive memory cannot be shared", domain.ErrInvalidArgument)
	}
	if memory.Lifecycle == domain.MemoryLifecycleDormant && !memory.DeletedAt.IsZero() {
		return fmt.Errorf("%w: dormant memory cannot have deleted_at", domain.ErrInvalidArgument)
	}
	return nil
}

func normalizeSource(memory domain.Memory, source domain.MemorySource, version uint64, index int) (domain.MemorySource, error) {
	if source.MemoryID.Empty() {
		source.MemoryID = memory.ID
	}
	if source.MemoryID != memory.ID {
		return domain.MemorySource{}, fmt.Errorf("%w: memory source references another memory", domain.ErrInvalidArgument)
	}
	source.MemoryVersion = version
	if source.ID.Empty() {
		source.ID = domain.ID(fmt.Sprintf("%s:v%d:%d", memory.ID, version, index))
	}
	if source.CreatedAt.IsZero() {
		source.CreatedAt = memory.UpdatedAt
	}
	source.SourceType = strings.TrimSpace(source.SourceType)
	if err := source.Validate(); err != nil {
		return domain.MemorySource{}, err
	}
	return source, nil
}
