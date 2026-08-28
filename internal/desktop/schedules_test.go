package desktop

import (
	"context"
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
