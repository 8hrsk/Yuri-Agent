package executionbudget

import (
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// ModelLimits is the secret-free subset of provider catalog metadata that can
// safely affect a run budget. Zero means that the provider did not publish the
// value and causes the conservative preset default to be used.
type ModelLimits struct {
	ContextWindow       int64
	MaxCompletionTokens int64
}

type ResolvedRun struct {
	Budget                 domain.RunBudget
	MaxOutputTokensPerStep int64
}

type Workload string

const (
	WorkloadInteractive Workload = "interactive"
	WorkloadBackground  Workload = "background"
	WorkloadSubagent    Workload = "subagent"
)

func ResolveRun(preset domain.ExecutionBudgetPreset, workload Workload, limits ModelLimits) ResolvedRun {
	preset = preset.Normalized()
	resolved := runDefaults(preset, workload)
	resolved.Budget.MaxTokens = contextBound(resolved.Budget.MaxTokens, limits.ContextWindow)
	resolved.MaxOutputTokensPerStep = positiveMin(resolved.MaxOutputTokensPerStep, limits.MaxCompletionTokens)
	resolved.MaxOutputTokensPerStep = positiveMin(resolved.MaxOutputTokensPerStep, resolved.Budget.MaxTokens)
	return resolved
}

func ResolvePeer(preset domain.ExecutionBudgetPreset, limits ModelLimits) domain.PeerDialogueBudget {
	preset = preset.Normalized()
	var result domain.PeerDialogueBudget
	switch preset {
	case domain.ExecutionBudgetEfficient:
		result = domain.PeerDialogueBudget{MinTurns: 1, MaxTurns: 2, MaxTokens: 4_000, MaxDurationSeconds: 45, CooldownSeconds: 300}
	case domain.ExecutionBudgetExtended:
		result = domain.PeerDialogueBudget{MinTurns: 2, MaxTurns: 6, MaxTokens: 12_000, MaxDurationSeconds: 180, CooldownSeconds: 300}
	default:
		result = domain.PeerDialogueBudget{MinTurns: 2, MaxTurns: 4, MaxTokens: 8_000, MaxDurationSeconds: 90, CooldownSeconds: 300}
	}
	result.MaxTokens = contextBound(result.MaxTokens, limits.ContextWindow)
	return result
}

func runDefaults(preset domain.ExecutionBudgetPreset, workload Workload) ResolvedRun {
	if workload == WorkloadSubagent {
		switch preset {
		case domain.ExecutionBudgetEfficient:
			return ResolvedRun{Budget: domain.RunBudget{MaxSteps: 2, MaxTokens: 2_000, MaxToolCalls: 1, MaxToolOutputBytes: 16 * 1024, MaxDurationSeconds: 45}, MaxOutputTokensPerStep: 1_000}
		case domain.ExecutionBudgetExtended:
			return ResolvedRun{Budget: domain.RunBudget{MaxSteps: 6, MaxTokens: 8_000, MaxToolCalls: 3, MaxToolOutputBytes: 16 * 1024, MaxDurationSeconds: 90}, MaxOutputTokensPerStep: 4_000}
		default:
			return ResolvedRun{Budget: domain.RunBudget{MaxSteps: 4, MaxTokens: 4_000, MaxToolCalls: 3, MaxToolOutputBytes: 16 * 1024, MaxDurationSeconds: 60}, MaxOutputTokensPerStep: 2_000}
		}
	}
	switch preset {
	case domain.ExecutionBudgetEfficient:
		return ResolvedRun{Budget: domain.RunBudget{MaxSteps: 4, MaxTokens: 12_000, MaxToolCalls: 8, MaxToolOutputBytes: 128 * 1024, MaxDurationSeconds: 180}, MaxOutputTokensPerStep: 2_000}
	case domain.ExecutionBudgetExtended:
		return ResolvedRun{Budget: domain.RunBudget{MaxSteps: 12, MaxTokens: 64_000, MaxToolCalls: 48, MaxToolOutputBytes: 512 * 1024, MaxDurationSeconds: 900}, MaxOutputTokensPerStep: 8_000}
	default:
		return ResolvedRun{Budget: domain.RunBudget{MaxSteps: 8, MaxTokens: 32_000, MaxToolCalls: 32, MaxToolOutputBytes: 256 * 1024, MaxDurationSeconds: 600}, MaxOutputTokensPerStep: 4_000}
	}
}

func contextBound(value, contextWindow int64) int64 {
	if contextWindow <= 0 {
		return value
	}
	// Leave one quarter of the published context window outside the aggregate
	// run budget for provider framing, tool schemas, and accounting variance.
	bound := contextWindow * 3 / 4
	if bound < 1_024 {
		bound = 1_024
	}
	return positiveMin(value, bound)
}

func positiveMin(value, bound int64) int64 {
	if bound <= 0 || value <= bound {
		return value
	}
	return bound
}
