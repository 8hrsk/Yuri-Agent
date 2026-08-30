package sqlite

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

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
	args := make([]any, 0, 5)
	if !opts.AgentID.Empty() {
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
	} else if !opts.IncludeDeleted {
		if opts.IncludeDormant {
			where = append(where, "mv.lifecycle_state IN ('active', 'dormant')")
		} else {
			where = append(where, "mv.lifecycle_state = 'active'")
		}
	} else if !opts.IncludeDormant {
		where = append(where, "mv.lifecycle_state IN ('active', 'deleted')")
	}
	if opts.ExcludeSensitivity != "" {
		if !opts.ExcludeSensitivity.Valid() {
			return nil, fmt.Errorf("%w: invalid memory sensitivity %q", domain.ErrInvalidArgument, opts.ExcludeSensitivity)
		}
		where = append(where, "mv.sensitivity <> ?")
		args = append(args, string(opts.ExcludeSensitivity))
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
	query := memoryHeadSelectPrefix + `
		FROM memory_heads AS mh
		JOIN memory_versions AS mv ON mv.memory_id = mh.memory_id AND mv.version = mh.version` + memoryRecallJoin + `
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

// coreScanHeadroom bounds how far past MaxItems the core prefix reads when a
// token budget is in play. Every other core predicate is enforced in SQL, so
// the only reason to over-fetch is that an item skipped for exceeding the
// remaining token budget may still be followed by a smaller one that fits.
const coreScanHeadroom = 8

// coreScanLimit translates the caller's item budget into a SQL LIMIT. With no
// token budget the SQL predicate is exact and the limit is the item budget
// itself; with one, the limit is the item budget plus headroom for items the
// budget skips. Zero means unbounded, which only an unbounded MaxItems asks for.
func coreScanLimit(opts CoreMemoryOptions) int {
	if opts.MaxItems <= 0 {
		return 0
	}
	if opts.MaxTokens <= 0 {
		return opts.MaxItems
	}
	if opts.MaxItems > math.MaxInt/coreScanHeadroom {
		return 0
	}
	return opts.MaxItems * coreScanHeadroom
}

// ListCore selects the bounded active core prefix. Dormant, deleted,
// HiddenFromCore and highly sensitive records are excluded even when they are
// pinned. Pinned records sort first so explicit user curation survives normal
// salience decay.
//
// Every one of those predicates is applied in SQL together with a LIMIT
// derived from the caller's budget: the core prefix is reassembled before each
// new context, and loading the whole active corpus into the heap to keep the
// first handful of rows made that cost grow with the size of the corpus.
func (r *MemoryRepository) ListCore(ctx context.Context, options ...CoreMemoryOptions) ([]domain.Memory, error) {
	opts := CoreMemoryOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.MaxItems < 0 || opts.MaxTokens < 0 {
		return nil, fmt.Errorf("%w: core limits cannot be negative", domain.ErrInvalidArgument)
	}
	items, err := r.List(ctx, MemoryListOptions{
		AgentID:            opts.AgentID,
		Scope:              opts.Scope,
		Lifecycle:          domain.MemoryLifecycleActive,
		ExcludeSensitivity: domain.MemorySensitivityHighlySensitive,
		ExcludeHidden:      true,
		Limit:              coreScanLimit(opts),
	})
	if err != nil {
		return nil, err
	}
	result := make([]domain.Memory, 0, len(items))
	usedTokens := 0
	for _, item := range items {
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

func (r *MemoryRepository) ListSourcesForAgent(ctx context.Context, agentID, id domain.ID, version ...uint64) ([]domain.MemorySource, error) {
	if agentID.Empty() {
		return nil, fmt.Errorf("%w: memory agent id is required", domain.ErrInvalidArgument)
	}
	item, err := r.GetForAgent(ctx, agentID, id)
	if err != nil {
		return nil, err
	}
	if len(version) > 0 && version[0] > 0 {
		if version[0] > item.Version {
			return nil, domain.ErrNotFound
		}
		return listSources(ctx, r.db, id, version[0])
	}
	return listSources(ctx, r.db, id, item.Version)
}
