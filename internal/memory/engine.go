package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

var ErrNoExtractor = fmt.Errorf("memory: extractor is not configured")

func firstAgentID(values ...domain.ID) domain.ID {
	for _, value := range values {
		if !value.Empty() {
			return value
		}
	}
	return ""
}

type Config struct {
	AgentID      domain.ID
	Store        Store
	Extractor    Extractor
	Archive      ArchiveSearcher
	Lexical      LexicalSearcher
	Vectors      VectorIndex
	Embedder     Embedder
	Consolidator Consolidator
	Ranker       HybridRanker
	Now          Clock
	IDs          IDGenerator
	DecayPolicy  func(domain.Memory) DecayPolicy
	CoreBudget   Budget
	RecallBudget Budget
}

// Engine owns autonomous memory policy. It never asks an approval handler:
// memory writes are internal, versioned, reversible changes. External side
// effects remain outside this package and still require Yuri policy checks.
type Engine struct {
	agentID      domain.ID
	store        Store
	extractor    Extractor
	archive      ArchiveSearcher
	lexical      LexicalSearcher
	vectors      VectorIndex
	embedder     Embedder
	consolidator Consolidator
	ranker       HybridRanker
	now          Clock
	ids          IDGenerator
	decayPolicy  func(domain.Memory) DecayPolicy
	coreBudget   Budget
	recallBudget Budget
}

func NewEngine(config Config) (*Engine, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("%w: %v", ErrNoStore, domain.ErrInvalidArgument)
	}
	if config.Now == nil {
		config.Now = defaultNow
	}
	if config.IDs == nil {
		config.IDs = domain.RandomIDGenerator{}
	}
	if config.Consolidator == nil {
		config.Consolidator = ConservativeConsolidator{}
	}
	if config.DecayPolicy == nil {
		config.DecayPolicy = func(m domain.Memory) DecayPolicy { return DefaultDecayPolicy(m.Kind) }
	}
	if config.CoreBudget.MaxItems == 0 {
		config.CoreBudget = Budget{MaxItems: 16, MaxTokens: 1800}
	}
	if config.RecallBudget.MaxItems == 0 {
		config.RecallBudget = Budget{MaxItems: 8, MaxTokens: 1800}
	}
	if config.Lexical == nil {
		if searcher, ok := config.Store.(LexicalSearcher); ok {
			config.Lexical = searcher
		}
	}
	if config.Archive == nil {
		if searcher, ok := config.Store.(ArchiveSearcher); ok {
			config.Archive = searcher
		}
	}
	if config.Ranker.Now == nil {
		config.Ranker.Now = config.Now
	}
	return &Engine{
		agentID: config.AgentID, store: config.Store, extractor: config.Extractor, archive: config.Archive,
		lexical: config.Lexical, vectors: config.Vectors, embedder: config.Embedder,
		consolidator: config.Consolidator, ranker: config.Ranker,
		now: config.Now, ids: config.IDs, decayPolicy: config.DecayPolicy,
		coreBudget: config.CoreBudget, recallBudget: config.RecallBudget,
	}, nil
}

// WriteResult describes one autonomous internal mutation. Changed is false
// when a duplicate candidate was safely ignored; the source may still be
// attached by a store implementation.
type WriteResult struct {
	Memory    domain.Memory
	Operation MemoryOperation
	Changed   bool
	Created   bool
	Reason    string
}

