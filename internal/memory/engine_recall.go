package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// Recall performs bounded hybrid memory retrieval. Automatic mode excludes
// dormant and hidden records. Deliberate mode includes dormant records when a
// user explicitly asks to search their past; callers may then restore them by
// passing RestoreDormant=true.
func (e *Engine) Recall(ctx context.Context, query string, options RecallOptions) ([]RecallResult, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if e == nil || e.store == nil {
		return nil, ErrNoStore
	}
	query, err := normalizeQuery(query)
	if err != nil {
		return nil, err
	}
	if options.Mode == "" {
		options.Mode = RecallAutomatic
	}
	if options.Mode != RecallAutomatic && options.Mode != RecallDeliberate {
		return nil, fmt.Errorf("%w: invalid recall mode %q", domain.ErrInvalidArgument, options.Mode)
	}
	now := options.Now
	if now.IsZero() {
		now = e.now()
	}
	budget := options.Budget
	if budget.MaxItems == 0 && budget.MaxTokens == 0 && budget.MaxChars == 0 {
		budget = e.recallBudget
	}
	budget = budget.normalize(options.Limit)
	if options.Limit <= 0 {
		options.Limit = budget.MaxItems
	}
	agentID := firstAgentID(options.AgentID, e.agentID)
	filter := MemoryFilter{AgentID: agentID, IncludeShared: true, States: []LifecycleState{StateActive}, IncludeDormant: options.Mode == RecallDeliberate, IncludeHidden: options.IncludeHidden, Limit: 0}
	if options.Mode == RecallDeliberate {
		filter.States = []LifecycleState{StateActive, StateDormant}
	}
	items, err := e.store.ListMemories(ctx, filter)
	if err != nil {
		return nil, err
	}
	// The query half of both relevance signals is prepared once. Scoring every
	// record with LexicalScore(item.Content, query) re-tokenized the query and
	// the record's immutable content on each of N calls, and
	// affectiveRelevance re-tokenized the query again per record.
	lexical := newLexicalQuery(query)
	affective := affectiveQuery(query)
	// eligibleAt maps an eligible record to its position in items, so the FTS
	// and vector overlays can still reach any record without a full-width
	// RankCandidate (which embeds domain.Memory) being materialized for every
	// record in the store. Only records that end up with a relevance signal
	// become candidates.
	eligibleAt := make(map[domain.ID]int, len(items))
	candidateAt := make(map[domain.ID]int)
	candidates := make([]RankCandidate, 0, maxInt(options.Limit*4, 32))
	// candidateFor returns the index of the candidate for an eligible record,
	// creating it on first use. It deliberately returns an index rather than a
	// pointer: appending to candidates may move the backing array, so a
	// pointer handed out here would not survive the next call.
	candidateFor := func(id domain.ID) (int, bool) {
		if position, ok := candidateAt[id]; ok {
			return position, true
		}
		position, ok := eligibleAt[id]
		if !ok {
			return 0, false
		}
		item := items[position]
		candidates = append(candidates, RankCandidate{Memory: item, AffectiveRelevance: affectiveRelevanceFor(affective, item)})
		candidateAt[id] = len(candidates) - 1
		return len(candidates) - 1, true
	}
	for position, item := range items {
		if !eligible(item, filter) {
			continue
		}
		eligibleAt[item.ID] = position
		if score := lexical.score(item.Content); score > 0 {
			at, _ := candidateFor(item.ID)
			candidates[at].LexicalScore = score
		}
	}
	if e.lexical != nil {
		hits, searchErr := e.lexical.SearchMemoryLexical(ctx, query, filter, maxInt(options.Limit*4, 32))
		if searchErr == nil {
			for _, hit := range hits {
				at, ok := candidateFor(hit.MemoryID)
				if !ok {
					continue
				}
				if hit.Score > candidates[at].LexicalScore {
					candidates[at].LexicalScore = clamp01(hit.Score)
				}
				candidates[at].Evidence.Snippet = hit.Snippet
			}
		} else if errors.Is(searchErr, context.Canceled) || errors.Is(searchErr, context.DeadlineExceeded) {
			return nil, searchErr
		}
	}
	if e.embedder != nil && e.vectors != nil {
		vectors, embedErr := e.embedder.Embed(ctx, []string{query})
		if embedErr == nil && len(vectors) > 0 {
			matches, searchErr := e.vectors.Search(ctx, vectors[0], maxInt(options.Limit*4, 32))
			if searchErr == nil {
				for _, match := range matches {
					at, ok := candidateFor(match.ID)
					if !ok {
						continue
					}
					candidates[at].VectorScore = clamp01(match.Score)
				}
			}
		} else if embedErr != nil && (errors.Is(embedErr, context.Canceled) || errors.Is(embedErr, context.DeadlineExceeded)) {
			return nil, embedErr
		}
	}
	// A query recall must have at least one relevance signal. Recency and
	// salience influence ordering among matches, but must not make unrelated
	// memories appear merely because the profile has stored data. A search
	// overlay can create a candidate carrying only a snippet, so this filter
	// is still required.
	retained := candidates[:0]
	for _, candidate := range candidates {
		if candidate.LexicalScore <= 0 && candidate.VectorScore <= 0 {
			continue
		}
		retained = append(retained, candidate)
	}
	results := e.ranker.Rank(retained, now)
	results, err = e.applyRecallBudget(ctx, results, budget, options.Limit)
	if err != nil {
		return nil, err
	}
	for index := range results {
		result := &results[index]
		sources, sourceErr := e.store.ListMemorySources(ctx, result.Memory.ID)
		if sourceErr != nil {
			return nil, sourceErr
		}
		result.Evidence.Sources = sources
		if options.Mode == RecallDeliberate && result.Dormant && options.RestoreDormant {
			restored, restoreErr := e.restore(ctx, result.Memory, now)
			if restoreErr != nil {
				return nil, restoreErr
			}
			result.Memory = restored
			result.Dormant = false
		}
		if touchErr := e.store.TouchMemory(ctx, result.Memory.ID, now); touchErr != nil {
			return nil, touchErr
		}
	}
	return results, nil
}

