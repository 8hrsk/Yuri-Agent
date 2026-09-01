package sqlite

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestRunInferenceAttributionIsImmutableAndUsageIsMonotonic(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	conversation := Conversation{ID: "conversation-attribution", AgentID: "owner", Title: "Attribution", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun("run-attribution", domain.RunKindInteractive, conversation.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Inference = domain.RunInferenceRoute{ProviderID: "openrouter", Model: "test/free"}
	if err := repositories.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(domain.RunStateQueued, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	run.Usage = domain.RunUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}
	if err := repositories.Runs.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Inference != run.Inference || stored.Usage != run.Usage {
		t.Fatalf("stored attribution = %#v / %#v", stored.Inference, stored.Usage)
	}

	changedRoute := stored
	if err := changedRoute.Transition(domain.RunStateRunning, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	changedRoute.Inference.Model = "different-model"
	if err := repositories.Runs.Save(ctx, changedRoute); err == nil || !strings.Contains(err.Error(), "inference route is immutable") {
		t.Fatalf("route mutation error = %v", err)
	}

	decreasedUsage := stored
	if err := decreasedUsage.Transition(domain.RunStateRunning, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	decreasedUsage.Usage = domain.RunUsage{InputTokens: 90, OutputTokens: 40, TotalTokens: 130}
	if err := repositories.Runs.Save(ctx, decreasedUsage); err == nil || !strings.Contains(err.Error(), "usage cannot decrease") {
		t.Fatalf("usage decrease error = %v", err)
	}

	increasedUsage := stored
	if err := increasedUsage.Transition(domain.RunStateRunning, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	increasedUsage.Usage = domain.RunUsage{InputTokens: 130, OutputTokens: 70, TotalTokens: 200}
	if err := repositories.Runs.Save(ctx, increasedUsage); err != nil {
		t.Fatalf("increase usage: %v", err)
	}
}

func TestRunInferenceFallbackSwitchIsGuardedAndDurable(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC)
	conversation := Conversation{ID: "conversation-fallback-switch", AgentID: "owner", Title: "Fallback", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRunForAgent("owner", "run-fallback-switch", domain.RunKindInteractive, conversation.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Inference = domain.RunInferenceRoute{ProviderID: "primary", Model: "model-a"}
	if err := repositories.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(domain.RunStateQueued, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(domain.RunStateRunning, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE agent_runs SET inference_route_switches = 1 WHERE id = ?`, string(run.ID)); err == nil {
		t.Fatal("SQLite accepted a fallback counter without a route switch")
	}
	if err := run.SwitchInferenceRoute(domain.RunInferenceRoute{ProviderID: "fallback", Model: "model-b"}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(ctx, run); err != nil {
		t.Fatalf("guarded route switch save: %v", err)
	}
	stored, err := repositories.Runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Inference != (domain.RunInferenceRoute{ProviderID: "fallback", Model: "model-b"}) || stored.InferenceRouteSwitches != 1 {
		t.Fatalf("stored fallback attribution = %#v switches=%d", stored.Inference, stored.InferenceRouteSwitches)
	}
	if err := stored.SwitchInferenceRoute(domain.RunInferenceRoute{ProviderID: "third", Model: "model-c"}, now.Add(4*time.Second)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second route switch error = %v, want conflict", err)
	}
}

func TestRunFailureMetadataRoundTripsWithoutProviderPayload(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	conversation := Conversation{ID: "conversation-failure", AgentID: "owner", Title: "Failure", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun("run-failure", domain.RunKindInteractive, conversation.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(domain.RunStateQueued, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(domain.RunStateRunning, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	want := domain.RunFailureInfo{Kind: domain.RunFailureRateLimit, Retryable: true, RetryAfterSeconds: 30}
	if err := run.FailWithInfo("Провайдер ограничил частоту запросов", want, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FailureInfo != want || stored.Failure != run.Failure {
		t.Fatalf("stored failure = %#v / %q", stored.FailureInfo, stored.Failure)
	}
}
