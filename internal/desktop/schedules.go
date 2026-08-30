package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	schedulerpkg "github.com/OrdoAI/yuri-agent/internal/scheduler"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

type ScheduleBudgetView struct {
	MaxDurationSeconds int   `json:"maxDurationSeconds,omitempty"`
	MaxTokens          int64 `json:"maxTokens,omitempty"`
	MaxToolCalls       int   `json:"maxToolCalls,omitempty"`
}

type ScheduleInput struct {
	ID              string             `json:"id,omitempty"`
	Title           string             `json:"title"`
	Prompt          string             `json:"prompt"`
	Type            string             `json:"type"`
	RunAt           string             `json:"runAt,omitempty"`
	IntervalSeconds int64              `json:"intervalSeconds,omitempty"`
	Expression      string             `json:"expression,omitempty"`
	Timezone        string             `json:"timezone"`
	MisfirePolicy   string             `json:"misfirePolicy"`
	Enabled         *bool              `json:"enabled,omitempty"`
	DeliveryChannel string             `json:"deliveryChannel,omitempty"`
	Budget          ScheduleBudgetView `json:"budget,omitempty"`
}

type ScheduleView struct {
	ID              string             `json:"id"`
	Title           string             `json:"title"`
	Prompt          string             `json:"prompt"`
	Type            string             `json:"type"`
	RunAt           string             `json:"runAt,omitempty"`
	IntervalSeconds int64              `json:"intervalSeconds,omitempty"`
	Expression      string             `json:"expression,omitempty"`
	Timezone        string             `json:"timezone"`
	MisfirePolicy   string             `json:"misfirePolicy"`
	Enabled         bool               `json:"enabled"`
	Status          string             `json:"status"`
	NextRunAt       string             `json:"nextRunAt,omitempty"`
	LastRunAt       string             `json:"lastRunAt,omitempty"`
	DeliveryChannel string             `json:"deliveryChannel"`
	Budget          ScheduleBudgetView `json:"budget"`
	CreatedAt       string             `json:"createdAt"`
	UpdatedAt       string             `json:"updatedAt"`
}

type ScheduleIDInput struct {
	ID         string `json:"id"`
	ScheduleID string `json:"scheduleId,omitempty"`
	Enabled    bool   `json:"enabled,omitempty"`
}