func eligible(memory domain.Memory, filter MemoryFilter) bool {
	if !memoryVisibleTo(memory, filter.AgentID, filter.IncludeShared) {
		return false
	}
	if memory.Lifecycle == domain.MemoryLifecycleDeleted {
		return false
	}
	if memory.Sensitivity == domain.MemorySensitivityHighlySensitive {
		return false
	}
	if memory.HiddenFromCore && !filter.IncludeHidden {
		return false
	}
	if memory.Lifecycle == domain.MemoryLifecycleDormant && !filter.IncludeDormant {
		return false
	}
	return true
}

func memoryVisibleTo(item domain.Memory, agentID domain.ID, includeShared bool) bool {
	if item.Scope == "" || item.Scope == domain.MemoryScopeAgentPrivate {
		return agentID.Empty() || item.AgentID == agentID
	}
	return includeShared && item.Scope.Shared()
}

func (e *Engine) applyRecallBudget(ctx context.Context, results []RecallResult, budget Budget, limit int) ([]RecallResult, error) {
	if limit <= 0 {
		limit = budget.MaxItems
	}
	if budget.MaxItems > 0 && limit > budget.MaxItems {
		limit = budget.MaxItems
	}
	if limit <= 0 {
		limit = len(results)
	}
	bounded := make([]RecallResult, 0, minInt(limit, len(results)))
	chars := 0
	for _, result := range results {
		if len(bounded) >= limit {
			break
		}
		content := result.Evidence.Snippet
		if content == "" {
			content = result.Memory.Content
		}
		remaining := budget.MaxChars - chars
		if remaining <= 0 {
			break
		}
		content = truncateUTF8(content, remaining)
		if strings.TrimSpace(content) == "" {
			continue
		}
		result.Evidence.Snippet = content
		chars += utf8.RuneCountInString(content)
		if budget.MaxTokens > 0 && int(math.Ceil(float64(chars)/4)) > budget.MaxTokens {
			break
		}
		bounded = append(bounded, result)
	}
	_ = ctx
	return bounded, nil
}

// SearchArchive is an explicit, on-demand transcript search. It never runs
// as part of ordinary Recall and therefore cannot silently load all sessions
// into a prompt.
func (e *Engine) SearchArchive(ctx context.Context, query string, options ArchiveSearchOptions) ([]ArchiveHit, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if e == nil || e.archive == nil {
		return nil, ErrNoArchiveSearch
	}
	query, err := normalizeQuery(query)
	if err != nil {
		return nil, err
	}
	if options.Limit <= 0 {
		options.Limit = 20
	}
	options.Budget = options.Budget.normalize(options.Limit)
	options.AgentID = firstAgentID(options.AgentID, e.agentID)
	hits, err := e.archive.SearchArchive(ctx, query, options)
	if err != nil {
		return nil, err
	}
	if len(hits) > options.Limit {
		hits = hits[:options.Limit]
	}
	chars := 0
	result := make([]ArchiveHit, 0, len(hits))
	for _, hit := range hits {
		text := hit.Snippet
		if text == "" {
			text = hit.Content
		}
		remaining := options.Budget.MaxChars - chars
		if remaining <= 0 {
			break
		}
		text = truncateUTF8(text, remaining)
		if text == "" {
			continue
		}
		hit.Snippet = text
		chars += utf8.RuneCountInString(text)
		if options.Budget.MaxTokens > 0 && int(math.Ceil(float64(chars)/4)) > options.Budget.MaxTokens {
			break
		}
		result = append(result, hit)
	}
	return result, nil
}
