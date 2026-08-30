package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

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
	if !opts.VisibleToAgentID.Empty() {
		where = append(where, "((mv.agent_id = ? AND mv.scope = 'agent_private') OR mv.scope IN ('owner_shared', 'installation_shared'))")
		args = append(args, string(opts.VisibleToAgentID))
	} else if !opts.AgentID.Empty() {
		where = append(where, "mv.agent_id = ?")
		args = append(args, string(opts.AgentID))
	}
	if opts.Scope != "" {
		if !opts.Scope.Valid() {
			return nil, fmt.Errorf("%w: invalid memory scope %q", domain.ErrInvalidArgument, opts.Scope)
		}
		where = append(where, "mv.scope = ?")
		args = append(args, string(opts.Scope))
	}
	if opts.Lifecycle != "" {
		if !opts.Lifecycle.Valid() {
			return nil, fmt.Errorf("%w: invalid memory lifecycle %q", domain.ErrInvalidArgument, opts.Lifecycle)
		}
		where = append(where, "mv.lifecycle_state = ?")
		args = append(args, string(opts.Lifecycle))
	} else if opts.IncludeDeleted {
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
	querySQL := memoryHeadSelectPrefix + `,
		snippet(memory_fts, 4, '[', ']', '…', 18) AS hit_snippet,
		bm25(memory_fts) AS hit_rank
		FROM memory_fts
		JOIN memory_heads AS mh ON mh.memory_id = memory_fts.memory_id
			AND mh.version = CAST(memory_fts.memory_version AS INTEGER)
		JOIN memory_versions AS mv ON mv.memory_id = mh.memory_id AND mv.version = mh.version` + memoryRecallJoin + `
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
	// The token budget decides the final hit set here, in Go and in order, so
	// provenance can only be read once that set is known. Resolving it inside
	// this loop cost one round-trip per surviving hit on a pool that is
	// deliberately a single connection; the affordable hits are collected
	// first and their sources are read as one set.
	selected := make([]hitRow, 0, len(rowsResult))
	usedTokens := 0
	for _, row := range rowsResult {
		itemTokens := approximateTokens(row.memory.Content) + approximateTokens(row.memory.Summary)
		if opts.MaxTokens > 0 && usedTokens+itemTokens > opts.MaxTokens {
			continue
		}
		selected = append(selected, row)
		usedTokens += itemTokens
	}
	revisions := make([]memoryRevisionKey, 0, len(selected))
	for _, row := range selected {
		revisions = append(revisions, memoryRevisionKey{id: row.memory.ID, version: row.memory.Version})
	}
	sources, err := listSourcesForRevisions(ctx, r.db, revisions)
	if err != nil {
		return nil, err
	}
	result := make([]MemorySearchHit, 0, len(selected))
	for _, row := range selected {
		result = append(result, MemorySearchHit{
			Memory: row.memory, Snippet: row.snippet, Score: -row.rank,
			Sources: sources[memoryRevisionKey{id: row.memory.ID, version: row.memory.Version}],
		})
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
// authoritative transcript and memory journals are left untouched, and so is
// memory_recalls, which is access telemetry rather than a projection and
// cannot be derived from the journal.
//
// Rebuilding also repairs a database that accumulated one memory_fts row per
// historical revision under the previous scheme: only head revisions are
// indexed, so the projection converges on one row per live memory.
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
		SELECT mv.memory_id, mv.version, mv.kind, mv.nature, mv.content_text, mv.summary
		FROM memory_versions AS mv
		JOIN memory_heads AS mh ON mh.memory_id = mv.memory_id AND mh.version = mv.version;
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
