package executionbudget

import (
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestResolveRunUsesPresetWorkloadAndModelCaps(t *testing.T) {
	balanced := ResolveRun(domain.ExecutionBudgetBalanced, WorkloadInteractive, ModelLimits{})
	if balanced.Budget.MaxTokens != 32_000 || balanced.Budget.MaxSteps != 8 || balanced.MaxOutputTokensPerStep != 4_000 {
		t.Fatalf("balanced = %#v", balanced)
	}
	bounded := ResolveRun(domain.ExecutionBudgetExtended, WorkloadInteractive, ModelLimits{ContextWindow: 32_000, MaxCompletionTokens: 3_000})
	if bounded.Budget.MaxTokens != 24_000 || bounded.MaxOutputTokensPerStep != 3_000 {
		t.Fatalf("bounded = %#v", bounded)
	}
	subagent := ResolveRun(domain.ExecutionBudgetEfficient, WorkloadSubagent, ModelLimits{})
	if subagent.Budget.MaxSteps != 2 || subagent.Budget.MaxToolCalls != 1 || subagent.Budget.MaxTokens != 2_000 {
		t.Fatalf("subagent = %#v", subagent)
	}
}

func TestResolvePeerKeepsSemanticRangeAndContextHeadroom(t *testing.T) {
	extended := ResolvePeer(domain.ExecutionBudgetExtended, ModelLimits{ContextWindow: 8_000})
	if extended.MinTurns != 2 || extended.MaxTurns != 6 || extended.MaxTokens != 6_000 || !extended.Valid() {
		t.Fatalf("extended peer = %#v", extended)
	}
	efficient := ResolvePeer(domain.ExecutionBudgetEfficient, ModelLimits{})
	if efficient.MinTurns != 1 || efficient.MaxTurns != 2 || efficient.MaxTokens != 4_000 || !efficient.Valid() {
		t.Fatalf("efficient peer = %#v", efficient)
	}
}

func TestTinyPublishedContextNeverExpandsPastModelLimit(t *testing.T) {
	resolved := ResolveRun(domain.ExecutionBudgetExtended, WorkloadInteractive, ModelLimits{ContextWindow: 512, MaxCompletionTokens: 128})
	if resolved.Budget.MaxTokens != 512 || resolved.MaxOutputTokensPerStep != 128 {
		t.Fatalf("tiny context resolution = %#v", resolved)
	}
}

func TestPeerOverrideCanOnlyNarrowResolvedBudget(t *testing.T) {
	base := ResolvePeer(domain.ExecutionBudgetBalanced, ModelLimits{})
	narrowed := NarrowPeer(base, PeerOverride{MaxTurns: 1, MaxTokens: 2_000, MaxDurationSeconds: 30})
	if narrowed.MinTurns != 1 || narrowed.MaxTurns != 1 || narrowed.MaxTokens != 2_000 || narrowed.MaxDurationSeconds != 30 || !narrowed.Valid() {
		t.Fatalf("narrowed peer = %#v", narrowed)
	}
	oversized := NarrowPeer(base, PeerOverride{MaxTurns: 99, MaxTokens: 99_000, MaxDurationSeconds: 999})
	if oversized != base {
		t.Fatalf("oversized override expanded base: %#v != %#v", oversized, base)
	}
}

func TestPeerRecommendationUsesSimilarObservedCostInsideCeiling(t *testing.T) {
	ceiling := ResolvePeer(domain.ExecutionBudgetExtended, ModelLimits{})
	recommendation, err := RecommendPeer(ceiling, "Проверить архитектуру плана", []PeerHistorySample{
		{Purpose: "Проверить архитектуру нового плана", Turns: 4, Tokens: 7_200, DurationSeconds: 88, HitHardLimit: true},
		{Purpose: "Поговорить о погоде", Turns: 2, Tokens: 1_500, DurationSeconds: 18},
	})
	if err != nil {
		t.Fatal(err)
	}
	if recommendation.Basis != PeerRecommendationSimilarHistory || recommendation.SampleCount != 1 {
		t.Fatalf("recommendation provenance = %#v", recommendation)
	}
	if recommendation.Budget.MaxTurns != 5 || recommendation.Budget.MaxTokens != 9_500 || recommendation.Budget.MaxDurationSeconds != 115 || !recommendation.Budget.Valid() {
		t.Fatalf("adaptive recommendation = %#v", recommendation.Budget)
	}
}

func TestPeerRecommendationNeverExpandsResolvedCeiling(t *testing.T) {
	ceiling := ResolvePeer(domain.ExecutionBudgetEfficient, ModelLimits{ContextWindow: 4_000})
	recommendation, err := RecommendPeer(ceiling, strings.Repeat("сложная составная цель; ", 10), []PeerHistorySample{
		{Purpose: "сложная составная цель", Turns: 8, Tokens: 15_000, DurationSeconds: 299, HitHardLimit: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if recommendation.Budget != ceiling {
		t.Fatalf("recommendation expanded ceiling: %#v != %#v", recommendation.Budget, ceiling)
	}
}
