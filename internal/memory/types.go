// Package memory contains the provider-neutral memory and context machinery
// used by Yuri. The package deliberately does not know about SQLite, an LLM
// SDK, Wails, or the future reflection/persona implementation. Persistent
// adapters implement Store and optional search/index ports.
package memory

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// These aliases keep the memory package ergonomic while making domain.Memory
// and domain.MemorySource the one canonical persistent model. New code should
// use the domain names when it crosses an application/storage boundary.
type Memory = domain.Memory
type MemorySource = domain.MemorySource
type Kind = domain.MemoryKind
type ContentType = domain.MemoryNature
type Sensitivity = domain.MemorySensitivity
type LifecycleState = domain.MemoryLifecycle

const (
	KindCore         = domain.MemoryKindCore
	KindUserProfile  = domain.MemoryKindUserModel
	KindUserModel    = domain.MemoryKindUserModel
	KindEpisodic     = domain.MemoryKindEpisodic
	KindSemantic     = domain.MemoryKindSemantic
	KindProcedural   = domain.MemoryKindProcedural
	KindRelationship = domain.MemoryKindRelationship

	ContentFact      = domain.MemoryNatureFact
	ContentOpinion   = domain.MemoryNatureOpinion
	ContentEmotion   = domain.MemoryNatureEmotion
	ContentInference = domain.MemoryNatureInference

	SensitivityPublic          = domain.MemorySensitivityPublic
	SensitivityPrivate         = domain.MemorySensitivityPrivate
	SensitivitySensitive       = domain.MemorySensitivitySensitive
	SensitivityHighlySensitive = domain.MemorySensitivityHighlySensitive

	StateActive  = domain.MemoryLifecycleActive
	StateDormant = domain.MemoryLifecycleDormant
	StateDeleted = domain.MemoryLifecycleDeleted
)

// DecayPolicy is derived policy rather than persistent record state. The
// durable memory model stores lifecycle and timestamps; a policy provider can
// later choose different curves without rewriting every record.
type DecayPolicy struct {
	HalfLife         time.Duration `json:"half_life"`
	DormantThreshold float64       `json:"dormant_threshold"`
	DormantAfter     time.Duration `json:"dormant_after"`
	NeverDormant     bool          `json:"never_dormant"`
}

func DefaultDecayPolicy(kind Kind) DecayPolicy {
	policy := DecayPolicy{
		HalfLife:         45 * 24 * time.Hour,
		DormantThreshold: 0.12,
		DormantAfter:     180 * 24 * time.Hour,
	}
	switch kind {
	case KindCore, KindUserProfile, KindProcedural:
		policy.HalfLife = 365 * 24 * time.Hour
		policy.DormantAfter = 0
		policy.NeverDormant = true
	case KindEpisodic:
		policy.HalfLife = 14 * 24 * time.Hour
		policy.DormantAfter = 120 * 24 * time.Hour
	}
	return policy
}

func (p DecayPolicy) normalize(kind Kind) DecayPolicy {
	defaultPolicy := DefaultDecayPolicy(kind)
	if p.HalfLife <= 0 {
		p.HalfLife = defaultPolicy.HalfLife
	}
	if p.DormantThreshold <= 0 || p.DormantThreshold > 1 {
		p.DormantThreshold = defaultPolicy.DormantThreshold
	}
	return p
}

// Normalize prepares extractor output for deterministic deduplication. It
// fills only storage-safe defaults and never interprets content as an
// instruction.
func Normalize(m domain.Memory, now time.Time) (domain.Memory, error) {
	if now.IsZero() {
		return domain.Memory{}, fmt.Errorf("%w: memory timestamp is required", domain.ErrInvalidArgument)
	}
	m.Content = strings.TrimSpace(m.Content)
	if m.Kind == "" {
		m.Kind = domain.MemoryKindSemantic
	}
	if m.Nature == "" {
		m.Nature = domain.MemoryNatureFact
	}
	if m.Sensitivity == "" {
		m.Sensitivity = domain.MemorySensitivityPrivate
	}
	if m.Retention == "" {
		m.Retention = domain.MemoryRetentionDecay
	}
	if m.Lifecycle == "" {
		m.Lifecycle = domain.MemoryLifecycleActive
	}
	if m.Confidence == 0 {
		m.Confidence = 0.7
	}
	if m.Salience == 0 {
		m.Salience = 0.5
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now.UTC()
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = m.CreatedAt
	}
	if m.Version == 0 {
		m.Version = 1
	}
	return m, m.Validate()
}

