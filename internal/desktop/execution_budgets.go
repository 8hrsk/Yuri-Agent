package desktop

import (
	"context"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/executionbudget"
)

type executionModelLimitSource interface {
	ExecutionModelLimits(model string) (executionbudget.ModelLimits, bool)
}

func modelExecutionLimits(backend agent.ModelBackend, model string) executionbudget.ModelLimits {
	source, ok := backend.(executionModelLimitSource)
	if !ok {
		return executionbudget.ModelLimits{}
	}
	limits, found := source.ExecutionModelLimits(model)
	if !found {
		return executionbudget.ModelLimits{}
	}
	return limits
}

func runWorkload(kind domain.RunKind) executionbudget.Workload {
	switch kind {
	case domain.RunKindSubagent:
		return executionbudget.WorkloadSubagent
	case domain.RunKindBackground, domain.RunKindReflection:
		return executionbudget.WorkloadBackground
	default:
		return executionbudget.WorkloadInteractive
	}
}

func mergeRequestedRunBudget(resolved executionbudget.ResolvedRun, requested domain.RunBudget) executionbudget.ResolvedRun {
	if requested.MaxSteps > 0 {
		resolved.Budget.MaxSteps = min(resolved.Budget.MaxSteps, requested.MaxSteps)
	}
	if requested.MaxTokens > 0 {
		resolved.Budget.MaxTokens = min(resolved.Budget.MaxTokens, requested.MaxTokens)
	}
	if requested.MaxToolCalls > 0 {
		resolved.Budget.MaxToolCalls = min(resolved.Budget.MaxToolCalls, requested.MaxToolCalls)
	}
	if requested.MaxToolOutputBytes > 0 {
		resolved.Budget.MaxToolOutputBytes = min(resolved.Budget.MaxToolOutputBytes, requested.MaxToolOutputBytes)
	}
	if requested.MaxDurationSeconds > 0 {
		resolved.Budget.MaxDurationSeconds = min(resolved.Budget.MaxDurationSeconds, requested.MaxDurationSeconds)
	}
	if resolved.MaxOutputTokensPerStep > resolved.Budget.MaxTokens {
		resolved.MaxOutputTokensPerStep = resolved.Budget.MaxTokens
	}
	return resolved
}

func (b *Bridge) persistResolvedRunBudget(ctx context.Context, run *domain.AgentRun, budget domain.RunBudget) error {
	if run == nil || run.Budget == budget {
		return nil
	}
	run.Budget = budget
	run.UpdatedAt = time.Now().UTC()
	run.Version++
	return b.repositories.Runs.Save(ctx, *run)
}
