package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// MemoryListOptions controls the current-memory projection returned by List.
// By default only active memories are returned. The UI can opt into dormant
// and deleted records for review without changing normal retrieval behavior.
type MemoryListOptions struct {
	Kind           domain.MemoryKind
	IncludeDormant bool
	IncludeDeleted bool
	IncludeHidden  bool
	ExcludeHidden  bool
	Limit          int
	Offset         int
}

// CoreMemoryOptions bounds the stable prefix injected into a new context.
// MaxTokens is an approximate UTF-8 token budget; zero means unbounded.
type CoreMemoryOptions struct {
	MaxItems  int
	MaxTokens int
}

// MemorySearchOptions describes semantic/lexical memory retrieval. Vector
// similarity is supplied by the memory service; this repository owns the FTS
// lexical leg and returns a deterministic lexical score and provenance.
type MemorySearchOptions struct {
	IncludeDormant bool
	// Deliberate is an explicit opt-in alias for IncludeDormant. It is useful
	// for callers representing a user-requested retrospective search.
	Deliberate     bool
	IncludeDeleted bool
	IncludeHidden  bool
	ExcludeHidden  bool
	Kind           domain.MemoryKind
	Limit          int
	Offset         int
	MaxTokens      int
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

// Create appends version one. An optional source slice can be supplied for
// provenance. The slice is variadic to keep the common no-source candidate
// call concise while still making source attachment atomic with the memory.
func (r *MemoryRepository) Create(ctx context.Context, memory domain.Memory, sources ...[]domain.MemorySource) error {
	return r.createWithMetadata(ctx, memory, nil, sources...)
}

// CreateWithMetadata is the create-side counterpart to
// AppendVersionWithMetadata. The revision parent is always zero for a new
// logical memory.
func (r *MemoryRepository) CreateWithMetadata(ctx context.Context, memory domain.Memory, metadata *MemoryVersionMetadata, sources ...[]domain.MemorySource) error {
	return r.createWithMetadata(ctx, memory, metadata, sources...)
}

func (r *MemoryRepository) createWithMetadata(ctx context.Context, memory domain.Memory, metadata *MemoryVersionMetadata, sources ...[]domain.MemorySource) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	memory = normalizeMemoryForCreate(memory)
	if err := validateMemoryForStorage(memory); err != nil {
		return err
	}
	if metadata != nil && metadata.ParentVersion != 0 {
		return fmt.Errorf("%w: create memory cannot have a parent version", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create memory", err)
	}
	defer tx.Rollback()
	var existing uint64
	err = tx.QueryRowContext(ctx, `SELECT version FROM memory_heads WHERE memory_id = ?`, string(memory.ID)).Scan(&existing)
	if err == nil {
		return domain.ErrConflict
	}
	if !isNoRows(err) {
		return wrappedSQLError("check memory head", err)
	}
	if err := r.appendVersionTx(ctx, tx, memory, firstSources(sources), 0, "create", metadata, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit create memory", err)
	}
	return nil
}

// Save appends the next version. memory.Version is the desired new revision
// and therefore must equal current.Version+1. Sources are optional; existing
// provenance is copied forward atomically before new sources are appended.
func (r *MemoryRepository) Save(ctx context.Context, memory domain.Memory, sources ...[]domain.MemorySource) error {
	return r.saveWithOperation(ctx, memory, "update", sources...)
}

func (r *MemoryRepository) saveWithOperation(ctx context.Context, memory domain.Memory, operation string, sources ...[]domain.MemorySource) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if memory.Version == 0 {
		return fmt.Errorf("%w: memory version must be positive when saving", domain.ErrInvalidArgument)
	}
	if err := validateMemoryForStorage(memory); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin save memory", err)
	}
	defer tx.Rollback()
	currentVersion, err := memoryHeadVersion(ctx, tx, memory.ID)
	if err != nil {
		return err
	}
	if memory.Version != currentVersion+1 {
		return domain.ErrConflict
	}
	if err := r.appendVersionTx(ctx, tx, memory, firstSources(sources), currentVersion, operation, nil, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit save memory", err)
	}
	return nil
}

// SaveVersion is an explicit name for callers that want to emphasize the
// append-only behavior. It is equivalent to Save.
func (r *MemoryRepository) SaveVersion(ctx context.Context, memory domain.Memory, sources []domain.MemorySource) error {
	return r.Save(ctx, memory, sources)
}

// Update is a compatibility alias for Save used by application services that
// call all versioned current projections "updates".
func (r *MemoryRepository) Update(ctx context.Context, memory domain.Memory, sources ...[]domain.MemorySource) error {
	return r.Save(ctx, memory, sources...)
}