type MemoryOperation string

const (
	OperationCreate  MemoryOperation = "create"
	OperationUpdate  MemoryOperation = "update"
	OperationMerge   MemoryOperation = "merge"
	OperationRestore MemoryOperation = "restore"
	OperationDormant MemoryOperation = "dormant"
	OperationHide    MemoryOperation = "hide"
	OperationForget  MemoryOperation = "forget"
	OperationDelete  MemoryOperation = "delete"
	OperationTouch   MemoryOperation = "touch"
)

func (o MemoryOperation) Valid() bool {
	switch o {
	case OperationCreate, OperationUpdate, OperationMerge, OperationRestore,
		OperationDormant, OperationHide, OperationForget, OperationDelete, OperationTouch:
		return true
	default:
		return false
	}
}

// MemoryRevision is an append-only journal entry. Its snapshot is the
// canonical domain.Memory projection, not a second persistent Memory type.
type MemoryRevision struct {
	ID            domain.ID       `json:"id"`
	MemoryID      domain.ID       `json:"memory_id"`
	Operation     MemoryOperation `json:"operation"`
	Snapshot      domain.Memory   `json:"snapshot"`
	ParentVersion uint64          `json:"parent_version"`
	Reason        string          `json:"reason,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// MemoryVersion is kept as a naming alias for callers that use the schema's
// terminology. Both names carry the same append-only revision contract.
type MemoryVersion = MemoryRevision

func (v MemoryRevision) Valid() error {
	if v.ID.Empty() || v.MemoryID.Empty() || !v.Operation.Valid() || v.Snapshot.ID != v.MemoryID || v.ParentVersion > v.Snapshot.Version {
		return fmt.Errorf("%w: invalid memory revision", domain.ErrInvalidArgument)
	}
	if err := v.Snapshot.Validate(); err != nil {
		return err
	}
	if v.CreatedAt.IsZero() {
		return fmt.Errorf("%w: memory revision timestamp is required", domain.ErrInvalidArgument)
	}
	return nil
}

type MemoryChange struct {
	Memory   domain.Memory
	Revision *MemoryRevision
	Sources  []domain.MemorySource
}

func (c MemoryChange) Validate() error {
	if err := c.Memory.Validate(); err != nil {
		return err
	}
	if c.Revision == nil {
		return fmt.Errorf("%w: memory revision is required", domain.ErrInvalidArgument)
	}
	if err := c.Revision.Valid(); err != nil {
		return err
	}
	if c.Revision.MemoryID != c.Memory.ID || c.Revision.Snapshot.Version != c.Memory.Version {
		return fmt.Errorf("%w: memory revision does not match projection", domain.ErrInvalidArgument)
	}
	for _, source := range c.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.MemoryID != c.Memory.ID || source.MemoryVersion != c.Memory.Version {
			return fmt.Errorf("%w: memory source does not match projection", domain.ErrInvalidArgument)
		}
	}
	return nil
}

// TranscriptMessage is the minimal immutable archive record needed by an
// extractor. It avoids importing the SQLite adapter's Message type.
type TranscriptMessage struct {
	ID             domain.ID `json:"id"`
	ConversationID domain.ID `json:"conversation_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"created_at"`
}

func (m TranscriptMessage) Valid() error {
	if m.ID.Empty() || m.ConversationID.Empty() || strings.TrimSpace(m.Role) == "" || strings.TrimSpace(m.Content) == "" || m.CreatedAt.IsZero() {
		return fmt.Errorf("%w: invalid transcript message", domain.ErrInvalidArgument)
	}
	return nil
}

// Turn is handed to Extractor after a successful foreground/background run.
// Extractors may return an empty slice; that is a valid, expected result.
type Turn struct {
	RunID          domain.ID
	ConversationID domain.ID
	Messages       []TranscriptMessage
	Now            time.Time
}

