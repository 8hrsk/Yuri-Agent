package desktop

import (
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func TestGetRunUsageStatsProjectsDurableAttribution(t *testing.T) {
	database, err := storage.Open(t.Context(), t.TempDir()+"/yuri.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	profile, err := domain.NewAgentProfile("agent-stats-bridge", "Эми", 21, "female", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Agents.Create(t.Context(), profile); err != nil {
		t.Fatal(err)
	}

	completed, err := domain.NewRunForAgent(profile.ID, "bridge-stats-complete", domain.RunKindInteractive, "", now)
	if err != nil {
		t.Fatal(err)
	}
	completed.Inference = domain.RunInferenceRoute{ProviderID: "openrouter", Model: "vendor/free"}
	completed.Usage = domain.RunUsage{InputTokens: 12, OutputTokens: 8, TotalTokens: 20}
	if err := repositories.Runs.Create(t.Context(), completed); err != nil {
		t.Fatal(err)
	}
	for _, state := range []domain.RunState{domain.RunStateQueued, domain.RunStateRunning, domain.RunStateCompleted} {
		if err := completed.Transition(state, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Runs.Save(t.Context(), completed); err != nil {
			t.Fatal(err)
		}
	}

	failed, err := domain.NewRunForAgent(profile.ID, "bridge-stats-failed", domain.RunKindInteractive, "", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	failed.Inference = completed.Inference
	failed.Usage = domain.RunUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	if err := repositories.Runs.Create(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	for _, state := range []domain.RunState{domain.RunStateQueued, domain.RunStateRunning} {
		if err := failed.Transition(state, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Runs.Save(t.Context(), failed); err != nil {
			t.Fatal(err)
		}
	}
	if err := failed.FailWithInfo("rate limited", domain.RunFailureInfo{Kind: domain.RunFailureRateLimit, Retryable: true}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(t.Context(), failed); err != nil {
		t.Fatal(err)
	}

	bridge := &Bridge{database: database, repositories: repositories}
	view, err := bridge.GetRunUsageStats(RunUsageStatsInput{
		From: now.Format(time.RFC3339Nano), To: now.Add(2 * time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.From != now.Format(time.RFC3339Nano) || view.To != now.Add(2*time.Minute).Format(time.RFC3339Nano) || len(view.Groups) != 1 {
		t.Fatalf("usage stats view = %#v", view)
	}
	group := view.Groups[0]
	if group.AgentID != string(profile.ID) || group.AgentName != profile.Name || group.ProviderID != "openrouter" || group.Model != "vendor/free" ||
		group.RunCount != 2 || group.StatusCounts[string(domain.RunStateCompleted)] != 1 || group.StatusCounts[string(domain.RunStateFailed)] != 1 ||
		group.FailureKinds[string(domain.RunFailureRateLimit)] != 1 || group.InputTokens != 15 || group.OutputTokens != 10 || group.TotalTokens != 25 {
		t.Fatalf("usage stats group = %#v", group)
	}
}

func TestGetRunUsageStatsUsesFiniteDefaultsAndRejectsInvalidWindows(t *testing.T) {
	database, err := storage.Open(t.Context(), t.TempDir()+"/yuri.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{database: database, repositories: repositories}
	defaultView, err := bridge.GetRunUsageStats(RunUsageStatsInput{})
	if err != nil {
		t.Fatal(err)
	}
	from, parseFromErr := time.Parse(time.RFC3339Nano, defaultView.From)
	to, parseToErr := time.Parse(time.RFC3339Nano, defaultView.To)
	if parseFromErr != nil || parseToErr != nil || to.Sub(from) != defaultRunUsageStatsWindow {
		t.Fatalf("default usage window = %q..%q (%v, %v)", defaultView.From, defaultView.To, parseFromErr, parseToErr)
	}

	cases := []RunUsageStatsInput{
		{From: "not-a-time", To: defaultView.To},
		{From: defaultView.To, To: defaultView.To},
		{From: from.Add(-storage.MaxRunUsageStatsWindow - time.Nanosecond).Format(time.RFC3339Nano), To: defaultView.To},
	}
	for index, input := range cases {
		if _, err := bridge.GetRunUsageStats(input); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("case %d error = %v, want ErrInvalidArgument", index, err)
		}
	}
}