// AppendVersion appends a revision after checking expectedVersion. It returns
// the newly stored current memory, which is convenient for autonomous memory
// writes and lifecycle transitions.
func (r *MemoryRepository) AppendVersion(ctx context.Context, memory domain.Memory, expectedVersion uint64, sources ...[]domain.MemorySource) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	if memory.Version == 0 {
		return domain.Memory{}, fmt.Errorf("%w: memory version must be positive when appending", domain.ErrInvalidArgument)
	}
	if err := validateMemoryForStorage(memory); err != nil {
		return domain.Memory{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("begin append memory", err)
	}
	defer tx.Rollback()
	currentVersion, err := memoryHeadVersion(ctx, tx, memory.ID)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != currentVersion || memory.Version != currentVersion+1 {
		return domain.Memory{}, domain.ErrConflict
	}
	if err := r.appendVersionTx(ctx, tx, memory, firstSources(sources), currentVersion, "update", nil, nil); err != nil {
		return domain.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, wrappedSQLError("commit append memory", err)
	}
	return memory, nil
}

// AppendVersionWithMetadata is the atomic application-service boundary for a
// memory change. It is equivalent to AppendVersion while retaining the
// caller's immutable revision ID, operation and reason in the journal.
func (r *MemoryRepository) AppendVersionWithMetadata(ctx context.Context, memory domain.Memory, expectedVersion uint64, metadata MemoryVersionMetadata, sources ...[]domain.MemorySource) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	if memory.Version == 0 {
		return domain.Memory{}, fmt.Errorf("%w: memory version must be positive when appending", domain.ErrInvalidArgument)
	}
	if err := validateMemoryForStorage(memory); err != nil {
		return domain.Memory{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("begin append memory metadata", err)
	}
	defer tx.Rollback()
	currentVersion, err := memoryHeadVersion(ctx, tx, memory.ID)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != currentVersion || memory.Version != currentVersion+1 {
		return domain.Memory{}, domain.ErrConflict
	}
	if metadata.ParentVersion != 0 && metadata.ParentVersion != currentVersion {
		return domain.Memory{}, domain.ErrConflict
	}
	if metadata.ParentVersion == 0 {
		metadata.ParentVersion = currentVersion
	}
	if err := r.appendVersionTx(ctx, tx, memory, firstSources(sources), currentVersion, metadata.Operation, &metadata, nil); err != nil {
		return domain.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, wrappedSQLError("commit append memory metadata", err)
	}
	return memory, nil
}

func firstSources(sources [][]domain.MemorySource) []domain.MemorySource {
	if len(sources) == 0 {
		return nil
	}
	return sources[0]
}

// Get returns the current revision. Deleted memories are returned as
// tombstones so the caller can explain or restore a user-visible deletion.
func (r *MemoryRepository) Get(ctx context.Context, id domain.ID) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	if id.Empty() {
		return domain.Memory{}, fmt.Errorf("%w: memory id is required", domain.ErrInvalidArgument)
	}
	return getCurrentMemory(ctx, r.db, id)
}

// GetVersion returns an immutable journal revision rather than the current
// projection. It is used by provenance viewers and rollback tooling.
func (r *MemoryRepository) GetVersion(ctx context.Context, id domain.ID, version uint64) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	if id.Empty() || version == 0 {
		return domain.Memory{}, fmt.Errorf("%w: memory id and positive version are required", domain.ErrInvalidArgument)
	}
	return getMemoryVersion(ctx, r.db, id, version)
}

// GetVersionRecord returns an immutable snapshot together with journal
// metadata used for audit, rollback and reflection explainability.
func (r *MemoryRepository) GetVersionRecord(ctx context.Context, id domain.ID, version uint64) (MemoryVersionRecord, error) {
	if err := requireDatabase(r.db); err != nil {
		return MemoryVersionRecord{}, err
	}
	if err := contextErr(ctx); err != nil {
		return MemoryVersionRecord{}, err
	}
	if id.Empty() || version == 0 {
		return MemoryVersionRecord{}, fmt.Errorf("%w: memory id and positive version are required", domain.ErrInvalidArgument)
	}
	var revisionID, operation, reason string
	var parentVersion uint64
	if err := r.db.QueryRowContext(ctx, `
		SELECT revision_id, operation, parent_version, reason
		FROM memory_versions WHERE memory_id = ? AND version = ?`, string(id), version).
		Scan(&revisionID, &operation, &parentVersion, &reason); err != nil {
		return MemoryVersionRecord{}, wrappedSQLError("get memory version metadata", err)
	}
	memory, err := getMemoryVersion(ctx, r.db, id, version)
	if err != nil {
		return MemoryVersionRecord{}, err
	}
	return MemoryVersionRecord{Memory: memory, RevisionID: domain.ID(revisionID), Operation: operation, ParentVersion: parentVersion, Reason: reason}, nil
}

