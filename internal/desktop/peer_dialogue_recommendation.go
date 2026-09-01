package desktop

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/executionbudget"
)

type RecommendPeerDialogueBudgetInput struct {
	PeerAgentID string `json:"peerAgentId"`
	Purpose     string `json:"purpose"`
}

type PeerDialogueBudgetRecommendationView struct {
	Recommended PeerDialogueBudgetView `json:"recommended"`
	Ceiling     PeerDialogueBudgetView `json:"ceiling"`
	Basis       string                 `json:"basis"`
	SampleCount int                    `json:"sampleCount"`
	Confidence  string                 `json:"confidence"`
	Rationale   string                 `json:"rationale"`
}

type PeerDialogueBudgetView struct {
	MinTurns           int   `json:"minTurns"`
	MaxTurns           int   `json:"maxTurns"`
	MaxTokens          int64 `json:"maxTokens"`
	MaxDurationSeconds int   `json:"maxDurationSeconds"`
}

// RecommendPeerDialogueBudget is a read-only preview. It uses only durable,
// participant-visible aggregate metrics and never starts a run, calls a model,
// or changes the owner's current draft. The returned recommendation remains a
// hint until the owner explicitly applies and submits it.
func (b *Bridge) RecommendPeerDialogueBudget(input RecommendPeerDialogueBudgetInput) (PeerDialogueBudgetRecommendationView, error) {
	input.PeerAgentID = strings.TrimSpace(input.PeerAgentID)
	input.Purpose = strings.TrimSpace(input.Purpose)
	if input.PeerAgentID == "" || input.Purpose == "" {
		return PeerDialogueBudgetRecommendationView{}, fmt.Errorf("%w: peer and purpose are required", domain.ErrInvalidArgument)
	}

	ctx, cancel := b.context()
	defer cancel()
	initiatorID := b.personaProfileID()
	initiator, err := b.repositories.Agents.Get(ctx, initiatorID)
	if err != nil {
		return PeerDialogueBudgetRecommendationView{}, err
	}
	peer, err := (peerDialogueAgentTool{bridge: b}).resolvePeerAgent(ctx, input.PeerAgentID)
	if err != nil {
		return PeerDialogueBudgetRecommendationView{}, err
	}
	if peer.ID == initiatorID {
		return PeerDialogueBudgetRecommendationView{}, fmt.Errorf("%w: an agent cannot talk to itself", domain.ErrInvalidArgument)
	}
	backend, model, err := b.chatBackendForAgent(ctx, initiatorID)
	if err != nil {
		return PeerDialogueBudgetRecommendationView{}, err
	}
	recommendation, ceiling, err := b.resolvePeerDialogueBudgetRecommendation(ctx, initiator, peer, backend, model, input.Purpose)
	if err != nil {
		return PeerDialogueBudgetRecommendationView{}, err
	}
	return PeerDialogueBudgetRecommendationView{
		Recommended: peerBudgetView(recommendation.Budget), Ceiling: peerBudgetView(ceiling),
		Basis: string(recommendation.Basis), SampleCount: recommendation.SampleCount,
		Confidence: recommendationConfidence(recommendation.SampleCount),
		Rationale:  recommendationRationale(recommendation.Basis, recommendation.SampleCount),
	}, nil
}

func (b *Bridge) resolvePeerDialogueBudgetRecommendation(ctx context.Context, initiator, peer domain.AgentProfile, backend agent.ModelBackend, model, purpose string) (executionbudget.PeerRecommendation, domain.PeerDialogueBudget, error) {
	ceiling := executionbudget.ResolvePeer(initiator.ExecutionBudget, modelExecutionLimits(backend, model))
	dialogues, err := b.repositories.PeerDialogues.ListByParticipant(ctx, initiator.ID, 50)
	if err != nil {
		return executionbudget.PeerRecommendation{}, domain.PeerDialogueBudget{}, err
	}
	pairKey := domain.AgentPairKey(initiator.ID, peer.ID)
	history := make([]executionbudget.PeerHistorySample, 0, 8)
	for _, dialogue := range dialogues {
		if dialogue.PairKey != pairKey || dialogue.Status != domain.PeerDialogueCompleted {
			continue
		}
		duration := 0
		if !dialogue.StartedAt.IsZero() && !dialogue.FinishedAt.IsZero() && dialogue.FinishedAt.After(dialogue.StartedAt) {
			duration = int(math.Ceil(dialogue.FinishedAt.Sub(dialogue.StartedAt).Seconds()))
		}
		history = append(history, executionbudget.PeerHistorySample{
			Purpose: dialogue.Purpose, Turns: dialogue.TurnCount, Tokens: dialogue.TokensUsed, DurationSeconds: duration,
			HitHardLimit: dialogue.CompletionReason == domain.PeerDialogueCompletionMaxTurns || dialogue.CompletionReason == domain.PeerDialogueCompletionMaxTokens,
		})
		if len(history) == 8 {
			break
		}
	}
	recommendation, err := executionbudget.RecommendPeer(ceiling, purpose, history)
	if err != nil {
		return executionbudget.PeerRecommendation{}, domain.PeerDialogueBudget{}, err
	}
	return recommendation, ceiling, nil
}

func peerBudgetView(budget domain.PeerDialogueBudget) PeerDialogueBudgetView {
	return PeerDialogueBudgetView{
		MinTurns: budget.MinTurns, MaxTurns: budget.MaxTurns, MaxTokens: budget.MaxTokens, MaxDurationSeconds: budget.MaxDurationSeconds,
	}
}

func recommendationConfidence(samples int) string {
	if samples >= 3 {
		return "high"
	}
	if samples > 0 {
		return "medium"
	}
	return "low"
}

func recommendationRationale(basis domain.PeerBudgetRecommendationBasis, samples int) string {
	switch basis {
	case executionbudget.PeerRecommendationSimilarHistory:
		return fmt.Sprintf("Учтены похожие завершённые диалоги этой пары: %d. Добавлен ограниченный запас к наблюдаемым затратам.", samples)
	case executionbudget.PeerRecommendationPairHistory:
		return fmt.Sprintf("Похожих целей пока нет; учтены последние завершённые диалоги этой пары: %d.", samples)
	default:
		return "Завершённой истории этой пары пока недостаточно; оценка построена по длине и структуре цели."
	}
}
