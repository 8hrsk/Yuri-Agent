package desktop

import (
	"context"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/executionbudget"
)

type budgetMetadataBackend struct{ limits executionbudget.ModelLimits }

func (backend budgetMetadataBackend) Start(context.Context, agent.ModelRequest) (agent.ModelStream, error) {
	return nil, nil
}

func (backend budgetMetadataBackend) ExecutionModelLimits(string) (executionbudget.ModelLimits, bool) {
	return backend.limits, true
}

func TestModelExecutionLimitsAndRequestedBudgetOnlyNarrowPreset(t *testing.T) {
	limits := modelExecutionLimits(budgetMetadataBackend{limits: executionbudget.ModelLimits{ContextWindow: 16_000, MaxCompletionTokens: 2_000}}, "model")
	resolved := executionbudget.ResolveRun(domain.ExecutionBudgetBalanced, executionbudget.WorkloadInteractive, limits)
	resolved = mergeRequestedRunBudget(resolved, domain.RunBudget{MaxSteps: 20, MaxTokens: 20_000, MaxToolCalls: 2, MaxDurationSeconds: 30})
	if resolved.Budget.MaxTokens != 12_000 || resolved.Budget.MaxSteps != 8 || resolved.Budget.MaxToolCalls != 2 || resolved.Budget.MaxDurationSeconds != 30 || resolved.MaxOutputTokensPerStep != 2_000 {
		t.Fatalf("resolved = %#v", resolved)
	}
}