// ListVersions returns immutable revisions newest first. A positive limit
// bounds the result; zero or omitted means all revisions.
func (r *MemoryRepository) ListVersions(ctx context.Context, id domain.ID, limit ...int) ([]MemoryVersionRecord, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if id.Empty() {
		return nil, fmt.Errorf("%w: memory id is required", domain.ErrInvalidArgument)
	}
	if len(limit) > 0 && limit[0] < 0 {
		return nil, fmt.Errorf("%w: memory version limit cannot be negative", domain.ErrInvalidArgument)
	}
	query := `
		SELECT version, revision_id, operation, parent_version, reason
		FROM memory_versions WHERE memory_id = ?
		ORDER BY version DESC`
	args := []any{string(id)}
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list memory versions", err)
	}
	metadata := make([]struct {
		version       uint64
		revisionID    string
		operation     string
		parentVersion uint64
		reason        string
	}, 0)
	for rows.Next() {
		var version, parentVersion uint64
		var revisionID, operation, reason string
		if err := rows.Scan(&version, &revisionID, &operation, &parentVersion, &reason); err != nil {
			rows.Close()
			return nil, wrappedSQLError("scan memory version metadata", err)
		}
		metadata = append(metadata, struct {
			version       uint64
			revisionID    string
			operation     string
			parentVersion uint64
			reason        string
		}{version: version, revisionID: revisionID, operation: operation, parentVersion: parentVersion, reason: reason})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrappedSQLError("iterate memory versions", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrappedSQLError("close memory versions", err)
	}
	result := make([]MemoryVersionRecord, 0, len(metadata))
	for _, item := range metadata {
		memory, err := getMemoryVersion(ctx, r.db, id, item.version)
		if err != nil {
			return nil, err
		}
		result = append(result, MemoryVersionRecord{Memory: memory, RevisionID: domain.ID(item.revisionID), Operation: item.operation, ParentVersion: item.parentVersion, Reason: item.reason})
	}
	return result, nil
}

// ListMemoryVersions is an architectural alias for ListVersions.
func (r *MemoryRepository) ListMemoryVersions(ctx context.Context, id domain.ID, limit ...int) ([]MemoryVersionRecord, error) {
	return r.ListVersions(ctx, id, limit...)
}

// GetCurrent is an explicit alias for Get for context assemblers that need to
// distinguish the current projection from an immutable journal revision.
func (r *MemoryRepository) GetCurrent(ctx context.Context, id domain.ID) (domain.Memory, error) {
	return r.Get(ctx, id)
}

// FindByCanonicalKey returns the current non-deleted memory used for
// deduplication. Empty keys are rejected because an empty key would collapse
// unrelated candidates into one record.
func (r *MemoryRepository) FindByCanonicalKey(ctx context.Context, key string, includeDormant ...bool) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return domain.Memory{}, fmt.Errorf("%w: canonical key is required", domain.ErrInvalidArgument)
	}
	dormant := len(includeDormant) > 0 && includeDormant[0]
	state := "mv.lifecycle_state = 'active'"
	if dormant {
		state = "mv.lifecycle_state IN ('active', 'dormant')"
	}
	row := r.db.QueryRowContext(ctx, memorySelectPrefix+`
		FROM memory_heads AS mh
		JOIN memory_versions AS mv ON mv.memory_id = mh.memory_id AND mv.version = mh.version
		WHERE mv.canonical_key = ? AND `+state+`
		ORDER BY mv.salience DESC, mv.updated_at DESC, mv.memory_id ASC
		LIMIT 1`, key)
	item, err := scanMemory(row)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("find memory by canonical key", err)
	}
	return item, nil
}

// List returns current memory versions in deterministic salience order. It is
// intentionally not the context assembler: callers that need token budgets
// should use ListCore or the memory service.
func (r *MemoryRepository) List(ctx context.Context, options ...MemoryListOptions) ([]domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	opts := MemoryListOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.Limit < 0 || opts.Offset < 0 {
		return nil, fmt.Errorf("%w: memory limit and offset cannot be negative", domain.ErrInvalidArgument)
	}
	where := []string{"1 = 1"}
	args := make([]any, 0, 3)
	if !opts.IncludeDeleted {
		if opts.IncludeDormant {
			where = append(where, "mv.lifecycle_state IN ('active', 'dormant')")
		} else {
			where = append(where, "mv.lifecycle_state = 'active'")
		}
	} else if !opts.IncludeDormant {
		where = append(where, "mv.lifecycle_state IN ('active', 'deleted')")
	}
	if opts.Kind != "" {
		if !opts.Kind.Valid() {
			return nil, fmt.Errorf("%w: invalid memory kind %q", domain.ErrInvalidArgument, opts.Kind)
		}
		where = append(where, "mv.kind = ?")
		args = append(args, string(opts.Kind))
	}
	if opts.ExcludeHidden && !opts.IncludeHidden {
		where = append(where, "mv.hidden_from_core = 0")
	}
	query := memorySelectPrefix + `
		FROM memory_heads AS mh
		JOIN memory_versions AS mv ON mv.memory_id = mh.memory_id AND mv.version = mh.version
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY mv.pinned DESC, mv.salience DESC, mv.confidence DESC, mv.updated_at DESC, mv.memory_id ASC`
	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		if opts.Limit <= 0 {
			query += " LIMIT -1"
		}
		query += " OFFSET ?"
		args = append(args, opts.Offset)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list memories", err)
	}
	defer rows.Close()
	result := make([]domain.Memory, 0)
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, wrappedSQLError("scan memory", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate memories", err)
	}
	return result, nil
}

