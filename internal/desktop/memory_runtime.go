package desktop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	contextbuilder "github.com/OrdoAI/yuri-agent/internal/context"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const memoryEventName = "yuri:memory"

func firstNonEmptyID(values ...domain.ID) domain.ID {
	for _, value := range values {
		if !value.Empty() {
			return value
		}
	}
	return ""
}

func (b *Bridge) newMemoryEngine(backend agent.ModelBackend, model string, agentID domain.ID) (*memory.Engine, error) {
	adapter := sqliteMemoryAdapter{repositories: b.repositories, agentID: agentID}
	return memory.NewEngine(memory.Config{
		AgentID: agentID,
		Store:   adapter, Extractor: modelMemoryExtractor{backend: backend, model: model},
		Archive: adapter, Lexical: adapter, Vectors: memory.NewBruteForceIndex(),
		Ranker: memory.HybridRanker{Weights: memory.RankWeights{
			Lexical: .45, Recency: .2, Salience: .3, Affective: .05,
		}},
		CoreBudget:   memory.Budget{MaxItems: 24, MaxChars: 6_000},
		RecallBudget: memory.Budget{MaxItems: 12, MaxChars: 6_000},
	})
}

type desktopContextSource struct {
	engine       *memory.Engine
	repositories *storage.Repositories
	agentID      domain.ID
}

func (source desktopContextSource) Core(ctx context.Context, limit int) ([]contextbuilder.MemoryItem, error) {
	snapshot, err := source.engine.CoreSnapshot(ctx, memory.Budget{MaxItems: limit, MaxChars: 6_000})
	if err != nil {
		return nil, err
	}
	result := make([]contextbuilder.MemoryItem, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		result = append(result, contextbuilder.MemoryItem{
			ID: entry.Memory.ID, Kind: string(entry.Memory.Kind), Content: entry.Memory.Content,
			Provenance: memoryProvenance(entry.Evidence.Sources), Score: entry.Score,
		})
	}
	return result, nil
}

func (source desktopContextSource) Recall(ctx context.Context, query string, limit int) ([]contextbuilder.MemoryItem, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	results, err := source.engine.Recall(ctx, query, memory.RecallOptions{
		AgentID: source.agentID, Mode: memory.RecallAutomatic, Limit: limit, Budget: memory.Budget{MaxItems: limit, MaxChars: 3_000},
	})
	if err != nil {
		return nil, err
	}
	items := make([]contextbuilder.MemoryItem, 0, len(results))
	for _, result := range results {
		content := result.Evidence.Snippet
		if strings.TrimSpace(content) == "" {
			content = result.Memory.Content
		}
		items = append(items, contextbuilder.MemoryItem{
			ID: result.Memory.ID, Kind: string(result.Memory.Kind), Content: content,
			Provenance: memoryProvenance(result.Evidence.Sources), Score: result.Score,
		})
	}
	return items, nil
}

