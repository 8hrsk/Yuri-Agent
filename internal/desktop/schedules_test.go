package desktop

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	schedulerpkg "github.com/OrdoAI/yuri-agent/internal/scheduler"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func newScheduleTestBridge(t *testing.T) *Bridge {
	t.Helper()
	database, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := schedulerpkg.New(repositories.Scheduler, schedulerpkg.ExecuteFunc(func(context.Context, schedulerpkg.ScheduledJob) error { return nil }), schedulerpkg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &Bridge{database: database, repositories: repositories, scheduler: worker, shuttingDown: true}
}

func TestScheduleBridgeCRUDAndManualRun(t *testing.T) {
	bridge := newScheduleTestBridge(t)
	created, err := bridge.CreateSchedule(ScheduleInput{
		Title: "Утренняя сводка", Prompt: "Подготовь краткую сводку", Type: "cron",
		Expression: "0 9 * * 1-5", Timezone: "Europe/Moscow", MisfirePolicy: "run_once",
		DeliveryChannel: "in_app", Budget: ScheduleBudgetView{MaxDurationSeconds: 120, MaxTokens: 2000, MaxToolCalls: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.NextRunAt == "" || !created.Enabled || created.Type != "cron" {
		t.Fatalf("created schedule = %#v", created)
	}
	items, err := bridge.ListSchedules()
	if err != nil || len(items) != 1 {
		t.Fatalf("ListSchedules() = %#v, %v", items, err)
	}
	paused, err := bridge.SetScheduleEnabled(ScheduleIDInput{ID: created.ID, Enabled: false})
	if err != nil || paused.Status != "paused" || paused.Enabled {
		t.Fatalf("paused schedule = %#v, %v", paused, err)
	}
	resumed, err := bridge.SetScheduleEnabled(ScheduleIDInput{ScheduleID: created.ID, Enabled: true})
	if err != nil || resumed.Status != "active" || !resumed.Enabled {
		t.Fatalf("resumed schedule = %#v, %v", resumed, err)
	}
	run, err := bridge.RunScheduleNow(ScheduleIDInput{ID: created.ID})
	if err != nil || run.ScheduleID != created.ID || run.Status != "queued" {
		t.Fatalf("manual run = %#v, %v", run, err)
	}
	if err := bridge.CancelJobRun(JobRunIDInput{RunID: run.ID}); err != nil {
		t.Fatalf("CancelJobRun() error = %v", err)
	}
	runs, err := bridge.ListJobRuns(JobRunListInput{Limit: 10})
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID || runs[0].Status != "cancelled" {
		t.Fatalf("ListJobRuns() = %#v, %v", runs, err)
	}
	if err := bridge.DeleteSchedule(ScheduleIDInput{ID: created.ID}); err != nil {
		t.Fatal(err)
	}
	items, err = bridge.ListSchedules()
	if err != nil || len(items) != 0 {
		t.Fatalf("schedules after delete = %#v, %v", items, err)
	}
}

func TestScheduleBridgeRejectsInvalidCronAndPreservesOneShotMisfire(t *testing.T) {
	bridge := newScheduleTestBridge(t)
	_, err := bridge.CreateSchedule(ScheduleInput{
		Title: "Bad cron", Prompt: "noop", Type: "cron", Expression: "0 9 * *",
		Timezone: "UTC", MisfirePolicy: "skip",
	})
	if err == nil {
		t.Fatal("invalid four-field cron was accepted")
	}
	past, err := bridge.CreateSchedule(ScheduleInput{
		Title: "Past", Prompt: "noop", Type: "once", RunAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
		Timezone: "UTC", MisfirePolicy: "run_once",
	})
	if err != nil || past.NextRunAt == "" {
		t.Fatalf("past one-shot must remain due for run_once recovery: %#v, %v", past, err)
	}
}

func TestBackgroundToolAuthorizerDeniesSideEffects(t *testing.T) {
	decision, err := (backgroundToolAuthorizer{}).Authorize(context.Background(), agent.ToolAuthorizationRequest{
		Tool: agent.ToolDescriptor{Name: "send", Description: "send", Risk: domain.RiskHigh, InputSchema: []byte(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != domain.PermissionDeny {
		t.Fatalf("background decision = %+v", decision)
	}
}

func TestSchedulerCreateToolRequiresApprovalRisk(t *testing.T) {
	descriptor := (scheduleAgentTool{}).Descriptor()
	if descriptor.Risk != domain.RiskMedium || len(descriptor.Capabilities) != 1 || descriptor.Capabilities[0] != domain.CapabilitySchedulerManage {
		t.Fatalf("scheduler tool descriptor = %#v", descriptor)
	}
}

// TestScheduledJobSurvivesActiveAgentSwitch reproduces H-1: the scheduled run
// used one agent-agnostic conversation ID, so every execution after the owner
// switched agents was rejected by the conversation ownership check and the
// schedule failed forever.
func TestScheduledJobSurvivesActiveAgentSwitch(t *testing.T) {
	server := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		writeProviderProbeSSE(writer, flusher, "response.created", map[string]any{
			"type": "response.created", "response": map[string]any{"id": "response-schedule"},
		})
		writeProviderProbeSSE(writer, flusher, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "response_id": "response-schedule", "delta": "Готово.",
		})
		writeProviderProbeSSE(writer, flusher, "response.completed", map[string]any{
			"type": "response.completed", "response_id": "response-schedule",
			"response": map[string]any{"id": "response-schedule", "usage": map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6}},
		})
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	bridge := newOpenAIBridgeSmoke(t, server.URL+"/v1", "sk-schedule-agent-switch")
	worker, err := schedulerpkg.New(bridge.repositories.Scheduler, schedulerpkg.ExecuteFunc(func(context.Context, schedulerpkg.ScheduledJob) error { return nil }), schedulerpkg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bridge.scheduler = worker

	first, err := bridge.CreateAgent(CreateAgentInput{Name: "Юри", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := bridge.CreateSchedule(ScheduleInput{
		Title: "Ежедневная сводка", Prompt: "Подготовь сводку", Type: "interval", IntervalSeconds: 3600,
		Timezone: "UTC", MisfirePolicy: "run_once", DeliveryChannel: "in_app",
	})
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := bridge.scheduler.GetSchedule(context.Background(), domain.ID(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerpkg.ScheduledJob{Schedule: schedule, Run: domain.JobRun{
		ID: "job-run-owner", ScheduleID: schedule.ID, State: domain.JobRunRunning, Trigger: domain.JobTriggerScheduled, Attempt: 1,
	}}
	if err := bridge.executeScheduledJob(context.Background(), job); err != nil {
		t.Fatalf("first scheduled execution under the creating agent: %v", err)
	}

	second, err := bridge.CreateAgent(CreateAgentInput{Name: "Мира", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetActiveAgent(SelectAgentInput{ID: second.ID}); err != nil {
		t.Fatal(err)
	}
	job.Run.ID = "job-run-after-switch"
	if err := bridge.executeScheduledJob(context.Background(), job); err != nil {
		t.Fatalf("scheduled execution after switching the active agent: %v", err)
	}

	ctx := context.Background()
	ownerConversations, err := bridge.repositories.Conversations.ListByAgent(ctx, domain.ID(first.ID))
	if err != nil || len(ownerConversations) != 1 {
		t.Fatalf("creating agent conversations = %#v err=%v", ownerConversations, err)
	}
	switchedConversations, err := bridge.repositories.Conversations.ListByAgent(ctx, domain.ID(second.ID))
	if err != nil || len(switchedConversations) != 1 {
		t.Fatalf("switched agent conversations = %#v err=%v", switchedConversations, err)
	}
	if ownerConversations[0].ID == switchedConversations[0].ID {
		t.Fatalf("scheduled conversations were not separated per agent: %q", ownerConversations[0].ID)
	}
	runs, err := bridge.repositories.Runs.ListByConversation(ctx, switchedConversations[0].ID, 10)
	if err != nil || len(runs) != 1 || runs[0].AgentID != domain.ID(second.ID) || runs[0].State != domain.RunStateCompleted {
		t.Fatalf("runs after switch = %#v err=%v", runs, err)
	}
}

func TestScheduleConversationIDIsPerAgentAndKeepsLegacyOwnerHistory(t *testing.T) {
	bridge := newScheduleTestBridge(t)
	ctx := context.Background()
	now := time.Now().UTC()
	legacyID := domain.ID("schedule_task-1")
	if err := bridge.repositories.Conversations.Create(ctx, storage.Conversation{
		ID: legacyID, AgentID: "agent-a", Title: "Ежедневная сводка", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if id := bridge.scheduleConversationID(ctx, "task-1", "agent-a"); id != legacyID {
		t.Fatalf("owner of the legacy conversation lost its history: %q", id)
	}
	if id := bridge.scheduleConversationID(ctx, "task-1", "agent-b"); id != "schedule_agent-b_task-1" {
		t.Fatalf("foreign agent reused another agent's conversation: %q", id)
	}
	if id := bridge.scheduleConversationID(ctx, "task-2", "agent-a"); id != "schedule_agent-a_task-2" {
		t.Fatalf("new schedule conversation = %q, want a per-agent id", id)
	}
}
