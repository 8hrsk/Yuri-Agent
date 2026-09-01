package executionbudget

import (
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
