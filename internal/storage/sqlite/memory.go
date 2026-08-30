package sqlite

import (
	"database/sql"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// MemoryListOptions controls the current-memory projection returned by List.
// By default only active memories are returned. The UI can opt into dormant
// and deleted records for review without changing normal retrieval behavior.
type MemoryListOptions struct {
	AgentID domain.ID
	Scope   domain.MemoryScope
	Kind    domain.MemoryKind
	// Lifecycle restricts the projection to one exact lifecycle state. It is
	// applied in SQL, before LIMIT/OFFSET, and overrides the
	// IncludeDormant/IncludeDeleted shorthands. Callers that page a single
	// lifecycle tab must use this instead of filtering the returned page:
	// filtering after pagination yields short and unstable pages.
	Lifecycle domain.MemoryLifecycle
	// ExcludeSensitivity drops one sensitivity class in SQL so that callers
	// with a hard item budget (ListCore) can push their whole predicate down
	// instead of over-fetching and discarding rows in Go.
	ExcludeSensitivity domain.MemorySensitivity
	IncludeDormant     bool
	IncludeDeleted     bool
	IncludeHidden      bool
	ExcludeHidden      bool
	Limit              int
	Offset             int
}

// CoreMemoryOptions bounds the stable prefix injected into a new context.
// MaxTokens is an approximate UTF-8 token budget; zero means unbounded.
type CoreMemoryOptions struct {
	AgentID   domain.ID
	Scope     domain.MemoryScope
	MaxItems  int
	MaxTokens int
}

// MemorySearchOptions describes semantic/lexical memory retrieval. Vector
// similarity is supplied by the memory service; this repository owns the FTS
// lexical leg and returns a deterministic lexical score and provenance.
type MemorySearchOptions struct {
	AgentID        domain.ID
	Scope          domain.MemoryScope
	IncludeDormant bool
	// Deliberate is an explicit opt-in alias for IncludeDormant. It is useful
	// for callers representing a user-requested retrospective search.
	Deliberate     bool
	IncludeDeleted bool
	IncludeHidden  bool
	ExcludeHidden  bool
	// Lifecycle restricts the search to one exact lifecycle state. It is
	// applied in SQL before LIMIT/OFFSET and overrides the IncludeDormant,
	// Deliberate and IncludeDeleted shorthands, so a caller paging through a
	// single state gets full pages instead of a filtered-down remainder.
	Lifecycle domain.MemoryLifecycle
	Kind      domain.MemoryKind
	Limit     int
	Offset    int
	MaxTokens int
}

// MemorySearchHit is a bounded lexical memory result. Sources are loaded
// separately from the memory content so the caller can render provenance and
// choose whether to fetch the original transcript.
type MemorySearchHit struct {
	Memory  domain.Memory
	Snippet string
	Score   float64
	Sources []domain.MemorySource
}

// MemoryVersionRecord exposes journal metadata needed by reflection and audit
// consumers without making them depend on the SQL schema. Memory contains the
// immutable snapshot for the revision.
type MemoryVersionRecord struct {
	Memory        domain.Memory
	RevisionID    domain.ID
	Operation     string
	ParentVersion uint64
	Reason        string
}

// MemoryVersionMetadata lets an application service preserve its own
// revision/event identity while the repository enforces optimistic versioning.
// Empty fields receive deterministic repository defaults.
type MemoryVersionMetadata struct {
	RevisionID    domain.ID
	Operation     string
	ParentVersion uint64
	Reason        string
}

// MemoryRepository persists versioned memories and their provenance. Every
// user-visible change appends a row to memory_versions; memory_heads is only a
// rebuildable current-version projection.
type MemoryRepository struct {
	db *sql.DB
}

func NewMemoryRepository(database *sql.DB) *MemoryRepository {
	return &MemoryRepository{db: database}
}