func (t Turn) Valid() error {
	if t.ConversationID.Empty() {
		return fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	if t.Now.IsZero() {
		return fmt.Errorf("%w: turn timestamp is required", domain.ErrInvalidArgument)
	}
	for _, message := range t.Messages {
		if err := message.Valid(); err != nil {
			return err
		}
	}
	return nil
}

type CandidateOperation string

const (
	CandidateAuto   CandidateOperation = "auto"
	CandidateCreate CandidateOperation = "create"
	CandidateUpdate CandidateOperation = "update"
	CandidateForget CandidateOperation = "forget"
	CandidateNoop   CandidateOperation = "noop"
)

// Candidate is provider-neutral extractor output. MatchID and DedupKey are
// hints only: the engine re-checks them against authoritative Store data.
type Candidate struct {
	Memory    domain.Memory         `json:"memory"`
	Operation CandidateOperation    `json:"operation"`
	MatchID   domain.ID             `json:"match_id,omitempty"`
	DedupKey  string                `json:"dedup_key,omitempty"`
	Reason    string                `json:"reason,omitempty"`
	Sources   []domain.MemorySource `json:"sources,omitempty"`
}

// RecallMode controls whether dormant records are eligible. Normal
// foreground context uses Automatic; an explicit request such as “найди в
// прошлых диалогах” uses Deliberate.
type RecallMode string

const (
	RecallAutomatic  RecallMode = "automatic"
	RecallDeliberate RecallMode = "deliberate"
)

type Budget struct {
	MaxItems  int `json:"max_items"`
	MaxTokens int `json:"max_tokens"`
	MaxChars  int `json:"max_chars"`
}

func (b Budget) normalize(defaultItems int) Budget {
	if b.MaxItems <= 0 {
		b.MaxItems = defaultItems
	}
	if b.MaxTokens <= 0 && b.MaxChars <= 0 {
		b.MaxTokens = 2000
	}
	if b.MaxChars <= 0 && b.MaxTokens > 0 {
		b.MaxChars = b.MaxTokens * 4
	}
	if b.MaxTokens <= 0 && b.MaxChars > 0 {
		b.MaxTokens = int(math.Ceil(float64(b.MaxChars) / 4))
	}
	return b
}

type MemoryFilter struct {
	States         []LifecycleState
	Kinds          []Kind
	IncludeDormant bool
	IncludeHidden  bool
	IncludeDeleted bool
	Limit          int
}

type RecallOptions struct {
	Mode           RecallMode
	Limit          int
	Budget         Budget
	Now            time.Time
	IncludeHidden  bool
	RestoreDormant bool
	ConversationID domain.ID
}

type SourceEvidence struct {
	Sources []domain.MemorySource `json:"sources,omitempty"`
	Snippet string                `json:"snippet,omitempty"`
}

type RecallResult struct {
	Memory         domain.Memory  `json:"memory"`
	Score          float64        `json:"score"`
	LexicalScore   float64        `json:"lexical_score"`
	VectorScore    float64        `json:"vector_score"`
	RecencyScore   float64        `json:"recency_score"`
	SalienceScore  float64        `json:"salience_score"`
	AffectiveScore float64        `json:"affective_score"`
	Dormant        bool           `json:"dormant"`
	Evidence       SourceEvidence `json:"evidence"`
}

type ArchiveHit struct {
	MessageID      domain.ID `json:"message_id"`
	ConversationID domain.ID `json:"conversation_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"created_at"`
	Score          float64   `json:"score"`
	Snippet        string    `json:"snippet,omitempty"`
}

type ArchiveSearchOptions struct {
	Limit           int
	Budget          Budget
	IncludeArchived bool
}

// ContextEntry is a bounded, provenance-aware value ready for prompt
// assembly. Content remains data and is wrapped by FormatContext.
type ContextEntry struct {
	Memory   domain.Memory
	Score    float64
	Evidence SourceEvidence
}

type ContextSnapshot struct {
	CreatedAt time.Time
	Entries   []ContextEntry
	Text      string
	Tokens    int
	Chars     int
}
