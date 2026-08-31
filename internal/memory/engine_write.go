package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

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
		if candidate.Interpretation != nil {
			candidate, err = e.prepareFictionInterpretation(ctx, candidate, turn)
			if err != nil {
				return results, err
			}
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

func (e *Engine) prepareFictionInterpretation(ctx context.Context, candidate Candidate, turn Turn) (Candidate, error) {
	reference := candidate.Interpretation
	if reference == nil || reference.SourceMemoryID.Empty() {
		return Candidate{}, fmt.Errorf("%w: fictional interpretation source is required", ErrCandidateRejected)
	}
	status := strings.TrimSpace(reference.Status)
	if status != FictionProvenanceInterpreted && status != FictionProvenanceUncertain {
		return Candidate{}, fmt.Errorf("%w: invalid fictional interpretation status", ErrCandidateRejected)
	}
	allowed := false
	for _, recalled := range turn.RecalledMemories {
		if recalled.ID == reference.SourceMemoryID && recalled.Nature == domain.MemoryNatureFiction {
			allowed = true
			break
		}
	}
	if !allowed {
		return Candidate{}, fmt.Errorf("%w: interpretation source was not recalled for this turn", ErrCandidateRejected)
	}
	source, err := e.store.GetMemory(ctx, reference.SourceMemoryID)
	if err != nil {
		return Candidate{}, err
	}
	if source.AgentID != firstAgentID(turn.AgentID, e.agentID) || source.Nature != domain.MemoryNatureFiction ||
		source.Lifecycle == domain.MemoryLifecycleDeleted {
		return Candidate{}, fmt.Errorf("%w: invalid fictional interpretation source", ErrCandidateRejected)
	}
	rootEpisodeID := ""
	if ownerPayload, parseErr := ParseBackstoryMemoryPayload(source.ContentJSON); parseErr == nil && ownerPayload.AgentID == source.AgentID {
		rootEpisodeID = ownerPayload.EpisodeID
	} else if derivedPayload, derivedErr := ParseBackstoryInterpretationPayload(source.ContentJSON); derivedErr == nil && derivedPayload.AgentID == source.AgentID {
		rootEpisodeID = derivedPayload.RootEpisodeID
	} else {
		return Candidate{}, fmt.Errorf("%w: untrusted fictional memory provenance", ErrCandidateRejected)
	}
	digest := sha256.Sum256([]byte(source.Content))
	payload := BackstoryInterpretationPayload{
		SchemaVersion: BackstoryMemorySchemaVersion, EpistemicStatus: BackstoryEpistemicFictional,
		Provenance: status, OwnerAuthored: false, AgentID: source.AgentID,
		SourceMemoryID: source.ID, SourceVersion: source.Version,
		SourceDigest: "sha256:" + hex.EncodeToString(digest[:]), RootEpisodeID: rootEpisodeID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Candidate{}, fmt.Errorf("encode backstory interpretation metadata: %w", err)
	}
	candidate.Memory.AgentID = source.AgentID
	candidate.Memory.Scope = domain.MemoryScopeAgentPrivate
	candidate.Memory.Kind = domain.MemoryKindEpisodic
	candidate.Memory.Nature = domain.MemoryNatureFiction
	candidate.Memory.ContentJSON = string(encoded)
	candidate.Memory.Sensitivity = domain.MemorySensitivityPrivate
	candidate.Memory.Retention = domain.MemoryRetentionDecay
	if status == FictionProvenanceUncertain && (candidate.Memory.Confidence <= 0 || candidate.Memory.Confidence > .6) {
		candidate.Memory.Confidence = .6
	}
	candidate.DedupKey = "fiction-interpretation:" + source.ID.String() + ":" + canonicalText(candidate.Memory.Content)
	candidate.Sources = append(candidate.Sources, domain.MemorySource{
		SourceType: BackstorySourceInterpretation, SourceID: source.ID, ExcerptHash: hashExcerpt(source.Content), CreatedAt: turn.Now,
	})
	return candidate, nil
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
	// The candidate's canonical form is invariant across the scan. Computing
	// it inside the loop meant tokenizing the candidate's content once per
	// stored record, and sameContent then tokenized the record's content and
	// joined it into a throwaway string only to compare and discard it.
	// canonicalTextEquals streams the record instead and stops at the first
	// rune that cannot match.
	canonical := canonicalText(memory.Content)
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
		if item.Kind == memory.Kind && item.Nature == memory.Nature && canonicalTextEquals(item.Content, canonical) {
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