// ProcessTurn lets Yuri independently choose what, if anything, to remember.
// Extractor output is persisted without an approval step, after validation,
// deduplication, sensitivity checks, and versioned consolidation.
func (e *Engine) ProcessTurn(ctx context.Context, turn Turn) ([]WriteResult, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if e == nil || e.store == nil {
		return nil, ErrNoStore
	}
	if e.extractor == nil {
		return nil, ErrNoExtractor
	}
	if err := turn.Valid(); err != nil {
		return nil, err
	}
	candidates, err := e.extractor.Extract(ctx, turn)
	if err != nil {
		return nil, fmt.Errorf("memory extract: %w", err)
	}
	results := make([]WriteResult, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Memory.AgentID.Empty() {
			candidate.Memory.AgentID = firstAgentID(turn.AgentID, e.agentID)
		}
		if candidate.Memory.Scope == "" {
			candidate.Memory.Scope = domain.MemoryScopeAgentPrivate
		}
		if err := contextErr(ctx); err != nil {
			return results, err
		}
		if candidate.Operation == CandidateNoop {
			continue
		}
		candidate.Sources = e.completeTurnSources(candidate, turn)
		result, err := e.applyCandidate(ctx, candidate, turn.Now)
		if err != nil {
			if candidate.Operation == CandidateNoop {
				continue
			}
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// Remember applies one explicitly supplied candidate through the same
// autonomous pipeline. It is useful for a foreground remember tool and for
// tests; it still never bypasses Store versioning or validation.
func (e *Engine) Remember(ctx context.Context, candidate Candidate) (WriteResult, error) {
	if err := contextErr(ctx); err != nil {
		return WriteResult{}, err
	}
	if e == nil || e.store == nil {
		return WriteResult{}, ErrNoStore
	}
	return e.applyCandidate(ctx, candidate, e.now())
}

func (e *Engine) applyCandidate(ctx context.Context, candidate Candidate, now time.Time) (WriteResult, error) {
	if err := contextErr(ctx); err != nil {
		return WriteResult{}, err
	}
	if now.IsZero() {
		now = e.now()
	}
	if candidate.Memory.AgentID.Empty() {
		candidate.Memory.AgentID = e.agentID
	}
	if candidate.Memory.Scope == "" {
		candidate.Memory.Scope = domain.MemoryScopeAgentPrivate
	}
	if !e.agentID.Empty() && candidate.Memory.AgentID != e.agentID {
		return WriteResult{}, domain.ErrConflict
	}
	if candidate.Operation == "" {
		candidate.Operation = CandidateAuto
	}
	if candidate.Operation != CandidateAuto && candidate.Operation != CandidateCreate &&
		candidate.Operation != CandidateUpdate && candidate.Operation != CandidateForget {
		return WriteResult{}, fmt.Errorf("%w: unsupported candidate operation %q", ErrCandidateRejected, candidate.Operation)
	}

	if candidate.Operation == CandidateForget {
		return e.forgetCandidate(ctx, candidate, now)
	}

	if candidate.Memory.ID.Empty() {
		if !candidate.MatchID.Empty() {
			candidate.Memory.ID = candidate.MatchID
		} else {
			id, idErr := e.ensureID("", "memory")
			if idErr != nil {
				return WriteResult{}, idErr
			}
			candidate.Memory.ID = id
		}
	}
	memory, err := Normalize(candidate.Memory, now)
	if err != nil {
		return WriteResult{}, fmt.Errorf("%w: %v", ErrCandidateRejected, err)
	}
	if memory.Sensitivity == domain.MemorySensitivityHighlySensitive && candidate.Operation == CandidateAuto {
		// Preserve a useful record without silently putting highly sensitive
		// material in the normal prompt. Deliberate UI search can opt in via
		// IncludeHidden, and the user can later curate it into the core.
		memory.HiddenFromCore = true
	}
	if candidate.DedupKey != "" {
		memory.CanonicalKey = strings.TrimSpace(candidate.DedupKey)
	}
	if memory.CanonicalKey == "" {
		memory.CanonicalKey = canonicalKey(memory)
	}

	existing, found, err := e.resolveExisting(ctx, candidate, memory)
	if err != nil {
		return WriteResult{}, err
	}
	if !found {
		if candidate.Operation == CandidateUpdate {
			return WriteResult{}, fmt.Errorf("%w: update target not found", domain.ErrNotFound)
		}
		memory.ID, err = e.ensureID(memory.ID, "memory")
		if err != nil {
			return WriteResult{}, err
		}
		memory.Version = 1
		memory.CreatedAt = nonZeroTime(memory.CreatedAt, now)
		memory.UpdatedAt = now.UTC()
		return e.commit(ctx, memory, nil, candidate.Sources, OperationCreate, candidate.Reason, now, true)
	}

	consolidation, err := e.consolidator.Consolidate(ctx, existing, candidate)
	if err != nil {
		return WriteResult{}, fmt.Errorf("memory consolidate: %w", err)
	}
	if consolidation.Noop {
		// A duplicate still gets a chance to carry new evidence. A source-only
		// update is a real append-only revision; without it a fact rediscovered
		// in a later session would silently lose that provenance.
		if len(candidate.Sources) == 0 {
			return WriteResult{Memory: existing, Operation: OperationTouch, Changed: false, Created: false, Reason: consolidation.Reason}, nil
		}
		touched := existing
		touched.Version = existing.Version + 1
		touched.UpdatedAt = now.UTC()
		return e.commit(ctx, touched, &existing, candidate.Sources, OperationTouch, consolidation.Reason, now, false)
	}
	merged := mergeMemory(existing, consolidation.Memory, now)
	if merged.ID.Empty() {
		merged.ID = existing.ID
	}
	merged.Version = existing.Version + 1
	merged.CreatedAt = existing.CreatedAt
	merged.UpdatedAt = now.UTC()
	if merged.CanonicalKey == "" {
		merged.CanonicalKey = existing.CanonicalKey
	}
	if merged.Lifecycle == domain.MemoryLifecycleDeleted {
		merged.DeletedAt = now.UTC()
	} else {
		merged.DeletedAt = time.Time{}
	}
	return e.commit(ctx, merged, &existing, candidate.Sources, consolidation.Operation, firstNonEmpty(consolidation.Reason, candidate.Reason), now, false)
}

func (e *Engine) resolveExisting(ctx context.Context, candidate Candidate, memory domain.Memory) (domain.Memory, bool, error) {
	if candidate.Operation == CandidateUpdate && !memory.ID.Empty() {
		existing, err := e.store.GetMemory(ctx, memory.ID)
		if err != nil {
			return domain.Memory{}, false, err
		}
		return existing, true, nil
	}
	if !candidate.MatchID.Empty() {
		existing, err := e.store.GetMemory(ctx, candidate.MatchID)
		if err != nil {
			if candidate.Operation == CandidateCreate && errorsIsNotFound(err) {
				return domain.Memory{}, false, nil
			}
			return domain.Memory{}, false, err
		}
		return existing, true, nil
	}
	items, err := e.store.ListMemories(ctx, MemoryFilter{AgentID: e.agentID, IncludeDormant: true, Limit: 0})
	if err != nil {
		return domain.Memory{}, false, err
	}
	key := strings.TrimSpace(memory.CanonicalKey)
	for _, item := range items {
		if !e.agentID.Empty() && item.AgentID != e.agentID {
			continue
		}
		if item.Lifecycle == domain.MemoryLifecycleDeleted || item.HiddenFromCore && candidate.Operation == CandidateCreate {
			continue
		}
		if key != "" && item.CanonicalKey == key && item.Kind == memory.Kind {
			return item, true, nil
		}
		if item.Kind == memory.Kind && item.Nature == memory.Nature && sameContent(item.Content, memory.Content) {
			return item, true, nil
		}
	}
	return domain.Memory{}, false, nil
}

func (e *Engine) forgetCandidate(ctx context.Context, candidate Candidate, now time.Time) (WriteResult, error) {
	id := candidate.MatchID
	if id.Empty() {
		id = candidate.Memory.ID
	}
	if id.Empty() {
		return WriteResult{}, fmt.Errorf("%w: forget target is required", domain.ErrInvalidArgument)
	}
	existing, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return WriteResult{}, err
	}
	if existing.Lifecycle == domain.MemoryLifecycleDeleted {
		return WriteResult{Memory: existing, Operation: OperationTouch, Changed: false}, nil
	}
	previous := existing
	existing.Version++
	existing.Lifecycle = domain.MemoryLifecycleDeleted
	existing.DeletedAt = now.UTC()
	existing.UpdatedAt = now.UTC()
	return e.commit(ctx, existing, &previous, candidate.Sources, OperationForget, candidate.Reason, now, false)
}

func (e *Engine) commit(ctx context.Context, memory domain.Memory, previous *domain.Memory, sources []domain.MemorySource, operation MemoryOperation, reason string, now time.Time, created bool) (WriteResult, error) {
	if operation == "" {
		operation = OperationUpdate
	}
	if memory.ID.Empty() {
		return WriteResult{}, fmt.Errorf("%w: memory id is required", domain.ErrInvalidArgument)
	}
	memory, err := Normalize(memory, now)
	if err != nil {
		return WriteResult{}, err
	}
	if previous != nil && memory.Version <= previous.Version {
		memory.Version = previous.Version + 1
	}
	revisionID, err := e.ids.NewID("memory_version")
	if err != nil {
		return WriteResult{}, fmt.Errorf("create memory revision id: %w", err)
	}
	parentVersion := uint64(0)
	if previous != nil {
		parentVersion = previous.Version
	}
	revision := &MemoryRevision{ID: revisionID, MemoryID: memory.ID, Operation: operation, Snapshot: memory, ParentVersion: parentVersion, Reason: reason, CreatedAt: now.UTC()}
	if err := revision.Valid(); err != nil {
		return WriteResult{}, err
	}
	sources = e.normalizeSources(memory, sources, now)
	for _, source := range sources {
		if err := source.Validate(); err != nil {
			return WriteResult{}, err
		}
	}
	change := MemoryChange{Memory: memory, Revision: revision, Sources: sources}
	if err := change.Validate(); err != nil {
		return WriteResult{}, err
	}
	if err := e.store.ApplyMemoryChange(ctx, change); err != nil {
		return WriteResult{}, err
	}
	// The vector projection is rebuildable, so a provider/index failure must
	// not roll back a durable memory write. IndexMemory exposes that failure to
	// maintenance jobs when they need an explicit health signal.
	if memory.Lifecycle == domain.MemoryLifecycleDeleted {
		if e.vectors != nil {
			_ = e.vectors.Delete(context.Background(), memory.ID)
		}
	} else {
		_ = e.indexMemory(context.Background(), memory)
	}
	return WriteResult{Memory: memory, Operation: operation, Changed: true, Created: created, Reason: reason}, nil
}

func (e *Engine) ensureID(id domain.ID, prefix string) (domain.ID, error) {
	if !id.Empty() {
		return id, nil
	}
	return e.ids.NewID(prefix)
}

func (e *Engine) completeTurnSources(candidate Candidate, turn Turn) []domain.MemorySource {
	if len(candidate.Sources) > 0 {
		return candidate.Sources
	}
	result := make([]domain.MemorySource, 0, len(turn.Messages))
	for _, message := range turn.Messages {
		result = append(result, domain.MemorySource{
			SourceType: "message", SourceID: message.ID, RunID: turn.RunID,
			ConversationID: message.ConversationID, MessageID: message.ID,
			ExcerptHash: hashExcerpt(message.Content), CreatedAt: turn.Now.UTC(),
		})
	}
	if len(result) == 0 {
		result = append(result, domain.MemorySource{SourceType: "turn", SourceID: turn.RunID, ConversationID: turn.ConversationID, RunID: turn.RunID, CreatedAt: turn.Now.UTC()})
	}
	return result
}

func (e *Engine) normalizeSources(memory domain.Memory, sources []domain.MemorySource, now time.Time) []domain.MemorySource {
	result := make([]domain.MemorySource, 0, len(sources))
	for _, source := range sources {
		source.MemoryID = memory.ID
		source.MemoryVersion = memory.Version
		if source.ID.Empty() {
			if id, err := e.ids.NewID("memory_source"); err == nil {
				source.ID = id
			} else {
				continue
			}
		}
		if source.SourceType == "" {
			source.SourceType = "manual"
		}
		if source.CreatedAt.IsZero() {
			source.CreatedAt = now.UTC()
		}
		result = append(result, source)
	}
	return result
}

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
	filter := MemoryFilter{AgentID: agentID, States: []LifecycleState{StateActive}, IncludeDormant: options.Mode == RecallDeliberate, IncludeHidden: options.IncludeHidden, Limit: 0}
	if options.Mode == RecallDeliberate {
		filter.States = []LifecycleState{StateActive, StateDormant}
	}
	items, err := e.store.ListMemories(ctx, filter)
	if err != nil {
		return nil, err
	}
	byID := make(map[domain.ID]RankCandidate, len(items))
	for _, item := range items {
		if !eligible(item, filter) {
			continue
		}
		byID[item.ID] = RankCandidate{Memory: item, LexicalScore: LexicalScore(item.Content, query), AffectiveRelevance: affectiveRelevance(query, item)}
	}
	if e.lexical != nil {
		hits, searchErr := e.lexical.SearchMemoryLexical(ctx, query, filter, maxInt(options.Limit*4, 32))
		if searchErr == nil {
			for _, hit := range hits {
				candidate, ok := byID[hit.MemoryID]
				if !ok {
					continue
				}
				if hit.Score > candidate.LexicalScore {
					candidate.LexicalScore = clamp01(hit.Score)
				}
				candidate.Evidence.Snippet = hit.Snippet
				byID[hit.MemoryID] = candidate
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
					candidate, ok := byID[match.ID]
					if !ok {
						continue
					}
					candidate.VectorScore = clamp01(match.Score)
					byID[match.ID] = candidate
				}
			}
		} else if embedErr != nil && (errors.Is(embedErr, context.Canceled) || errors.Is(embedErr, context.DeadlineExceeded)) {
			return nil, embedErr
		}
	}
	// A query recall must have at least one relevance signal. Recency and
	// salience influence ordering among matches, but must not make unrelated
	// memories appear merely because the profile has stored data.
	for id, candidate := range byID {
		if candidate.LexicalScore <= 0 && candidate.VectorScore <= 0 {
			delete(byID, id)
		}
	}
	candidates := make([]RankCandidate, 0, len(byID))
	for _, candidate := range byID {
		candidates = append(candidates, candidate)
	}
	results := e.ranker.Rank(candidates, now)
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
	if !filter.AgentID.Empty() && memory.AgentID != filter.AgentID {
		return false
	}
	if memory.Scope != "" && memory.Scope != domain.MemoryScopeAgentPrivate {
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

// ApplyDecay moves low-salience records to dormant. The operation is
// reversible through deliberate Recall or an explicit RestoreMemory call.
func (e *Engine) ApplyDecay(ctx context.Context, now time.Time) ([]WriteResult, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if e == nil || e.store == nil {
		return nil, ErrNoStore
	}
	if now.IsZero() {
		now = e.now()
	}
	items, err := e.store.ListMemories(ctx, MemoryFilter{AgentID: e.agentID, Limit: 0})
	if err != nil {
		return nil, err
	}
	results := make([]WriteResult, 0)
	for _, item := range items {
		if (!e.agentID.Empty() && item.AgentID != e.agentID) || item.Lifecycle != domain.MemoryLifecycleActive || item.Pinned || item.HiddenFromCore || item.Retention == domain.MemoryRetentionPermanent {
			continue
		}
		policy := e.decayPolicy(item).normalize(item.Kind)
		if policy.NeverDormant {
			continue
		}
		score := EffectiveSalience(item, now, policy)
		anchor := activityTime(item)
		tooOld := policy.DormantAfter > 0 && now.Sub(anchor) >= policy.DormantAfter
		if score >= policy.DormantThreshold && !tooOld {
			continue
		}
		previous := item
		item.Lifecycle = domain.MemoryLifecycleDormant
		item.DormantAt = now.UTC()
		item.UpdatedAt = now.UTC()
		item.Version++
		result, commitErr := e.commit(ctx, item, &previous, nil, OperationDormant, "natural decay", now, false)
		if commitErr != nil {
			return results, commitErr
		}
		results = append(results, result)
	}
	return results, nil
}

// RestoreMemory explicitly reactivates a dormant record. Deliberate Recall
// calls this same path when configured to restore a hit.
func (e *Engine) RestoreMemory(ctx context.Context, id domain.ID, reason string) (domain.Memory, error) {
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	memory, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	return e.restoreWithReason(ctx, memory, e.now(), reason)
}

// ForgetMemory creates a reversible tombstone for a derived memory. It never
// deletes transcript messages or their provenance.
func (e *Engine) ForgetMemory(ctx context.Context, id domain.ID, reason string) (domain.Memory, error) {
	result, err := e.Remember(ctx, Candidate{Operation: CandidateForget, MatchID: id, Reason: reason})
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}

// EditMemory applies a user- or application-requested content edit as a new
// version while preserving the original transcript and provenance.
func (e *Engine) EditMemory(ctx context.Context, id domain.ID, content, reason string) (domain.Memory, error) {
	if strings.TrimSpace(content) == "" {
		return domain.Memory{}, fmt.Errorf("%w: memory content is required", domain.ErrInvalidArgument)
	}
	existing, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	existing.Content = strings.TrimSpace(content)
	result, err := e.Remember(ctx, Candidate{
		Operation: CandidateUpdate,
		MatchID:   id,
		Memory:    existing,
		Reason:    reason,
	})
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}

// HideMemory controls inclusion in the stable core snapshot while keeping a
// record available to deliberate search and the memory UI.
func (e *Engine) HideMemory(ctx context.Context, id domain.ID, hidden bool, reason string) (domain.Memory, error) {
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	memory, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	previous := memory
	memory.Version++
	memory.HiddenFromCore = hidden
	memory.UpdatedAt = e.now().UTC()
	result, err := e.commit(ctx, memory, &previous, nil, OperationHide, reason, memory.UpdatedAt, false)
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}

// PinMemory marks a memory as explicitly curated for the core snapshot.
func (e *Engine) PinMemory(ctx context.Context, id domain.ID, pinned bool, reason string) (domain.Memory, error) {
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	memory, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	previous := memory
	memory.Version++
	memory.Pinned = pinned
	memory.UpdatedAt = e.now().UTC()
	result, err := e.commit(ctx, memory, &previous, nil, OperationUpdate, reason, memory.UpdatedAt, false)
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}

func (e *Engine) restore(ctx context.Context, memory domain.Memory, now time.Time) (domain.Memory, error) {
	return e.restoreWithReason(ctx, memory, now, "deliberate recall")
}

func (e *Engine) restoreWithReason(ctx context.Context, memory domain.Memory, now time.Time, reason string) (domain.Memory, error) {
	if memory.Lifecycle != domain.MemoryLifecycleDormant {
		return memory, nil
	}
	previous := memory
	memory.Lifecycle = domain.MemoryLifecycleActive
	memory.DormantAt = time.Time{}
	memory.DeletedAt = time.Time{}
	memory.UpdatedAt = now.UTC()
	memory.Version++
	result, err := e.commit(ctx, memory, &previous, nil, OperationRestore, reason, now, false)
	if err != nil {
		return domain.Memory{}, err
	}
	return result.Memory, nil
}

// IndexMemory updates only the rebuildable semantic projection. Durable
// memory state is unaffected if no embedder/vector index is configured.
func (e *Engine) IndexMemory(ctx context.Context, memory domain.Memory) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return e.indexMemory(ctx, memory)
}

// RebuildVectorIndex recreates the derived semantic projection from current
// authoritative records. Deleted records are omitted; active, dormant and
// hidden memories remain searchable when policy permits them.
func (e *Engine) RebuildVectorIndex(ctx context.Context) (int, error) {
	if err := contextErr(ctx); err != nil {
		return 0, err
	}
	if e == nil || e.store == nil {
		return 0, ErrNoStore
	}
	if e.embedder == nil || e.vectors == nil {
		return 0, fmt.Errorf("%w: vector index and embedder are required", ErrNoEmbedder)
	}
	items, err := e.store.ListMemories(ctx, MemoryFilter{AgentID: e.agentID, IncludeDormant: true, IncludeHidden: true, Limit: 0})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, item := range items {
		if (!e.agentID.Empty() && item.AgentID != e.agentID) || item.Lifecycle == domain.MemoryLifecycleDeleted {
			continue
		}
		if err := e.indexMemory(ctx, item); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (e *Engine) indexMemory(ctx context.Context, memory domain.Memory) error {
	if e == nil || e.embedder == nil || e.vectors == nil {
		return nil
	}
	vectors, err := e.embedder.Embed(ctx, []string{memory.Content})
	if err != nil {
		return err
	}
	if len(vectors) == 0 {
		return fmt.Errorf("%w: embedder returned no vector", ErrNoEmbedder)
	}
	return e.vectors.Upsert(ctx, VectorDocument{ID: memory.ID, Vector: vectors[0], Version: e.embedder.Version()})
}

// CoreSnapshot selects a stable, bounded prefix for a new run. It includes
// only active, non-hidden high-signal records; the transcript remains
// on-demand. The returned Text is safe to append below higher-priority policy
// and identity instructions because records are explicitly marked as data.
func (e *Engine) CoreSnapshot(ctx context.Context, options Budget) (ContextSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return ContextSnapshot{}, err
	}
	if e == nil || e.store == nil {
		return ContextSnapshot{}, ErrNoStore
	}
	now := e.now()
	if options.MaxItems == 0 && options.MaxChars == 0 && options.MaxTokens == 0 {
		options = e.coreBudget
	}
	options = options.normalize(16)
	items, err := e.store.ListMemories(ctx, MemoryFilter{AgentID: e.agentID, States: []LifecycleState{StateActive}, Kinds: []Kind{KindCore, KindUserProfile, KindSemantic, KindProcedural, KindRelationship}, IncludeHidden: false, Limit: 0})
	if err != nil {
		return ContextSnapshot{}, err
	}
	candidates := make([]RankCandidate, 0, len(items))
	for _, item := range items {
		if (!e.agentID.Empty() && item.AgentID != e.agentID) || item.Lifecycle != domain.MemoryLifecycleActive || item.HiddenFromCore || item.Lifecycle == domain.MemoryLifecycleDeleted ||
			item.Sensitivity == domain.MemorySensitivityHighlySensitive {
			continue
		}
		// Pinned memories receive a deterministic salience floor for snapshot
		// selection without changing their stored value.
		copy := item
		if copy.Pinned && copy.Salience < 0.99 {
			copy.Salience = 0.99
		}
		candidates = append(candidates, RankCandidate{Memory: copy, AffectiveRelevance: math.Abs(copy.Valence)})
	}
	ranked := e.ranker.Rank(candidates, now)
	for index := range ranked {
		sources, sourceErr := e.store.ListMemorySources(ctx, ranked[index].Memory.ID)
		if sourceErr != nil {
			return ContextSnapshot{}, sourceErr
		}
		ranked[index].Evidence.Sources = sources
	}
	entries := make([]ContextEntry, 0, len(ranked))
	chars := 0
	for _, result := range ranked {
		if len(entries) >= options.MaxItems {
			break
		}
		content := result.Memory.Content
		remaining := options.MaxChars - chars
		if remaining <= 0 {
			break
		}
		content = truncateUTF8(content, remaining)
		if strings.TrimSpace(content) == "" {
			continue
		}
		result.Memory.Content = content
		entries = append(entries, ContextEntry{Memory: result.Memory, Score: result.Score, Evidence: result.Evidence})
		chars += utf8.RuneCountInString(content)
		if options.MaxTokens > 0 && int(math.Ceil(float64(chars)/4)) >= options.MaxTokens {
			break
		}
	}
	snapshot := ContextSnapshot{CreatedAt: now.UTC(), Entries: entries, Chars: chars, Tokens: int(math.Ceil(float64(chars) / 4))}
	snapshot.Text = FormatContext(entries)
	return snapshot, nil
}

// FormatContext renders only a bounded data section. Provenance is included
// so the model can distinguish a recalled fact from the current conversation.
func FormatContext(entries []ContextEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<yuri-memory-context provenance=\"bounded-core-snapshot\">\n")
	for _, entry := range entries {
		builder.WriteString("  <memory id=\"")
		builder.WriteString(escapeContext(entry.Memory.ID.String()))
		builder.WriteString("\" kind=\"")
		builder.WriteString(escapeContext(string(entry.Memory.Kind)))
		builder.WriteString("\" nature=\"")
		builder.WriteString(escapeContext(string(entry.Memory.Nature)))
		builder.WriteString("\" source=\"")
		builder.WriteString(escapeContext(sourceLabel(entry.Evidence.Sources)))
		builder.WriteString("\">\n    ")
		builder.WriteString(escapeContext(entry.Memory.Content))
		builder.WriteString("\n  </memory>\n")
	}
	builder.WriteString("</yuri-memory-context>")
	return builder.String()
}

func sourceLabel(sources []domain.MemorySource) string {
	if len(sources) == 0 {
		return "unknown"
	}
	parts := make([]string, 0, minInt(len(sources), 3))
	for _, source := range sources {
		label := source.SourceID.String()
		if label == "" {
			label = source.SourceType
		}
		parts = append(parts, label)
		if len(parts) == 3 {
			break
		}
	}
	return strings.Join(parts, ",")
}

func escapeContext(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	return value
}

// EffectiveSalience is exported for decay previews and tests.
func EffectiveSalience(memory domain.Memory, now time.Time, policy DecayPolicy) float64 {
	policy = policy.normalize(memory.Kind)
	anchor := activityTime(memory)
	if now.Before(anchor) || policy.HalfLife <= 0 {
		return clamp01(memory.Salience * memory.Confidence)
	}
	age := now.Sub(anchor)
	decay := math.Exp(-age.Hours() / policy.HalfLife.Hours() * math.Ln2)
	accessBoost := math.Min(0.25, math.Log1p(float64(memory.AccessCount))*0.03)
	return clamp01(memory.Salience*memory.Confidence*decay + accessBoost)
}

func activityTime(memory domain.Memory) time.Time {
	anchor := memory.UpdatedAt
	for _, candidate := range []time.Time{memory.LastAccessedAt, memory.LastRecalledAt, memory.CreatedAt} {
		if candidate.After(anchor) {
			anchor = candidate
		}
	}
	return anchor
}

func affectiveRelevance(query string, memory domain.Memory) float64 {
	if memory.Nature == domain.MemoryNatureEmotion || memory.Kind == domain.MemoryKindRelationship {
		return math.Abs(memory.Valence)
	}
	for _, token := range tokenize(query) {
		switch token {
		case "чувств", "эмоц", "отнош", "feel", "emotion", "mood", "relationship":
			return math.Abs(memory.Valence)
		}
	}
	return 0
}

func canonicalKey(memory domain.Memory) string {
	return string(memory.Kind) + ":" + string(memory.Nature) + ":" + strings.Join(tokenize(memory.Content), " ")
}

func sameContent(left, right string) bool {
	return canonicalText(left) == canonicalText(right)
}

func canonicalText(value string) string { return strings.Join(tokenize(value), " ") }

func hashExcerpt(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func mergeMemory(existing, candidate domain.Memory, now time.Time) domain.Memory {
	merged := existing
	if strings.TrimSpace(candidate.Content) != "" {
		merged.Content = strings.TrimSpace(candidate.Content)
	}
	if candidate.ContentJSON != "" {
		merged.ContentJSON = candidate.ContentJSON
	}
	if candidate.Summary != "" {
		merged.Summary = candidate.Summary
	}
	if candidate.Kind.Valid() {
		merged.Kind = candidate.Kind
	}
	if candidate.Nature.Valid() {
		merged.Nature = candidate.Nature
	}
	if candidate.Sensitivity.Valid() {
		merged.Sensitivity = candidate.Sensitivity
	}
	if candidate.Retention.Valid() {
		merged.Retention = candidate.Retention
	}
	if candidate.Confidence > merged.Confidence {
		merged.Confidence = candidate.Confidence
	}
	if candidate.Salience > merged.Salience {
		merged.Salience = candidate.Salience
	}
	if candidate.Valence != 0 {
		merged.Valence = candidate.Valence
	}
	if candidate.CanonicalKey != "" {
		merged.CanonicalKey = candidate.CanonicalKey
	}
	if candidate.Pinned {
		merged.Pinned = true
	}
	if candidate.HiddenFromCore {
		merged.HiddenFromCore = true
	}
	if candidate.Lifecycle == domain.MemoryLifecycleDormant {
		merged.Lifecycle = candidate.Lifecycle
		merged.DormantAt = candidate.DormantAt
	}
	merged.UpdatedAt = now.UTC()
	return merged
}

// ConservativeConsolidator is safe to use before a model-assisted conflict
// resolver exists. Equal facts are merged; a higher-confidence replacement is
// accepted, while a lower-confidence conflicting claim is ignored.
type ConservativeConsolidator struct{}

func (ConservativeConsolidator) Consolidate(_ context.Context, existing domain.Memory, candidate Candidate) (Consolidation, error) {
	if candidate.Operation == CandidateForget {
		return Consolidation{Operation: OperationForget, Memory: existing}, nil
	}
	if sameContent(existing.Content, candidate.Memory.Content) && candidate.Memory.Content != "" {
		// Preserve the canonical human-facing text for duplicates; punctuation
		// and casing differences should not create needless revisions.
		merged := existing
		if candidate.Memory.Confidence > merged.Confidence {
			merged.Confidence = candidate.Memory.Confidence
		}
		if candidate.Memory.Salience > merged.Salience {
			merged.Salience = candidate.Memory.Salience
		}
		if candidate.Memory.ContentJSON != "" {
			merged.ContentJSON = candidate.Memory.ContentJSON
		}
		if merged.Confidence == existing.Confidence && merged.Salience == existing.Salience && merged.ContentJSON == existing.ContentJSON {
			return Consolidation{Operation: OperationTouch, Memory: existing, Noop: true, Reason: "duplicate fact"}, nil
		}
		return Consolidation{Operation: OperationMerge, Memory: merged, Reason: "duplicate evidence consolidated"}, nil
	}
	if candidate.Operation == CandidateUpdate || candidate.MatchID != "" {
		return Consolidation{Operation: OperationUpdate, Memory: candidate.Memory, Reason: "explicit memory update"}, nil
	}
	if candidate.Memory.Confidence > existing.Confidence {
		return Consolidation{Operation: OperationMerge, Memory: candidate.Memory, Reason: "higher-confidence evidence replaced conflicting claim"}, nil
	}
	return Consolidation{Operation: OperationTouch, Memory: existing, Noop: true, Reason: "lower-confidence conflicting claim ignored"}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nonZeroTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback.UTC()
	}
	return value.UTC()
}

func truncateUTF8(value string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= maxChars {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxChars])
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func errorsIsNotFound(err error) bool {
	return err == domain.ErrNotFound || strings.Contains(err.Error(), domain.ErrNotFound.Error())
}

// Keep the sorting helper local so archive adapters can return arbitrary
// order without affecting deterministic memory snapshot selection.
func sortMemories(items []domain.Memory) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Salience != items[j].Salience {
			return items[i].Salience > items[j].Salience
		}
		return items[i].ID.String() < items[j].ID.String()
	})
}
