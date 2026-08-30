package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func getCurrentMemory(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id domain.ID) (domain.Memory, error) {
	row := queryer.QueryRowContext(ctx, memoryHeadSelectPrefix+`
		FROM memory_heads AS mh
		JOIN memory_versions AS mv ON mv.memory_id = mh.memory_id AND mv.version = mh.version`+memoryRecallJoin+`
		WHERE mh.memory_id = ?`, string(id))
	item, err := scanMemory(row)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("get memory", err)
	}
	return item, nil
}

func getCurrentMemoryForAgent(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, agentID, id domain.ID) (domain.Memory, error) {
	if id.Empty() {
		return domain.Memory{}, fmt.Errorf("%w: memory id is required", domain.ErrInvalidArgument)
	}
	row := queryer.QueryRowContext(ctx, memoryHeadSelectPrefix+`
		FROM memory_heads AS mh
		JOIN memory_versions AS mv ON mv.memory_id = mh.memory_id AND mv.version = mh.version`+memoryRecallJoin+`
		WHERE mh.memory_id = ? AND mv.agent_id = ? AND mv.scope = 'agent_private'`, string(id), string(agentID))
	item, err := scanMemory(row)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("get agent memory", err)
	}
	return item, nil
}

func ensureMemoryAgentTx(ctx context.Context, tx *sql.Tx, id, agentID domain.ID) error {
	if agentID.Empty() {
		return fmt.Errorf("%w: memory agent id is required", domain.ErrInvalidArgument)
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM memory_versions WHERE memory_id = ? AND version = 1`, string(id)).Scan(&stored); err != nil {
		return wrappedSQLError("check memory agent", err)
	}
	if stored != string(agentID) {
		return domain.ErrConflict
	}
	return nil
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
	mv.memory_id, mv.agent_id, mv.scope, mv.version, mv.kind, mv.nature, mv.content_text, mv.content_json, mv.summary,
	mv.confidence, mv.salience, mv.valence, mv.sensitivity, mv.retention_policy,
	mv.lifecycle_state, mv.pinned, mv.hidden_from_core, mv.canonical_key, mv.embedding_version,
	mv.access_count, mv.last_accessed_at, mv.last_recalled_at, mv.created_at, mv.updated_at,
	mv.dormant_at, mv.deleted_at, mv.reason, mv.source_run_id, mv.source_conversation_id,
	mv.source_message_id`

// memoryHeadSelectPrefix reads exactly the same columns in exactly the same
// order as memorySelectPrefix so every scanner is shared, but resolves the
// three recall counters against memory_recalls, which is authoritative for
// them. A recall no longer appends a revision, so the head revision's own
// access_count/last_*_at are only a floor recorded at the last content write.
// Queries using this prefix must join memory_heads AS mh and append
// memoryRecallJoin.
const memoryHeadSelectPrefix = `SELECT
	mv.memory_id, mv.agent_id, mv.scope, mv.version, mv.kind, mv.nature, mv.content_text, mv.content_json, mv.summary,
	mv.confidence, mv.salience, mv.valence, mv.sensitivity, mv.retention_policy,
	mv.lifecycle_state, mv.pinned, mv.hidden_from_core, mv.canonical_key, mv.embedding_version,
	MAX(mv.access_count, COALESCE(mr.access_count, 0)) AS access_count,
	COALESCE(mr.last_accessed_at, mv.last_accessed_at) AS last_accessed_at,
	COALESCE(mr.last_recalled_at, mv.last_recalled_at) AS last_recalled_at,
	mv.created_at, mv.updated_at,
	mv.dormant_at, mv.deleted_at, mv.reason, mv.source_run_id, mv.source_conversation_id,
	mv.source_message_id`

// memoryRecallJoin attaches the bounded recall counter to a head projection
// query. It is a LEFT JOIN because a memory that was never recalled has no row.
const memoryRecallJoin = `
		LEFT JOIN memory_recalls AS mr ON mr.memory_id = mh.memory_id`

type scanner interface {
	Scan(...any) error
}

// memoryRowBuffer holds the raw column targets for the fixed memory column
// list so that every memory scanner shares one definition of the row shape.
// Queries that append trailing columns append their own targets after
// targets(); the fixed prefix must stay in the order of memorySelectPrefix.
type memoryRowBuffer struct {
	item                                                                                  domain.Memory
	idValue, agentID, scope, kind, nature, contentJSON, sensitivity, retention, lifecycle string
	content, summary, canonicalKey, embeddingVersion, reason                              string
	sourceRunID, sourceConversationID, sourceMessageID                                    sql.NullString
	lastAccessedAt, lastRecalledAt, createdAt, updatedAt                                  string
	dormantAt, deletedAt                                                                  sql.NullString
	pinned, hidden                                                                        int
}

func (buffer *memoryRowBuffer) targets() []any {
	return []any{
		&buffer.idValue, &buffer.agentID, &buffer.scope, &buffer.item.Version, &buffer.kind,
		&buffer.nature, &buffer.content, &buffer.contentJSON, &buffer.summary,
		&buffer.item.Confidence, &buffer.item.Salience, &buffer.item.Valence, &buffer.sensitivity,
		&buffer.retention, &buffer.lifecycle, &buffer.pinned, &buffer.hidden, &buffer.canonicalKey,
		&buffer.embeddingVersion, &buffer.item.AccessCount,
		&nullableString{Value: &buffer.lastAccessedAt}, &nullableString{Value: &buffer.lastRecalledAt},
		&buffer.createdAt, &buffer.updatedAt, &buffer.dormantAt, &buffer.deletedAt, &buffer.reason,
		&buffer.sourceRunID, &buffer.sourceConversationID, &buffer.sourceMessageID,
	}
}

func (buffer *memoryRowBuffer) memory() (domain.Memory, error) {
	item := buffer.item
	item.ID = domain.ID(buffer.idValue)
	item.AgentID = domain.ID(buffer.agentID)
	item.Scope = domain.MemoryScope(buffer.scope)
	item.Kind = domain.MemoryKind(buffer.kind)
	item.Nature = domain.MemoryNature(buffer.nature)
	item.Content = buffer.content
	item.ContentJSON = buffer.contentJSON
	item.Summary = buffer.summary
	item.Sensitivity = domain.MemorySensitivity(buffer.sensitivity)
	item.Retention = domain.MemoryRetention(buffer.retention)
	item.Lifecycle = domain.MemoryLifecycle(buffer.lifecycle)
	item.Pinned = buffer.pinned != 0
	item.HiddenFromCore = buffer.hidden != 0
	item.CanonicalKey = buffer.canonicalKey
	item.EmbeddingVersion = buffer.embeddingVersion
	item.Reason = buffer.reason
	if buffer.sourceRunID.Valid {
		item.SourceRunID = domain.ID(buffer.sourceRunID.String)
	}
	if buffer.sourceConversationID.Valid {
		item.SourceConversationID = domain.ID(buffer.sourceConversationID.String)
	}
	if buffer.sourceMessageID.Valid {
		item.SourceMessageID = domain.ID(buffer.sourceMessageID.String)
	}
	var err error
	if item.LastAccessedAt, err = scanTime(buffer.lastAccessedAt); err != nil {
		return domain.Memory{}, err
	}
	if item.LastRecalledAt, err = scanTime(buffer.lastRecalledAt); err != nil {
		return domain.Memory{}, err
	}
	if item.CreatedAt, err = scanTime(buffer.createdAt); err != nil {
		return domain.Memory{}, err
	}
	if item.UpdatedAt, err = scanTime(buffer.updatedAt); err != nil {
		return domain.Memory{}, err
	}
	if item.DormantAt, err = scanNullableTime(buffer.dormantAt); err != nil {
		return domain.Memory{}, err
	}
	if item.DeletedAt, err = scanNullableTime(buffer.deletedAt); err != nil {
		return domain.Memory{}, err
	}
	return item, nil
}

func scanMemory(row scanner) (domain.Memory, error) {
	var buffer memoryRowBuffer
	if err := row.Scan(buffer.targets()...); err != nil {
		return domain.Memory{}, err
	}
	return buffer.memory()
}

func scanMemoryWithTail(row scanner, item *domain.Memory, snippet *string, rank *float64) error {
	var buffer memoryRowBuffer
	if err := row.Scan(append(buffer.targets(), snippet, rank)...); err != nil {
		return err
	}
	scanned, err := buffer.memory()
	if err != nil {
		return err
	}
	*item = scanned
	return nil
}

// scanMemoryVersionRecord reads a journal revision together with its revision
// metadata from one row. The memory snapshot carries the counters as they were
// recorded at that revision, which is what an audit or rollback view wants;
// live recall counters belong to the head projection, not to history.
func scanMemoryVersionRecord(row scanner) (MemoryVersionRecord, error) {
	var buffer memoryRowBuffer
	var revisionID, operation string
	var parentVersion uint64
	if err := row.Scan(append(buffer.targets(), &revisionID, &operation, &parentVersion)...); err != nil {
		return MemoryVersionRecord{}, err
	}
	item, err := buffer.memory()
	if err != nil {
		return MemoryVersionRecord{}, err
	}
	return MemoryVersionRecord{
		Memory:        item,
		RevisionID:    domain.ID(revisionID),
		Operation:     operation,
		ParentVersion: parentVersion,
		Reason:        item.Reason,
	}, nil
}

// memoryVersionSelect reads full journal revisions with their metadata in one
// query. Reading ids first and then issuing one QueryRowContext per revision
// turned a version listing into N+1 round-trips over a single serialized
// connection, and every one of those rows carries the full revision content.
const memoryVersionSelect = memorySelectPrefix + `,
	mv.revision_id, mv.operation, mv.parent_version
	FROM memory_versions AS mv`

// memorySourceSelect reads the full provenance row. One definition serves the
// single-revision read and the set read below so both scan the same shape.
const memorySourceSelect = `
		SELECT id, memory_id, memory_version, source_type, source_id, run_id,
		       conversation_id, message_id, excerpt_hash, created_at
		FROM memory_sources`

// memorySourceOrder is the stable provenance order. It is a total order, so a
// set read can sort once globally and still hand every revision its links in
// exactly the order a single-revision read would have produced.
const memorySourceOrder = `
		ORDER BY created_at ASC, id ASC`

func scanMemorySource(row scanner) (domain.MemorySource, error) {
	var source domain.MemorySource
	var idValue, memoryID, sourceType, excerptHash, createdAt string
	var sourceID, runID, conversationID, messageID sql.NullString
	if err := row.Scan(&idValue, &memoryID, &source.MemoryVersion, &sourceType, &sourceID, &runID, &conversationID, &messageID, &excerptHash, &createdAt); err != nil {
		return domain.MemorySource{}, wrappedSQLError("scan memory source", err)
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
	var err error
	if source.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.MemorySource{}, err
	}
	return source, nil
}

func listSources(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id domain.ID, version uint64) ([]domain.MemorySource, error) {
	rows, err := queryer.QueryContext(ctx, memorySourceSelect+`
		WHERE memory_id = ? AND memory_version = ?`+memorySourceOrder, string(id), version)
	if err != nil {
		return nil, wrappedSQLError("list memory sources", err)
	}
	defer rows.Close()
	result := make([]domain.MemorySource, 0)
	for rows.Next() {
		source, err := scanMemorySource(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, source)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate memory sources", err)
	}
	return result, nil
}

// memoryRevisionKey identifies one memory revision. Provenance is keyed by
// (memory_id, memory_version), so a set read has to carry both halves: an id
// on its own would silently mix revisions if a caller ever holds two of them.
type memoryRevisionKey struct {
	id      domain.ID
	version uint64
}

// maxSourceLookupPairs bounds how many revisions one provenance read binds.
// Each revision costs two bound parameters, so a chunk stays far inside
// SQLite's variable limit; a caller holding more revisions than this pays one
// extra round-trip per chunk rather than one per revision.
const maxSourceLookupPairs = 400

// listSourcesForRevisions reads the provenance of many revisions in one
// round-trip per chunk, keyed by revision. Calling listSources once per item
// is an N+1 read, and the pool is deliberately a single connection (see
// listing.go), so every extra round-trip serializes against all writers.
//
// Every requested revision gets an entry, empty and non-nil when it has no
// links, so a caller sees exactly what listSources returns for one revision.
func listSourcesForRevisions(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, keys []memoryRevisionKey) (map[memoryRevisionKey][]domain.MemorySource, error) {
	sources := make(map[memoryRevisionKey][]domain.MemorySource, len(keys))
	pending := make([]memoryRevisionKey, 0, len(keys))
	for _, key := range keys {
		if _, seen := sources[key]; seen {
			continue
		}
		sources[key] = make([]domain.MemorySource, 0)
		pending = append(pending, key)
	}
	for start := 0; start < len(pending); start += maxSourceLookupPairs {
		end := min(start+maxSourceLookupPairs, len(pending))
		if err := readSourceChunk(ctx, queryer, pending[start:end], sources); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

func readSourceChunk(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, keys []memoryRevisionKey, into map[memoryRevisionKey][]domain.MemorySource) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	revisions := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)*2)
	for _, key := range keys {
		revisions = append(revisions, "(memory_id = ? AND memory_version = ?)")
		args = append(args, string(key.id), key.version)
	}
	rows, err := queryer.QueryContext(ctx, memorySourceSelect+`
		WHERE `+strings.Join(revisions, " OR ")+memorySourceOrder, args...)
	if err != nil {
		return wrappedSQLError("list memory sources", err)
	}
	defer rows.Close()
	for rows.Next() {
		source, err := scanMemorySource(rows)
		if err != nil {
			return err
		}
		key := memoryRevisionKey{id: source.MemoryID, version: source.MemoryVersion}
		into[key] = append(into[key], source)
	}
	if err := rows.Err(); err != nil {
		return wrappedSQLError("iterate memory sources", err)
	}
	return nil
}
