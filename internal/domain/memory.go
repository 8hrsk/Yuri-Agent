package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MemoryKind identifies the durable store a memory belongs to. A memory is
// additionally scoped to its owning named agent; a conversation is only a
// source of evidence and is not a memory namespace.
type MemoryKind string

const (
	MemoryKindCore         MemoryKind = "core"
	MemoryKindUserModel    MemoryKind = "user_model"
	MemoryKindEpisodic     MemoryKind = "episodic"
	MemoryKindSemantic     MemoryKind = "semantic"
	MemoryKindRelationship MemoryKind = "relationship"
	MemoryKindProcedural   MemoryKind = "procedural"
)

func (k MemoryKind) Valid() bool {
	switch k {
	case MemoryKindCore, MemoryKindUserModel, MemoryKindEpisodic,
		MemoryKindSemantic, MemoryKindRelationship, MemoryKindProcedural:
		return true
	default:
		return false
	}
}

// MemoryNature describes what kind of claim the content represents. It is
// deliberately separate from MemoryKind: for example, an episodic memory can
// contain either a fact or an inference.
type MemoryNature string

const (
	MemoryNatureFact      MemoryNature = "fact"
	MemoryNatureOpinion   MemoryNature = "opinion"
	MemoryNatureEmotion   MemoryNature = "emotion"
	MemoryNatureInference MemoryNature = "inference"
	// MemoryNatureFiction marks owner-authored identity material that the
	// character may remember as its subjective past. It must never be treated
	// as evidence about the owner, the real world, permissions, or capabilities.
	MemoryNatureFiction MemoryNature = "fiction"
)

func (n MemoryNature) Valid() bool {
	switch n {
	case MemoryNatureFact, MemoryNatureOpinion, MemoryNatureEmotion, MemoryNatureInference, MemoryNatureFiction:
		return true
	default:
		return false
	}
}

// MemoryLifecycle is the durable lifecycle of a memory projection. Deleted
// is a tombstone for a memory version; it does not delete source transcript
// messages.
type MemoryLifecycle string

const (
	MemoryLifecycleActive  MemoryLifecycle = "active"
	MemoryLifecycleDormant MemoryLifecycle = "dormant"
	MemoryLifecycleDeleted MemoryLifecycle = "deleted"
)

func (l MemoryLifecycle) Valid() bool {
	switch l {
	case MemoryLifecycleActive, MemoryLifecycleDormant, MemoryLifecycleDeleted:
		return true
	default:
		return false
	}
}

// MemoryScope identifies who may retrieve a memory. Shared records remain
// owned by their creating agent, but become visible to the other local agents
// only after an explicit owner publication.
type MemoryScope string

const (
	MemoryScopeAgentPrivate       MemoryScope = "agent_private"
	MemoryScopeOwnerShared        MemoryScope = "owner_shared"
	MemoryScopeInstallationShared MemoryScope = "installation_shared"
)

func (s MemoryScope) Valid() bool {
	switch s {
	case "", MemoryScopeAgentPrivate, MemoryScopeOwnerShared, MemoryScopeInstallationShared:
		return true
	default:
		return false
	}
}

func (s MemoryScope) Shared() bool {
	return s == MemoryScopeOwnerShared || s == MemoryScopeInstallationShared
}

// MemoryState is an architectural alias used by context and reflection code.
type MemoryState = MemoryLifecycle

const (
	MemoryStateActive  = MemoryLifecycleActive
	MemoryStateDormant = MemoryLifecycleDormant
	MemoryStateDeleted = MemoryLifecycleDeleted
)

// MemorySensitivity controls whether a candidate may be promoted into the
// normal active context. Values are intentionally coarse; policy adapters can
// impose stricter rules without changing the storage contract.
type MemorySensitivity string

const (
	MemorySensitivityPublic          MemorySensitivity = "public"
	MemorySensitivityPrivate         MemorySensitivity = "private"
	MemorySensitivitySensitive       MemorySensitivity = "sensitive"
	MemorySensitivityHighlySensitive MemorySensitivity = "highly_sensitive"
)

func (s MemorySensitivity) Valid() bool {
	switch s {
	case MemorySensitivityPublic, MemorySensitivityPrivate,
		MemorySensitivitySensitive, MemorySensitivityHighlySensitive:
		return true
	default:
		return false
	}
}

// MemoryRetention is a hint used by the background memory service. It is not
// a permission to erase original user messages.
type MemoryRetention string

const (
	MemoryRetentionPermanent MemoryRetention = "permanent"
	MemoryRetentionDecay     MemoryRetention = "decay"
	MemoryRetentionSession   MemoryRetention = "session"
	MemoryRetentionUntilDate MemoryRetention = "until_date"
)

func (r MemoryRetention) Valid() bool {
	switch r {
	case MemoryRetentionPermanent, MemoryRetentionDecay,
		MemoryRetentionSession, MemoryRetentionUntilDate:
		return true
	default:
		return false
	}
}