// ListCore selects the bounded active core prefix. Dormant, deleted and
// HiddenFromCore records are excluded even when they are pinned. Pinned
// records sort first so explicit user curation survives normal salience decay.
func (r *MemoryRepository) ListCore(ctx context.Context, options ...CoreMemoryOptions) ([]domain.Memory, error) {
	opts := CoreMemoryOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.MaxItems < 0 || opts.MaxTokens < 0 {
		return nil, fmt.Errorf("%w: core limits cannot be negative", domain.ErrInvalidArgument)
	}
	items, err := r.List(ctx, MemoryListOptions{})
	if err != nil {
		return nil, err
	}
	result := make([]domain.Memory, 0, len(items))
	usedTokens := 0
	for _, item := range items {
		if !item.IsActive() || item.HiddenFromCore || item.Sensitivity == domain.MemorySensitivityHighlySensitive {
			continue
		}
		if opts.MaxItems > 0 && len(result) >= opts.MaxItems {
			break
		}
		itemTokens := approximateTokens(item.Content) + approximateTokens(item.Summary)
		if opts.MaxTokens > 0 && usedTokens+itemTokens > opts.MaxTokens {
			continue
		}
		result = append(result, item)
		usedTokens += itemTokens
	}
	return result, nil
}

// SelectCore is the architectural name for ListCore.
func (r *MemoryRepository) SelectCore(ctx context.Context, options ...CoreMemoryOptions) ([]domain.Memory, error) {
	return r.ListCore(ctx, options...)
}

// ListSources lists all provenance links for the current revision unless a
// specific version is supplied. The source transcript itself remains in the
// conversation/message repositories.
func (r *MemoryRepository) ListSources(ctx context.Context, id domain.ID, version ...uint64) ([]domain.MemorySource, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if id.Empty() {
		return nil, fmt.Errorf("%w: memory id is required", domain.ErrInvalidArgument)
	}
	var targetVersion uint64
	if len(version) > 0 && version[0] > 0 {
		targetVersion = version[0]
	} else {
		var err error
		targetVersion, err = memoryHeadVersion(ctx, r.db, id)
		if err != nil {
			return nil, err
		}
	}
	return listSources(ctx, r.db, id, targetVersion)
}

// MarkDormant appends a lifecycle revision that is excluded from ordinary
// retrieval. A deliberate search can request IncludeDormant/Deliberate.
func (r *MemoryRepository) MarkDormant(ctx context.Context, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.transitionLifecycle(ctx, id, expectedVersion, domain.MemoryLifecycleDormant, at, reason)
}

// Restore appends an active revision for a dormant or deleted memory while
// retaining the old state in the immutable journal.
func (r *MemoryRepository) Restore(ctx context.Context, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.transitionLifecycle(ctx, id, expectedVersion, domain.MemoryLifecycleActive, at, reason)
}

// SoftDelete creates a tombstone revision. Original conversation messages and
// evidence rows from prior revisions are never physically removed here.
func (r *MemoryRepository) SoftDelete(ctx context.Context, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.transitionLifecycle(ctx, id, expectedVersion, domain.MemoryLifecycleDeleted, at, reason)
}

// Forget is the user-facing alias for SoftDelete.
func (r *MemoryRepository) Forget(ctx context.Context, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.SoftDelete(ctx, id, expectedVersion, at, reason)
}

// Pin appends a revision that changes explicit core curation without mutating
// a previous version.
func (r *MemoryRepository) Pin(ctx context.Context, id domain.ID, expectedVersion uint64, pinned bool, at time.Time, reason string) (domain.Memory, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != current.Version {
		return domain.Memory{}, domain.ErrConflict
	}
	current.Version++
	current.Pinned = pinned
	current.UpdatedAt = at.UTC()
	current.Reason = reason
	if err := r.saveWithOperation(ctx, current, "pin"); err != nil {
		return domain.Memory{}, err
	}
	return current, nil
}

