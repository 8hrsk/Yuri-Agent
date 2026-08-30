package memory

import (
	"fmt"

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
