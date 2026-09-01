package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestRunRepositoryAggregateUsageStatsGroupsRouteAgentAndFailures(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for _, profile := range []domain.AgentProfile{
		mustStatsProfile(t, "agent-stats-a", "Эми", now),
		mustStatsProfile(t, "agent-stats-b", "Юри", now.Add(time.Second)),
	} {
		if err := repositories.Agents.Create(ctx, profile); err != nil {
			t.Fatal(err)
		}
	}

	persistStatsRun(t, repositories, statsRunSpec{
		id: "stats-a-complete", agentID: "agent-stats-a", createdAt: now,
		providerID: "openrouter", model: "vendor/free", state: domain.RunStateCompleted,
		usage: domain.RunUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	})
	persistStatsRun(t, repositories, statsRunSpec{
		id: "stats-a-failed", agentID: "agent-stats-a", createdAt: now.Add(time.Minute),
		providerID: "openrouter", model: "vendor/free", state: domain.RunStateFailed,
		failureKind: domain.RunFailureRateLimit,
		usage:       domain.RunUsage{InputTokens: 4, OutputTokens: 6, TotalTokens: 10},
	})
	persistStatsRun(t, repositories, statsRunSpec{
		id: "stats-a-queued", agentID: "agent-stats-a", createdAt: now.Add(2 * time.Minute),
		providerID: "openrouter", model: "vendor/free", state: domain.RunStateQueued,
		usage: domain.RunUsage{InputTokens: 1, OutputTokens: 0, TotalTokens: 1},
	})
	persistStatsRun(t, repositories, statsRunSpec{
		id: "stats-b-complete", agentID: "agent-stats-b", createdAt: now.Add(3 * time.Minute),
		providerID: "codex", model: "gpt-5", state: domain.RunStateCompleted,
		usage: domain.RunUsage{InputTokens: 100, OutputTokens: 25, TotalTokens: 125},
	})
	// An empty route is retained as its own historical bucket. A failed legacy
	// row with no failure_kind is surfaced as the stable "unknown" bucket.
	persistStatsRun(t, repositories, statsRunSpec{
		id: "stats-b-legacy", agentID: "agent-stats-b", createdAt: now.Add(4 * time.Minute),
		state: domain.RunStateFailed, usage: domain.RunUsage{TotalTokens: 3},
	})
	// The upper bound is exclusive and this row must not affect the report.
	persistStatsRun(t, repositories, statsRunSpec{
		id: "stats-outside", agentID: "agent-stats-a", createdAt: now.Add(10 * time.Minute),
		providerID: "openrouter", model: "vendor/free", state: domain.RunStateCompleted,
		usage: domain.RunUsage{InputTokens: 900, OutputTokens: 900, TotalTokens: 1800},
	})

	groups, err := repositories.Runs.AggregateUsageStats(ctx, RunUsageStatsOptions{
		From: now, To: now.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 {
		t.Fatalf("groups = %#v, want 3 route/agent buckets", groups)
	}

	openRouter := findUsageStatsGroup(t, groups, "agent-stats-a", "openrouter", "vendor/free")
	if openRouter.AgentName != "Эми" || openRouter.RunCount != 3 ||
		openRouter.StatusCounts[string(domain.RunStateCompleted)] != 1 ||
		openRouter.StatusCounts[string(domain.RunStateFailed)] != 1 ||
		openRouter.StatusCounts[string(domain.RunStateQueued)] != 1 ||
		openRouter.FailureKinds[string(domain.RunFailureRateLimit)] != 1 {
		t.Fatalf("openrouter aggregate = %#v", openRouter)
	}
	if openRouter.Usage != (domain.RunUsage{InputTokens: 15, OutputTokens: 11, TotalTokens: 26}) {
		t.Fatalf("openrouter usage = %#v", openRouter.Usage)
	}

	codex := findUsageStatsGroup(t, groups, "agent-stats-b", "codex", "gpt-5")
	if codex.AgentName != "Юри" || codex.RunCount != 1 || codex.StatusCounts[string(domain.RunStateCompleted)] != 1 ||
		codex.Usage != (domain.RunUsage{InputTokens: 100, OutputTokens: 25, TotalTokens: 125}) {
		t.Fatalf("codex aggregate = %#v", codex)
	}

	legacy := findUsageStatsGroup(t, groups, "agent-stats-b", "", "")
	if legacy.RunCount != 1 || legacy.FailureKinds[string(domain.RunFailureUnknown)] != 1 || legacy.Usage.TotalTokens != 3 {
		t.Fatalf("legacy aggregate = %#v", legacy)
	}

	agentGroups, err := repositories.Runs.AggregateUsageStats(ctx, RunUsageStatsOptions{
		From: now, To: now.Add(10 * time.Minute), AgentID: "agent-stats-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(agentGroups) != 1 || agentGroups[0].AgentID != "agent-stats-a" {
		t.Fatalf("filtered groups = %#v", agentGroups)
	}
}

func TestRunRepositoryAggregateUsageStatsRequiresBoundedWindow(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	cases := []RunUsageStatsOptions{
		{From: now},
		{To: now},
		{From: now, To: now},
		{From: now.Add(-MaxRunUsageStatsWindow - time.Nanosecond), To: now},
	}
	for index, options := range cases {
		if _, err := repositories.Runs.AggregateUsageStats(ctx, options); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("case %d error = %v, want ErrInvalidArgument", index, err)
		}
	}
}

type statsRunSpec struct {
	id          string
	agentID     string
	createdAt   time.Time
	providerID  string
	model       string
	state       domain.RunState
	failureKind domain.RunFailureKind
	usage       domain.RunUsage
}

func mustStatsProfile(t *testing.T, id, name string, now time.Time) domain.AgentProfile {
	t.Helper()
	profile, err := domain.NewAgentProfile(domain.ID(id), name, 21, "female", "", now)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func persistStatsRun(t *testing.T, repositories *Repositories, spec statsRunSpec) {
	t.Helper()
	run, err := domain.NewRunForAgent(domain.ID(spec.agentID), domain.ID(spec.id), domain.RunKindBackground, "", spec.createdAt)
	if err != nil {
		t.Fatal(err)
	}
	run.Inference = domain.RunInferenceRoute{ProviderID: spec.providerID, Model: spec.model}
	run.Usage = spec.usage
	run.Budget = domain.RunBudget{MaxSteps: 2, MaxTokens: 2000, MaxToolOutputBytes: 4096, MaxDurationSeconds: 30}
	if err := repositories.Runs.Create(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if spec.state == domain.RunStateCreated {
		return
	}
	for _, next := range []domain.RunState{domain.RunStateQueued, domain.RunStateRunning} {
		if spec.state == domain.RunStateQueued && next == domain.RunStateRunning {
			break
		}
		if err := run.Transition(next, spec.createdAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Runs.Save(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	if spec.state == domain.RunStateQueued || spec.state == domain.RunStateRunning {
		return
	}
	if spec.state == domain.RunStateFailed {
		info := domain.RunFailureInfo{Kind: spec.failureKind, Retryable: spec.failureKind == domain.RunFailureRateLimit}
		if spec.failureKind == "" {
			info = domain.RunFailureInfo{}
		}
		if err := run.FailWithInfo("test failure", info, spec.createdAt.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	} else if err := run.Transition(spec.state, spec.createdAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(t.Context(), run); err != nil {
		t.Fatal(err)
	}
}

func findUsageStatsGroup(t *testing.T, groups []RunUsageStatsGroup, agentID, providerID, model string) RunUsageStatsGroup {
	t.Helper()
	for _, group := range groups {
		if string(group.AgentID) == agentID && group.ProviderID == providerID && group.Model == model {
			return group
		}
	}
	t.Fatalf("usage stats group %s/%s/%s not found in %#v", agentID, providerID, model, groups)
	return RunUsageStatsGroup{}
}