// HideFromCore changes only the core inclusion flag and keeps the complete
// record available to deliberate search and the memory UI.
func (r *MemoryRepository) HideFromCore(ctx context.Context, id domain.ID, expectedVersion uint64, hidden bool, at time.Time, reason string) (domain.Memory, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != current.Version {
		return domain.Memory{}, domain.ErrConflict
	}
	current.Version++
	current.HiddenFromCore = hidden
	current.UpdatedAt = at.UTC()
	current.Reason = reason
	if err := r.saveWithOperation(ctx, current, "hide"); err != nil {
		return domain.Memory{}, err
	}
	return current, nil
}

// RecordRecall appends access metadata, allowing decay services to base their
// decisions on durable recall history without mutating an existing version.
func (r *MemoryRepository) RecordRecall(ctx context.Context, id domain.ID, at time.Time) (domain.Memory, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	current.Version++
	current.AccessCount++
	current.LastAccessedAt = at.UTC()
	current.LastRecalledAt = at.UTC()
	current.UpdatedAt = at.UTC()
	if err := r.saveWithOperation(ctx, current, "touch"); err != nil {
		return domain.Memory{}, err
	}
	return current, nil
}

// Search performs safe FTS5 lexical retrieval over current memory revisions.
// Dormant memories are included only with an explicit deliberate option.
func (r *MemoryRepository) Search(ctx context.Context, query string, options ...MemorySearchOptions) ([]MemorySearchHit, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	ftsQuery, err := safeFTSQuery(query)
	if err != nil {
		return nil, err
	}
	opts := MemorySearchOptions{Limit: 20}
	if len(options) > 0 {
		opts = options[0]
		if opts.Limit == 0 {
			opts.Limit = 20
		}
	}
	if opts.Limit < 0 || opts.Offset < 0 || opts.MaxTokens < 0 {
		return nil, fmt.Errorf("%w: memory search bounds cannot be negative", domain.ErrInvalidArgument)
	}
	where := []string{"memory_fts MATCH ?"}
	args := []any{ftsQuery}
	if opts.IncludeDeleted {
		if opts.IncludeDormant || opts.Deliberate {
			where = append(where, "mv.lifecycle_state IN ('active', 'dormant', 'deleted')")
		} else {
			where = append(where, "mv.lifecycle_state IN ('active', 'deleted')")
		}
	} else if opts.IncludeDormant || opts.Deliberate {
		where = append(where, "mv.lifecycle_state IN ('active', 'dormant')")
	} else {
		where = append(where, "mv.lifecycle_state = 'active'")
	}
	if opts.Kind != "" {
		if !opts.Kind.Valid() {
			return nil, fmt.Errorf("%w: invalid memory kind %q", domain.ErrInvalidArgument, opts.Kind)
		}
		where = append(where, "mv.kind = ?")
		args = append(args, string(opts.Kind))
	}
	if opts.ExcludeHidden && !opts.IncludeHidden {
		where = append(where, "mv.hidden_from_core = 0")
	}
	querySQL := memorySelectPrefix + `,
		snippet(memory_fts, 4, '[', ']', '…', 18) AS hit_snippet,
		bm25(memory_fts) AS hit_rank
		FROM memory_fts
		JOIN memory_heads AS mh ON mh.memory_id = memory_fts.memory_id
			AND mh.version = CAST(memory_fts.memory_version AS INTEGER)
		JOIN memory_versions AS mv ON mv.memory_id = mh.memory_id AND mv.version = mh.version
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY hit_rank ASC, mv.salience DESC, mv.updated_at DESC`
	if opts.Limit > 0 {
		querySQL += " LIMIT ?"
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		if opts.Limit <= 0 {
			querySQL += " LIMIT -1"
		}
		querySQL += " OFFSET ?"
		args = append(args, opts.Offset)
	}
	rows, err := r.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, wrappedSQLError("search memories", err)
	}
	defer rows.Close()
	type hitRow struct {
		memory  domain.Memory
		snippet string
		rank    float64
	}
	rowsResult := make([]hitRow, 0)
	for rows.Next() {
		var item domain.Memory
		var snippet string
		var rank float64
		if err := scanMemoryWithTail(rows, &item, &snippet, &rank); err != nil {
			return nil, wrappedSQLError("scan memory search hit", err)
		}
		rowsResult = append(rowsResult, hitRow{memory: item, snippet: snippet, rank: rank})
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate memory search", err)
	}
	result := make([]MemorySearchHit, 0, len(rowsResult))
	usedTokens := 0
	for _, row := range rowsResult {
		itemTokens := approximateTokens(row.memory.Content) + approximateTokens(row.memory.Summary)
		if opts.MaxTokens > 0 && usedTokens+itemTokens > opts.MaxTokens {
			continue
		}
		sources, err := r.ListSources(ctx, row.memory.ID, row.memory.Version)
		if err != nil {
			return nil, err
		}
		result = append(result, MemorySearchHit{Memory: row.memory, Snippet: row.snippet, Score: -row.rank, Sources: sources})
		usedTokens += itemTokens
	}
	return result, nil
}