// Memory is the current view of one logical memory. The SQLite adapter keeps
// every revision in an append-only journal and exposes the latest revision as
// this value. Content is the human-readable representation; ContentJSON can
// carry structured data and must be valid JSON when supplied.
type Memory struct {
	ID                   ID                `json:"id"`
	AgentID              ID                `json:"agent_id"`
	Scope                MemoryScope       `json:"scope"`
	Version              uint64            `json:"version"`
	Kind                 MemoryKind        `json:"kind"`
	Nature               MemoryNature      `json:"nature"`
	Content              string            `json:"content"`
	ContentJSON          string            `json:"content_json,omitempty"`
	Summary              string            `json:"summary,omitempty"`
	Confidence           float64           `json:"confidence"`
	Salience             float64           `json:"salience"`
	Valence              float64           `json:"valence"`
	Sensitivity          MemorySensitivity `json:"sensitivity"`
	Retention            MemoryRetention   `json:"retention"`
	Lifecycle            MemoryLifecycle   `json:"lifecycle"`
	Pinned               bool              `json:"pinned"`
	HiddenFromCore       bool              `json:"hidden_from_core"`
	CanonicalKey         string            `json:"canonical_key,omitempty"`
	EmbeddingVersion     string            `json:"embedding_version,omitempty"`
	AccessCount          int64             `json:"access_count"`
	LastAccessedAt       time.Time         `json:"last_accessed_at,omitempty"`
	LastRecalledAt       time.Time         `json:"last_recalled_at,omitempty"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	DormantAt            time.Time         `json:"dormant_at,omitempty"`
	DeletedAt            time.Time         `json:"deleted_at,omitempty"`
	Reason               string            `json:"reason,omitempty"`
	SourceRunID          ID                `json:"source_run_id,omitempty"`
	SourceConversationID ID                `json:"source_conversation_id,omitempty"`
	SourceMessageID      ID                `json:"source_message_id,omitempty"`
}

// Valid performs storage-independent validation. Zero Version is accepted for
// a new candidate and is normalized to one by the SQLite repository.
func (m Memory) Valid() bool { return m.Validate() == nil }

func (m Memory) Validate() error {
	if m.ID.Empty() || !m.Kind.Valid() || !m.Nature.Valid() || !m.Scope.Valid() ||
		!m.Sensitivity.Valid() || !m.Retention.Valid() || !m.Lifecycle.Valid() {
		return fmt.Errorf("%w: invalid memory identity or enum", ErrInvalidArgument)
	}
	if m.Version == 0 {
		return fmt.Errorf("%w: memory version must be positive", ErrInvalidArgument)
	}
	if strings.TrimSpace(m.ContentJSON) != "" && !json.Valid([]byte(m.ContentJSON)) {
		return fmt.Errorf("%w: memory content_json must be valid JSON", ErrInvalidArgument)
	}
	if strings.TrimSpace(m.Content) == "" && strings.TrimSpace(m.ContentJSON) == "" {
		return fmt.Errorf("%w: memory content or content_json is required", ErrInvalidArgument)
	}
	if m.Confidence < 0 || m.Confidence > 1 || m.Salience < 0 || m.Salience > 1 || m.Valence < -1 || m.Valence > 1 {
		return fmt.Errorf("%w: memory scores are out of range", ErrInvalidArgument)
	}
	if m.AccessCount < 0 {
		return fmt.Errorf("%w: memory access count cannot be negative", ErrInvalidArgument)
	}
	if m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: memory timestamps are required", ErrInvalidArgument)
	}
	if m.Lifecycle == MemoryLifecycleDormant && m.DeletedAt.IsZero() == false {
		return fmt.Errorf("%w: dormant memory cannot have deleted_at", ErrInvalidArgument)
	}
	if m.Lifecycle == MemoryLifecycleActive && !m.DeletedAt.IsZero() {
		return fmt.Errorf("%w: active memory cannot have deleted_at", ErrInvalidArgument)
	}
	if m.Lifecycle == MemoryLifecycleDeleted && m.DeletedAt.IsZero() {
		return fmt.Errorf("%w: deleted memory requires deleted_at", ErrInvalidArgument)
	}
	return nil
}

func (m Memory) IsActive() bool  { return m.Lifecycle == MemoryLifecycleActive }
func (m Memory) IsDormant() bool { return m.Lifecycle == MemoryLifecycleDormant }
func (m Memory) IsDeleted() bool { return m.Lifecycle == MemoryLifecycleDeleted }

// MemorySource links a memory revision to evidence. The source is metadata,
// not a copy of the original text; ExcerptHash allows integrity checks without
// leaking potentially sensitive excerpts into indexes or logs.
type MemorySource struct {
	ID             ID        `json:"id"`
	MemoryID       ID        `json:"memory_id"`
	MemoryVersion  uint64    `json:"memory_version"`
	SourceType     string    `json:"source_type"`
	SourceID       ID        `json:"source_id,omitempty"`
	RunID          ID        `json:"run_id,omitempty"`
	ConversationID ID        `json:"conversation_id,omitempty"`
	MessageID      ID        `json:"message_id,omitempty"`
	ExcerptHash    string    `json:"excerpt_hash,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s MemorySource) Valid() bool {
	return s.Validate() == nil
}

func (s MemorySource) Validate() error {
	if s.ID.Empty() || s.MemoryID.Empty() || s.MemoryVersion == 0 || strings.TrimSpace(s.SourceType) == "" || s.CreatedAt.IsZero() {
		return fmt.Errorf("%w: invalid memory source", ErrInvalidArgument)
	}
	return nil
}