func (source desktopContextSource) SearchArchive(ctx context.Context, query contextbuilder.ArchiveQuery) ([]contextbuilder.ArchiveHit, error) {
	if strings.TrimSpace(query.Text) == "" {
		return nil, nil
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 12
	}
	// Ask for a bounded superset because the current conversation is filtered
	// below and must not crowd out actual cross-session hits.
	hits, err := source.repositories.Archive.Search(ctx, query.Text, storage.ArchiveSearchOptions{
		AgentID: firstNonEmptyID(query.AgentID, source.agentID), Limit: limit * 3, MaxTokens: 3_000,
	})
	if err != nil {
		return nil, err
	}
	result := make([]contextbuilder.ArchiveHit, 0, limit)
	for _, hit := range hits {
		if hit.ConversationID == query.ExcludeConversationID {
			continue
		}
		result = append(result, contextbuilder.ArchiveHit{
			ConversationID: hit.ConversationID, MessageID: hit.Message.ID,
			Conversation: hit.ConversationTitle, Excerpt: firstNonEmpty(hit.Snippet, truncateRunes(hit.Message.Content, 600)),
			OccurredAt: hit.Message.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"), Score: boundedScore(hit.Score),
		})
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func memoryProvenance(sources []domain.MemorySource) string {
	if len(sources) == 0 {
		return "derived memory"
	}
	source := sources[0]
	switch {
	case !source.MessageID.Empty():
		return fmt.Sprintf("message:%s", source.MessageID)
	case !source.ConversationID.Empty():
		return fmt.Sprintf("conversation:%s", source.ConversationID)
	case !source.SourceID.Empty():
		return fmt.Sprintf("%s:%s", source.SourceType, source.SourceID)
	default:
		return source.SourceType
	}
}

func (b *Bridge) emitMemoryUpdated(writes int) {
	if b == nil || writes <= 0 {
		return
	}
	b.mu.RLock()
	appContext := b.appCtx
	b.mu.RUnlock()
	if appContext != nil {
		wailsruntime.EventsEmit(appContext, memoryEventName, map[string]any{"type": "memory.updated", "writes": writes})
	}
}

var _ contextbuilder.Source = desktopContextSource{}

func (b *Bridge) reviewTurnInBackground(engine *memory.Engine, backend agent.ModelBackend, model string, allowReflection bool, turn memory.Turn, agentID domain.ID) {
	if engine == nil || turn.ConversationID.Empty() {
		return
	}
	if agentID.Empty() {
		agentID = turn.AgentID
	}
	if agentID.Empty() {
		return
	}
	// The captured owner is authoritative for the whole background pass. Do
	// not let a stale or caller-supplied Turn.AgentID redirect memory writes
	// after the active profile has changed.
	if turn.AgentID != agentID {
		turn.AgentID = agentID
	}
	b.mu.Lock()
	if b.shuttingDown {
		b.mu.Unlock()
		return
	}
	backgroundCtx := b.backgroundCtx
	if backgroundCtx == nil {
		backgroundCtx = context.Background()
	}
	b.background.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.background.Done()
		// The chat run this pass belongs to has already completed and emitted
		// its terminal event, and both the decay pass and the reflection engine
		// persist through optimistic saves that simply do not land when this
		// goroutine dies. There is no durable object left in a non-terminal
		// state to fail, so the recovery reports through the log only.
		defer b.recoverBridgeGoroutine("memory_review", nil)
		ctx, cancel := context.WithTimeout(backgroundCtx, 3*time.Minute)
		defer cancel()
		decayed, decayErr := engine.ApplyDecay(ctx, turn.Now)
		if decayErr != nil && b.logger != nil && ctx.Err() == nil {
			b.logger.WarnContext(ctx, "memory decay pass failed", "run_id", turn.RunID, "error", safeError(decayErr.Error()))
		}
		results, err := engine.ProcessTurn(ctx, turn)
		if err != nil {
			if b.logger != nil && ctx.Err() == nil {
				b.logger.WarnContext(ctx, "post-turn memory review failed", "run_id", turn.RunID, "error", safeError(err.Error()))
			}
			results = nil
		}
		writes := len(decayed) + len(results)
		if b.logger != nil && writes > 0 {
			b.logger.InfoContext(ctx, "post-turn memory review completed", "run_id", turn.RunID, "writes", writes)
		}
		if writes > 0 {
			b.emitMemoryUpdated(writes)
		}
		if allowReflection {
			b.reflectOnTurn(ctx, backend, model, turn, agentID)
			if _, reconcileErr := b.reconcileCompletedPeerSocialReflections(ctx, backend, model, 10); reconcileErr != nil && b.logger != nil && ctx.Err() == nil {
				b.logger.WarnContext(ctx, "reconcile peer social reflection", "run_id", turn.RunID, "error", safeError(reconcileErr.Error()))
			}
		}
	}()
}