// SearchLexical is a descriptive alias for the repository's FTS5 leg.
func (r *MemoryRepository) SearchLexical(ctx context.Context, query string, options ...MemorySearchOptions) ([]MemorySearchHit, error) {
	return r.Search(ctx, query, options...)
}

// SearchArchive is a convenience bridge for callers that keep one memory
// service dependency. The dedicated ArchiveRepository exposes the same data.
func (r *MemoryRepository) SearchArchive(ctx context.Context, query string, options ...ArchiveSearchOptions) ([]ArchiveSearchHit, error) {
	return NewArchiveRepository(r.db).Search(ctx, query, options...)
}

// RebuildProjections reconstructs memory heads, memory FTS and message FTS in
// one transaction. It is safe after an interrupted import or an index loss;
// authoritative transcript and memory journals are left untouched.
func (r *MemoryRepository) RebuildProjections(ctx context.Context) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin rebuild projections", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM memory_heads;
		INSERT INTO memory_heads(memory_id, version, updated_at)
		SELECT mv.memory_id, mv.version, mv.updated_at
		FROM memory_versions AS mv
		JOIN (
			SELECT memory_id, MAX(version) AS version
			FROM memory_versions GROUP BY memory_id
		) AS latest ON latest.memory_id = mv.memory_id AND latest.version = mv.version;
		DELETE FROM memory_fts;
		INSERT INTO memory_fts(memory_id, memory_version, kind, nature, content, summary)
		SELECT memory_id, version, kind, nature, content_text, summary FROM memory_versions;
		DELETE FROM messages_fts;
		INSERT INTO messages_fts(message_id, conversation_id, role, content, created_at)
		SELECT id, conversation_id, role, content, created_at FROM messages;`); err != nil {
		return wrappedSQLError("rebuild search projections", err)
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit rebuild projections", err)
	}
	return nil
}

// RebuildSearchProjections is an architectural alias used by startup repair.
func (r *MemoryRepository) RebuildSearchProjections(ctx context.Context) error {
	return r.RebuildProjections(ctx)
}

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
		string(memory.ID), memory.Version, memory.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return wrappedSQLError("update memory head", err)
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
			memory_id, version, revision_id, operation, parent_version, kind, nature, content_text, content_json, summary,
			confidence, salience, valence, sensitivity, retention_policy, lifecycle_state,
			pinned, hidden_from_core, canonical_key, embedding_version, access_count,
			last_accessed_at, last_recalled_at, created_at, updated_at, dormant_at,
			deleted_at, reason, source_run_id, source_conversation_id, source_message_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(memory.ID), memory.Version, revisionID, operation, parentVersion, string(memory.Kind), string(memory.Nature), memory.Content,
		memory.ContentJSON, memory.Summary, memory.Confidence, memory.Salience, memory.Valence,
		string(memory.Sensitivity), string(memory.Retention), string(memory.Lifecycle), boolInt(memory.Pinned),
		boolInt(memory.HiddenFromCore), memory.CanonicalKey, memory.EmbeddingVersion, memory.AccessCount,
		nullableTimeValue(memory.LastAccessedAt), nullableTimeValue(memory.LastRecalledAt),
		memory.CreatedAt.UTC().Format(time.RFC3339Nano), memory.UpdatedAt.UTC().Format(time.RFC3339Nano),
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
		nullableID(source.MessageID), source.ExcerptHash, source.CreatedAt.UTC().Format(time.RFC3339Nano))
	return wrappedSQLError("insert memory source", err)
}

func normalizeMemoryForCreate(memory domain.Memory) domain.Memory {
	if memory.Version == 0 {
		memory.Version = 1
	}
	if memory.Kind == "" {
		memory.Kind = domain.MemoryKindSemantic
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

func getCurrentMemory(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id domain.ID) (domain.Memory, error) {
	row := queryer.QueryRowContext(ctx, memorySelectPrefix+`
		FROM memory_heads AS mh
		JOIN memory_versions AS mv ON mv.memory_id = mh.memory_id AND mv.version = mh.version
		WHERE mh.memory_id = ?`, string(id))
	item, err := scanMemory(row)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("get memory", err)
	}
	return item, nil
}

func getMemoryVersion(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id domain.ID, version uint64) (domain.Memory, error) {
	row := queryer.QueryRowContext(ctx, memorySelectPrefix+`
		FROM memory_versions AS mv
		WHERE mv.memory_id = ? AND mv.version = ?`, string(id), version)
	item, err := scanMemory(row)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("get memory version", err)
	}
	return item, nil
}

func memoryHeadVersion(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id domain.ID) (uint64, error) {
	if id.Empty() {
		return 0, fmt.Errorf("%w: memory id is required", domain.ErrInvalidArgument)
	}
	var version uint64
	if err := queryer.QueryRowContext(ctx, `SELECT version FROM memory_heads WHERE memory_id = ?`, string(id)).Scan(&version); err != nil {
		return 0, wrappedSQLError("get memory head", err)
	}
	return version, nil
}

const memorySelectPrefix = `SELECT
	mv.memory_id, mv.version, mv.kind, mv.nature, mv.content_text, mv.content_json, mv.summary,
	mv.confidence, mv.salience, mv.valence, mv.sensitivity, mv.retention_policy,
	mv.lifecycle_state, mv.pinned, mv.hidden_from_core, mv.canonical_key, mv.embedding_version,
	mv.access_count, mv.last_accessed_at, mv.last_recalled_at, mv.created_at, mv.updated_at,
	mv.dormant_at, mv.deleted_at, mv.reason, mv.source_run_id, mv.source_conversation_id,
	mv.source_message_id`

type scanner interface {
	Scan(...any) error
}

func scanMemory(row scanner) (domain.Memory, error) {
	var (
		item                                                                  domain.Memory
		idValue, kind, nature, contentJSON, sensitivity, retention, lifecycle string
		content, summary, canonicalKey, embeddingVersion, reason              string
		sourceRunID, sourceConversationID, sourceMessageID                    sql.NullString
		lastAccessedAt, lastRecalledAt, createdAt, updatedAt                  string
		dormantAt, deletedAt                                                  sql.NullString
		pinned, hidden                                                        int
	)
	err := row.Scan(
		&idValue, &item.Version, &kind, &nature, &content, &contentJSON, &summary,
		&item.Confidence, &item.Salience, &item.Valence, &sensitivity, &retention,
		&lifecycle, &pinned, &hidden, &canonicalKey, &embeddingVersion, &item.AccessCount,
		&nullableString{Value: &lastAccessedAt}, &nullableString{Value: &lastRecalledAt},
		&createdAt, &updatedAt, &dormantAt, &deletedAt, &reason, &sourceRunID,
		&sourceConversationID, &sourceMessageID)
	if err != nil {
		return domain.Memory{}, err
	}
	item.ID = domain.ID(idValue)
	item.Kind = domain.MemoryKind(kind)
	item.Nature = domain.MemoryNature(nature)
	item.Content = content
	item.ContentJSON = contentJSON
	item.Summary = summary
	item.Sensitivity = domain.MemorySensitivity(sensitivity)
	item.Retention = domain.MemoryRetention(retention)
	item.Lifecycle = domain.MemoryLifecycle(lifecycle)
	item.Pinned = pinned != 0
	item.HiddenFromCore = hidden != 0
	item.CanonicalKey = canonicalKey
	item.EmbeddingVersion = embeddingVersion
	item.Reason = reason
	if sourceRunID.Valid {
		item.SourceRunID = domain.ID(sourceRunID.String)
	}
	if sourceConversationID.Valid {
		item.SourceConversationID = domain.ID(sourceConversationID.String)
	}
	if sourceMessageID.Valid {
		item.SourceMessageID = domain.ID(sourceMessageID.String)
	}
	var errTime error
	if item.LastAccessedAt, errTime = scanTime(lastAccessedAt); errTime != nil {
		return domain.Memory{}, errTime
	}
	if item.LastRecalledAt, errTime = scanTime(lastRecalledAt); errTime != nil {
		return domain.Memory{}, errTime
	}
	if item.CreatedAt, errTime = scanTime(createdAt); errTime != nil {
		return domain.Memory{}, errTime
	}
	if item.UpdatedAt, errTime = scanTime(updatedAt); errTime != nil {
		return domain.Memory{}, errTime
	}
	if item.DormantAt, errTime = scanNullableTime(dormantAt); errTime != nil {
		return domain.Memory{}, errTime
	}
	if item.DeletedAt, errTime = scanNullableTime(deletedAt); errTime != nil {
		return domain.Memory{}, errTime
	}
	return item, nil
}

func scanMemoryWithTail(row scanner, item *domain.Memory, snippet *string, rank *float64) error {
	var (
		idValue, kind, nature, contentJSON, sensitivity, retention, lifecycle string
		content, summary, canonicalKey, embeddingVersion, reason              string
		sourceRunID, sourceConversationID, sourceMessageID                    sql.NullString
		lastAccessedAt, lastRecalledAt, createdAt, updatedAt                  string
		dormantAt, deletedAt                                                  sql.NullString
		pinned, hidden                                                        int
	)
	if err := row.Scan(
		&idValue, &item.Version, &kind, &nature, &content, &contentJSON, &summary,
		&item.Confidence, &item.Salience, &item.Valence, &sensitivity, &retention,
		&lifecycle, &pinned, &hidden, &canonicalKey, &embeddingVersion, &item.AccessCount,
		&nullableString{Value: &lastAccessedAt}, &nullableString{Value: &lastRecalledAt},
		&createdAt, &updatedAt, &dormantAt, &deletedAt, &reason, &sourceRunID,
		&sourceConversationID, &sourceMessageID, snippet, rank); err != nil {
		return err
	}
	item.ID = domain.ID(idValue)
	item.Kind = domain.MemoryKind(kind)
	item.Nature = domain.MemoryNature(nature)
	item.Content = content
	item.ContentJSON = contentJSON
	item.Summary = summary
	item.Sensitivity = domain.MemorySensitivity(sensitivity)
	item.Retention = domain.MemoryRetention(retention)
	item.Lifecycle = domain.MemoryLifecycle(lifecycle)
	item.Pinned = pinned != 0
	item.HiddenFromCore = hidden != 0
	item.CanonicalKey = canonicalKey
	item.EmbeddingVersion = embeddingVersion
	item.Reason = reason
	if sourceRunID.Valid {
		item.SourceRunID = domain.ID(sourceRunID.String)
	}
	if sourceConversationID.Valid {
		item.SourceConversationID = domain.ID(sourceConversationID.String)
	}
	if sourceMessageID.Valid {
		item.SourceMessageID = domain.ID(sourceMessageID.String)
	}
	var err error
	if item.LastAccessedAt, err = scanTime(lastAccessedAt); err != nil {
		return err
	}
	if item.LastRecalledAt, err = scanTime(lastRecalledAt); err != nil {
		return err
	}
	if item.CreatedAt, err = scanTime(createdAt); err != nil {
		return err
	}
	if item.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return err
	}
	if item.DormantAt, err = scanNullableTime(dormantAt); err != nil {
		return err
	}
	if item.DeletedAt, err = scanNullableTime(deletedAt); err != nil {
		return err
	}
	return nil
}

func listSources(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id domain.ID, version uint64) ([]domain.MemorySource, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT id, memory_id, memory_version, source_type, source_id, run_id,
		       conversation_id, message_id, excerpt_hash, created_at
		FROM memory_sources WHERE memory_id = ? AND memory_version = ?
		ORDER BY created_at ASC, id ASC`, string(id), version)
	if err != nil {
		return nil, wrappedSQLError("list memory sources", err)
	}
	defer rows.Close()
	result := make([]domain.MemorySource, 0)
	for rows.Next() {
		var source domain.MemorySource
		var idValue, memoryID, sourceType, excerptHash, createdAt string
		var sourceID, runID, conversationID, messageID sql.NullString
		if err := rows.Scan(&idValue, &memoryID, &source.MemoryVersion, &sourceType, &sourceID, &runID, &conversationID, &messageID, &excerptHash, &createdAt); err != nil {
			return nil, wrappedSQLError("scan memory source", err)
		}
		source.ID = domain.ID(idValue)
		source.MemoryID = domain.ID(memoryID)
		source.SourceType = sourceType
		source.ExcerptHash = excerptHash
		if sourceID.Valid {
			source.SourceID = domain.ID(sourceID.String)
		}
		if runID.Valid {
			source.RunID = domain.ID(runID.String)
		}
		if conversationID.Valid {
			source.ConversationID = domain.ID(conversationID.String)
		}
		if messageID.Valid {
			source.MessageID = domain.ID(messageID.String)
		}
		if source.CreatedAt, err = scanTime(createdAt); err != nil {
			return nil, err
		}
		result = append(result, source)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate memory sources", err)
	}
	return result, nil
}

func safeFTSQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("%w: search query is required", domain.ErrInvalidArgument)
	}
	words := strings.FieldsFunc(query, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		if strings.TrimSpace(word) == "" {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
	}
	if len(quoted) == 0 {
		return "", fmt.Errorf("%w: search query has no searchable terms", domain.ErrInvalidArgument)
	}
	return strings.Join(quoted, " AND "), nil
}

func approximateTokens(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	return (len([]rune(value)) + 3) / 4
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func isNoRows(err error) bool {
	return err == sql.ErrNoRows
}