type JobRunListInput struct {
	ScheduleID string `json:"scheduleId,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type JobRunIDInput struct {
	ID    string `json:"id,omitempty"`
	RunID string `json:"runId,omitempty"`
}

type JobRunView struct {
	ID            string `json:"id"`
	ScheduleID    string `json:"scheduleId"`
	ScheduleTitle string `json:"scheduleTitle,omitempty"`
	Status        string `json:"status"`
	Attempt       int    `json:"attempt"`
	StartedAt     string `json:"startedAt,omitempty"`
	FinishedAt    string `json:"finishedAt,omitempty"`
	DurationMS    int64  `json:"durationMs,omitempty"`
	Error         string `json:"error,omitempty"`
	Summary       string `json:"summary,omitempty"`
	TriggeredBy   string `json:"triggeredBy"`
}

type scheduledPayload struct {
	Kind            string               `json:"kind"`
	Prompt          string               `json:"prompt,omitempty"`
	DeliveryChannel string               `json:"delivery_channel,omitempty"`
	Internal        bool                 `json:"internal,omitempty"`
	Notification    *domain.Notification `json:"notification,omitempty"`
}

func (b *Bridge) ListSchedules() ([]ScheduleView, error) {
	ctx, cancel := b.context()
	defer cancel()
	items, err := b.scheduler.ListSchedules(ctx, domain.ScheduleListOptions{IncludePaused: true, IncludeCompleted: true, Limit: 500})
	if err != nil {
		return nil, err
	}
	views := make([]ScheduleView, 0, len(items))
	for _, item := range items {
		payload, err := decodeScheduledPayload(item.PayloadJSON)
		if err != nil || payload.Internal {
			continue
		}
		views = append(views, scheduleView(item, payload))
	}
	return views, nil
}

func (b *Bridge) CreateSchedule(input ScheduleInput) (ScheduleView, error) {
	ctx, cancel := b.context()
	defer cancel()
	return b.createSchedule(ctx, input, domain.ActorUser)
}

func (b *Bridge) createSchedule(ctx context.Context, input ScheduleInput, actor domain.Actor) (ScheduleView, error) {
	schedule, payload, err := b.scheduleFromInput(input, domain.Schedule{})
	if err != nil {
		return ScheduleView{}, err
	}
	if err := b.scheduler.CreateSchedule(ctx, schedule); err != nil {
		return ScheduleView{}, err
	}
	b.recordScheduleAudit(ctx, actor, "schedule.create", schedule.ID, "")
	created, err := b.scheduler.GetSchedule(ctx, schedule.ID)
	if err != nil {
		return ScheduleView{}, err
	}
	return scheduleView(created, payload), nil
}

func (b *Bridge) UpdateSchedule(input ScheduleInput) (ScheduleView, error) {
	ctx, cancel := b.context()
	defer cancel()
	id := domain.ID(strings.TrimSpace(input.ID))
	if id.Empty() {
		return ScheduleView{}, fmt.Errorf("schedule id is required")
	}
	current, err := b.scheduler.GetSchedule(ctx, id)
	if err != nil {
		return ScheduleView{}, err
	}
	edited, payload, err := b.scheduleFromInput(input, current)
	if err != nil {
		return ScheduleView{}, err
	}
	edited.ID = current.ID
	edited.CreatedAt = current.CreatedAt
	edited.Version = current.Version
	edited.NextRunAt = time.Time{}
	updated, err := b.scheduler.UpdateSchedule(ctx, edited)
	if err != nil {
		return ScheduleView{}, err
	}
	b.recordScheduleAudit(ctx, domain.ActorUser, "schedule.update", updated.ID, "")
	return scheduleView(updated, payload), nil
}

func (b *Bridge) SetScheduleEnabled(input ScheduleIDInput) (ScheduleView, error) {
	ctx, cancel := b.context()
	defer cancel()
	id := scheduleInputID(input)
	var (
		item domain.Schedule
		err  error
	)
	if input.Enabled {
		item, err = b.scheduler.Resume(ctx, id)
	} else {
		item, err = b.scheduler.Pause(ctx, id)
	}
	if err != nil {
		return ScheduleView{}, err
	}
	payload, err := decodeScheduledPayload(item.PayloadJSON)
	if err != nil {
		return ScheduleView{}, err
	}
	action := "schedule.pause"
	if input.Enabled {
		action = "schedule.resume"
	}
	b.recordScheduleAudit(ctx, domain.ActorUser, action, item.ID, "")
	return scheduleView(item, payload), nil
}

func (b *Bridge) RunScheduleNow(input ScheduleIDInput) (JobRunView, error) {
	ctx, cancel := b.context()
	defer cancel()
	id := scheduleInputID(input)
	run, err := b.scheduler.ManualRun(ctx, id)
	if err != nil {
		return JobRunView{}, err
	}
	item, err := b.scheduler.GetSchedule(ctx, id)
	if err != nil {
		return JobRunView{}, err
	}
	b.recordScheduleAudit(ctx, domain.ActorUser, "job.queued", id, run.ID)
	b.runSchedulerSoon()
	return jobRunView(run, item.Name), nil
}

func (b *Bridge) DeleteSchedule(input ScheduleIDInput) error {
	ctx, cancel := b.context()
	defer cancel()
	id := scheduleInputID(input)
	if err := b.scheduler.Delete(ctx, id); err != nil {
		return err
	}
	b.recordScheduleAudit(ctx, domain.ActorUser, "schedule.delete", id, "")
	return nil
}

func (b *Bridge) ListJobRuns(input JobRunListInput) ([]JobRunView, error) {
	ctx, cancel := b.context()
	defer cancel()
	limit := input.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	runs, err := b.repositories.Scheduler.ListJobRuns(ctx, domain.ID(strings.TrimSpace(input.ScheduleID)), domain.JobRunListOptions{Limit: limit})
	if err != nil {
		return nil, err
	}
	titles := make(map[domain.ID]string)
	views := make([]JobRunView, 0, len(runs))
	for _, run := range runs {
		title, ok := titles[run.ScheduleID]
		if !ok {
			if schedule, getErr := b.repositories.Scheduler.GetSchedule(ctx, run.ScheduleID); getErr == nil {
				title = schedule.Name
			}
			titles[run.ScheduleID] = title
		}
		views = append(views, jobRunView(run, title))
	}
	return views, nil
}

func (b *Bridge) CancelJobRun(input JobRunIDInput) error {
	ctx, cancel := b.context()
	defer cancel()
	id := domain.ID(strings.TrimSpace(firstNonEmpty(input.RunID, input.ID)))
	if id.Empty() {
		return fmt.Errorf("job run id is required")
	}
	run, err := b.scheduler.GetJobRun(ctx, id)
	if err != nil {
		return err
	}
	if run.State.Terminal() {
		return nil
	}
	if err := b.scheduler.CancelRun(ctx, id); err != nil {
		return err
	}
	b.recordScheduleAudit(ctx, domain.ActorUser, "job.cancelled", run.ScheduleID, run.ID)
	return nil
}

func (b *Bridge) scheduleFromInput(input ScheduleInput, current domain.Schedule) (domain.Schedule, scheduledPayload, error) {
	title := strings.TrimSpace(input.Title)
	prompt := strings.TrimSpace(input.Prompt)
	if title == "" || prompt == "" {
		return domain.Schedule{}, scheduledPayload{}, fmt.Errorf("schedule title and prompt are required")
	}
	delivery := strings.TrimSpace(input.DeliveryChannel)
	if delivery == "" {
		delivery = "in_app"
	}
	if delivery != "in_app" && delivery != "notification" {
		return domain.Schedule{}, scheduledPayload{}, fmt.Errorf("unsupported delivery channel %q", delivery)
	}
	payload := scheduledPayload{Kind: "agent_task", Prompt: prompt, DeliveryChannel: delivery}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.Schedule{}, scheduledPayload{}, err
	}
	now := time.Now().UTC()
	id := current.ID
	if id.Empty() {
		id, err = domain.NewID("schedule")
		if err != nil {
			return domain.Schedule{}, scheduledPayload{}, err
		}
	}
	enabled := true
	status := domain.ScheduleStatusActive
	if !current.ID.Empty() {
		enabled = current.Enabled
		status = current.Status
		if status == domain.ScheduleStatusCompleted {
			enabled, status = true, domain.ScheduleStatusActive
		}
	}
	if input.Enabled != nil {
		enabled = *input.Enabled
		if enabled {
			status = domain.ScheduleStatusActive
		} else {
			status = domain.ScheduleStatusPaused
		}
	}
	timezone := strings.TrimSpace(input.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	schedule := domain.Schedule{
		ID: id, Name: title, Kind: domain.ScheduleKind(strings.TrimSpace(input.Type)),
		Expression: strings.TrimSpace(input.Expression), Timezone: timezone,
		IntervalSeconds: input.IntervalSeconds, PayloadJSON: string(encoded), Status: status, Enabled: enabled,
		MisfirePolicy: domain.MisfirePolicy(strings.TrimSpace(input.MisfirePolicy)),
		Retry:         domain.RetryPolicy{MaxAttempts: 3, InitialBackoffSecond: 5, MaxBackoffSecond: 300},
		Budget:        domain.JobBudget{MaxDurationSeconds: input.Budget.MaxDurationSeconds, MaxTokens: input.Budget.MaxTokens, MaxToolCalls: input.Budget.MaxToolCalls},
		AllowOverlap:  false, HistoryLimit: 100, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if schedule.MisfirePolicy == "" {
		schedule.MisfirePolicy = domain.MisfireRunOnce
	}
	if schedule.Budget.MaxDurationSeconds == 0 {
		schedule.Budget.MaxDurationSeconds = 180
	}
	if schedule.Budget.MaxTokens == 0 {
		schedule.Budget.MaxTokens = 1_800
	}
	if schedule.Budget.MaxToolCalls == 0 {
		schedule.Budget.MaxToolCalls = 8
	}
	switch schedule.Kind {
	case domain.ScheduleKindOnce:
		runAt, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(input.RunAt))
		if parseErr != nil {
			return domain.Schedule{}, scheduledPayload{}, fmt.Errorf("parse runAt: %w", parseErr)
		}
		schedule.StartAt = runAt.UTC()
		schedule.Expression = ""
		schedule.IntervalSeconds = 0
	case domain.ScheduleKindInterval:
		if schedule.IntervalSeconds <= 0 {
			return domain.Schedule{}, scheduledPayload{}, fmt.Errorf("intervalSeconds must be positive")
		}
		schedule.StartAt = now.Add(time.Duration(schedule.IntervalSeconds) * time.Second)
		schedule.Expression = ""
	case domain.ScheduleKindCron:
		schedule.StartAt = now
		schedule.IntervalSeconds = 0
	default:
		return domain.Schedule{}, scheduledPayload{}, fmt.Errorf("unsupported schedule type %q", input.Type)
	}
	return schedule, payload, nil
}

func (b *Bridge) executeScheduledJob(ctx context.Context, job schedulerpkg.ScheduledJob) (jobErr error) {
	// This is the bridge's only entry point into the scheduler's worker
	// goroutines, so the guard belongs here rather than at any one call site: a
	// panic becomes this job's error return, which the scheduler records as a
	// failed job run and retries under its own policy.
	defer b.recoverBridgeGoroutine("scheduled_job", func(recovered error) {
		jobErr = recovered
		_ = b.appendScheduleAudit(context.Background(), domain.ActorSystem, "job.failed", job.Schedule.ID, job.Run.ID)
	})
	payload, err := decodeScheduledPayload(job.Schedule.PayloadJSON)
	if err != nil {
		return err
	}
	if err := b.appendScheduleAudit(ctx, domain.ActorSystem, "job.started", job.Schedule.ID, job.Run.ID); err != nil {
		return err
	}
	if payload.Kind == "notification" && payload.Notification != nil {
		return b.executeNotificationJob(ctx, job, *payload.Notification)
	}
	if payload.Kind != "agent_task" || strings.TrimSpace(payload.Prompt) == "" {
		return fmt.Errorf("unsupported scheduled payload kind %q", payload.Kind)
	}
	maxSteps := job.Schedule.Budget.MaxToolCalls + 2
	if maxSteps < 2 {
		maxSteps = 2
	}
	agentID := b.personaProfileID()
	if agentID.Empty() {
		return fmt.Errorf("%w: active agent is required", domain.ErrInvalidArgument)
	}
	conversationID := b.scheduleConversationID(ctx, job.Schedule.ID, agentID)
	result, runErr := b.sendMessageContextWithBudget(ctx, ChatRequest{
		ConversationID: string(conversationID), Text: payload.Prompt,
	}, domain.RunKindBackground, domain.RunBudget{
		MaxSteps: maxSteps, MaxTokens: job.Schedule.Budget.MaxTokens, MaxToolCalls: job.Schedule.Budget.MaxToolCalls,
		MaxToolOutputBytes: 256 * 1024, MaxDurationSeconds: job.Schedule.Budget.MaxDurationSeconds,
	})
	if runErr != nil {
		_ = b.appendScheduleAudit(context.Background(), domain.ActorSystem, "job.failed", job.Schedule.ID, job.Run.ID)
		return runErr
	}
	if result.Status != "complete" {
		_ = b.appendScheduleAudit(context.Background(), domain.ActorSystem, "job.failed", job.Schedule.ID, job.Run.ID)
		return errors.New("scheduled agent run did not complete")
	}
	if payload.DeliveryChannel == "notification" {
		notificationID := domain.ID("notification_" + string(job.Run.ID))
		notification := domain.Notification{
			ID: notificationID, Type: domain.NotificationTypeTaskCompleted,
			Title: "Yuri завершила задачу", Body: job.Schedule.Name,
			Source:    domain.NotificationSource{Kind: domain.NotificationSourceSchedule, ID: string(job.Schedule.ID), Label: job.Schedule.Name, Reason: "scheduled background task completed"},
			CreatedAt: time.Now().UTC(), ConversationID: conversationID, DeepLink: "yuri://tasks/" + string(job.Schedule.ID),
		}
		// The agent task has already completed. A notification adapter failure
		// must not retry the model/tool run and duplicate its side effects.
		_ = b.deliverOrDeferNotification(ctx, notification)
	}
	if err := b.appendScheduleAudit(ctx, domain.ActorSystem, "job.completed", job.Schedule.ID, job.Run.ID); err != nil {
		if b.logger != nil {
			b.logger.ErrorContext(ctx, "append completed job audit", "job_run_id", job.Run.ID, "error", err)
		}
	}
	return nil
}

// scheduleConversationID resolves the conversation a scheduled agent task
// writes into. Conversations are owned by exactly one agent, and a run always
// executes under the currently active agent, so the historical agent-agnostic
// ID ("schedule_<schedule>") permanently broke a schedule once the owner
// switched agents: the ownership check in ensureConversation rejected the
// foreign conversation on every later occurrence. Namespacing the ID by the
// executing agent keeps each agent's scheduled history separate and makes the
// failure impossible. A pre-existing legacy conversation is reused while it
// still belongs to the executing agent so no visible history is orphaned.
func (b *Bridge) scheduleConversationID(ctx context.Context, scheduleID, agentID domain.ID) domain.ID {
	legacyID := domain.ID("schedule_" + string(scheduleID))
	if agentID.Empty() || b.repositories == nil || b.repositories.Conversations == nil {
		return legacyID
	}
	if conversation, err := b.repositories.Conversations.Get(ctx, legacyID); err == nil && conversation.AgentID == agentID {
		return legacyID
	}
	return domain.ID("schedule_" + string(agentID) + "_" + string(scheduleID))
}

func (b *Bridge) executeNotificationJob(ctx context.Context, job schedulerpkg.ScheduledJob, notification domain.Notification) error {
	if err := b.deliverOrDeferNotification(ctx, notification); err != nil {
		return err
	}
	if err := b.appendScheduleAudit(ctx, domain.ActorSystem, "job.completed", job.Schedule.ID, job.Run.ID); err != nil {
		if b.logger != nil {
			b.logger.ErrorContext(ctx, "append completed notification job audit", "job_run_id", job.Run.ID, "error", err)
		}
	}
	return nil
}

func (b *Bridge) deliverOrDeferNotification(ctx context.Context, notification domain.Notification) error {
	decision, err := b.deliverNotification(ctx, notification)
	if err != nil {
		return err
	}
	if !decision.Deferred() || decision.DeliverAt.IsZero() {
		return nil
	}
	return b.enqueueDeferredNotification(ctx, notification, decision.DeliverAt)
}

func (b *Bridge) enqueueDeferredNotification(ctx context.Context, notification domain.Notification, at time.Time) error {
	digest := sha256.Sum256([]byte(string(notification.ID) + "\x00" + at.UTC().Format(time.RFC3339Nano)))
	id := domain.ID(fmt.Sprintf("schedule_deferred_%x", digest[:16]))
	payload := scheduledPayload{Kind: "notification", Internal: true, Notification: &notification}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	err = b.scheduler.CreateSchedule(ctx, domain.Schedule{
		ID: id, Name: "Deferred notification", Kind: domain.ScheduleKindOnce, Timezone: "UTC", StartAt: at.UTC(),
		PayloadJSON: string(encoded), Status: domain.ScheduleStatusActive, Enabled: true,
		MisfirePolicy: domain.MisfireRunOnce, Retry: domain.RetryPolicy{MaxAttempts: 3, InitialBackoffSecond: 10, MaxBackoffSecond: 300},
		Budget: domain.JobBudget{MaxDurationSeconds: 30}, HistoryLimit: 10, CreatedAt: now, UpdatedAt: now, Version: 1,
	})
	if errors.Is(err, domain.ErrConflict) {
		return nil
	}
	return err
}

func (b *Bridge) runSchedulerSoon() {
	b.mu.Lock()
	worker := b.scheduler
	backgroundCtx := b.backgroundCtx
	shuttingDown := b.shuttingDown
	if worker == nil || shuttingDown {
		b.mu.Unlock()
		return
	}
	if backgroundCtx == nil {
		backgroundCtx = context.Background()
	}
	b.background.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.background.Done()
		// Job execution itself is already guarded in executeScheduledJob, which
		// fails the individual job run. What is left here is a panic in the
		// scheduler's own claim/lease bookkeeping: it owns no bridge-side run to
		// fail, and any claim it left behind is reclaimed by RecoverExpiredLeases
		// on the next cycle, so this guard reports through the log.
		defer b.recoverBridgeGoroutine("scheduler_run_due", nil)
		_, _ = worker.RunDue(backgroundCtx)
	}()
}

func decodeScheduledPayload(raw string) (scheduledPayload, error) {
	var payload scheduledPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return scheduledPayload{}, fmt.Errorf("decode scheduled payload: %w", err)
	}
	return payload, nil
}

func scheduleView(item domain.Schedule, payload scheduledPayload) ScheduleView {
	view := ScheduleView{
		ID: string(item.ID), Title: item.Name, Prompt: payload.Prompt, Type: string(item.Kind),
		IntervalSeconds: item.IntervalSeconds, Expression: item.Expression, Timezone: item.Timezone,
		MisfirePolicy: string(item.MisfirePolicy), Enabled: item.Enabled, Status: string(item.Status),
		DeliveryChannel: payload.DeliveryChannel,
		Budget:          ScheduleBudgetView{MaxDurationSeconds: item.Budget.MaxDurationSeconds, MaxTokens: item.Budget.MaxTokens, MaxToolCalls: item.Budget.MaxToolCalls},
		CreatedAt:       item.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: item.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if item.Kind == domain.ScheduleKindOnce {
		view.RunAt = item.StartAt.UTC().Format(time.RFC3339Nano)
	}
	if !item.NextRunAt.IsZero() {
		view.NextRunAt = item.NextRunAt.UTC().Format(time.RFC3339Nano)
	}
	if !item.LastRunAt.IsZero() {
		view.LastRunAt = item.LastRunAt.UTC().Format(time.RFC3339Nano)
	}
	if view.DeliveryChannel == "" {
		view.DeliveryChannel = "in_app"
	}
	return view
}

func jobRunView(run domain.JobRun, title string) JobRunView {
	status := string(run.State)
	if run.State == domain.JobRunSucceeded {
		status = "completed"
	}
	view := JobRunView{
		ID: string(run.ID), ScheduleID: string(run.ScheduleID), ScheduleTitle: title,
		Status: status, Attempt: run.Attempt, Error: run.Error, Summary: run.ResultRef,
		TriggeredBy: string(run.Trigger),
	}
	if !run.StartedAt.IsZero() {
		view.StartedAt = run.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !run.FinishedAt.IsZero() {
		view.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	if !run.StartedAt.IsZero() && !run.FinishedAt.IsZero() {
		view.DurationMS = run.FinishedAt.Sub(run.StartedAt).Milliseconds()
	}
	return view
}

func scheduleInputID(input ScheduleIDInput) domain.ID {
	return domain.ID(strings.TrimSpace(firstNonEmpty(input.ScheduleID, input.ID)))
}

func (b *Bridge) appendScheduleAudit(ctx context.Context, actor domain.Actor, action string, scheduleID, jobRunID domain.ID) error {
	id, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"schedule_id": string(scheduleID), "job_run_id": string(jobRunID)})
	return b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: id, Actor: actor, Action: action, Target: string(scheduleID),
		Decision: domain.PermissionAllow, PayloadRedacted: string(payload), CreatedAt: time.Now().UTC(),
	})
}

func (b *Bridge) recordScheduleAudit(ctx context.Context, actor domain.Actor, action string, scheduleID, jobRunID domain.ID) {
	if err := b.appendScheduleAudit(ctx, actor, action, scheduleID, jobRunID); err != nil {
		// The schedule mutation has already committed. Reporting the whole
		// operation as failed would invite a retry and duplicate the task/run.
		if b.logger != nil {
			b.logger.ErrorContext(ctx, "append schedule audit", "action", action, "schedule_id", scheduleID, "error", err)
		}
	}
}
