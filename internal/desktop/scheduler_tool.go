package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const scheduleCreateToolID = "scheduler.create"

// Server-side bounds for an agent-proposed schedule. The JSON schema in the
// descriptor is advisory: a model can emit anything, so the same limits are
// enforced here before a durable task is ever persisted.
const (
	minScheduleIntervalSeconds  int64 = 60
	maxScheduleIntervalSeconds  int64 = 366 * 24 * 60 * 60
	maxScheduleDurationSeconds  int   = 900
	maxScheduleTokens           int64 = 20_000
	maxScheduleToolCalls        int   = 32
	maxScheduleRunAtHorizon           = 366 * 24 * time.Hour
	maxScheduleTitleLength            = 200
	maxSchedulePromptLength           = 4000
	maxScheduleExpressionLength       = 200
)

// ToolApprovalScoper lets a tool describe the concrete effect of one specific
// call. The approval card can then show the interval and budget the owner is
// actually approving instead of a generic capability list.
type ToolApprovalScoper interface {
	ApprovalScope(arguments json.RawMessage) (string, bool)
}

var _ ToolApprovalScoper = scheduleAgentTool{}

// scheduleAgentTool lets Yuri propose a durable task from natural language.
// Its medium risk ensures the interpreted schedule is shown in the existing
// approval UI before anything is persisted.
type scheduleAgentTool struct {
	bridge *Bridge
}

func (tool scheduleAgentTool) Descriptor() agent.ToolDescriptor {
	return agent.ToolDescriptor{
		Name:        scheduleCreateToolID,
		Description: "Create a durable one-shot, interval, or standard five-field CRON task after owner approval.",
		Risk:        domain.RiskMedium, Capabilities: domain.CapabilitySet{domain.CapabilitySchedulerManage},
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"title":{"type":"string"},
				"prompt":{"type":"string"},
				"type":{"type":"string","enum":["once","interval","cron"]},
				"runAt":{"type":"string","description":"RFC3339 timestamp for one-shot tasks"},
				"intervalSeconds":{"type":"integer","minimum":60,"maximum":31622400},
				"expression":{"type":"string","description":"Standard five-field CRON expression"},
				"timezone":{"type":"string","description":"IANA timezone"},
				"misfirePolicy":{"type":"string","enum":["skip","run_once"]},
				"deliveryChannel":{"type":"string","enum":["in_app","notification"]},
				"budget":{"type":"object","properties":{"maxDurationSeconds":{"type":"integer","minimum":1,"maximum":900},"maxTokens":{"type":"integer","minimum":1,"maximum":20000},"maxToolCalls":{"type":"integer","minimum":1,"maximum":32}}}
			},
			"required":["title","prompt","type","timezone","misfirePolicy"]
		}`),
	}
}

func (tool scheduleAgentTool) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	if tool.bridge == nil || tool.bridge.scheduler == nil {
		return agent.ToolResult{}, fmt.Errorf("scheduler is unavailable")
	}
	input, err := decodeScheduleToolInput(call.Arguments)
	if err != nil {
		return agent.ToolResult{}, err
	}
	view, err := tool.bridge.createSchedule(ctx, input, domain.ActorAgent)
	if err != nil {
		return agent.ToolResult{}, err
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("encode schedule result: %w", err)
	}
	return agent.ToolResult{Content: string(encoded)}, nil
}

// ApprovalScope renders the interval, delivery channel and budget of the
// proposed task. A generic "execute tool scheduler.create" hides exactly the
// two numbers that decide how expensive the approval is.
func (tool scheduleAgentTool) ApprovalScope(arguments json.RawMessage) (string, bool) {
	input, err := decodeScheduleToolInput(arguments)
	if err != nil {
		return "", false
	}
	return describeScheduleScope(input), true
}

func describeScheduleScope(input ScheduleInput) string {
	parts := make([]string, 0, 6)
	switch strings.TrimSpace(input.Type) {
	case string(domain.ScheduleKindOnce):
		parts = append(parts, "однократно "+strings.TrimSpace(input.RunAt))
	case string(domain.ScheduleKindInterval):
		parts = append(parts, fmt.Sprintf("каждые %d с", input.IntervalSeconds))
	case string(domain.ScheduleKindCron):
		parts = append(parts, "CRON "+strings.TrimSpace(input.Expression))
	}
	parts = append(parts, strings.TrimSpace(input.Timezone))
	delivery := strings.TrimSpace(input.DeliveryChannel)
	if delivery == "" {
		delivery = "in_app"
	}
	parts = append(parts, delivery)
	budget := effectiveScheduleBudget(input.Budget)
	parts = append(parts, fmt.Sprintf("бюджет %d с · %d токенов · %d вызовов",
		budget.MaxDurationSeconds, budget.MaxTokens, budget.MaxToolCalls))
	return strings.Join(parts, " · ")
}

// effectiveScheduleBudget mirrors the defaults scheduleFromInput applies, so
// the approval card never shows "0" where a default will be substituted.
func effectiveScheduleBudget(budget ScheduleBudgetView) ScheduleBudgetView {
	if budget.MaxDurationSeconds == 0 {
		budget.MaxDurationSeconds = 180
	}
	if budget.MaxTokens == 0 {
		budget.MaxTokens = 1_800
	}
	if budget.MaxToolCalls == 0 {
		budget.MaxToolCalls = 8
	}
	return budget
}

// decodeScheduleToolInput parses and fully validates an agent-proposed
// schedule. Unknown members are rejected so a model cannot smuggle a field the
// descriptor never advertised.
func decodeScheduleToolInput(arguments json.RawMessage) (ScheduleInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var input ScheduleInput
	if err := decoder.Decode(&input); err != nil {
		return ScheduleInput{}, fmt.Errorf("decode schedule request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ScheduleInput{}, errors.New("schedule request contains trailing JSON")
	}
	if err := validateScheduleToolInput(&input); err != nil {
		return ScheduleInput{}, err
	}
	return input, nil
}

func validateScheduleToolInput(input *ScheduleInput) error {
	if strings.TrimSpace(input.ID) != "" {
		return errors.New("scheduler.create cannot target an existing schedule id")
	}
	if input.Enabled != nil {
		return errors.New("scheduler.create cannot set the enabled flag")
	}
	title := strings.TrimSpace(input.Title)
	prompt := strings.TrimSpace(input.Prompt)
	if title == "" || prompt == "" {
		return errors.New("schedule title and prompt are required")
	}
	if len(title) > maxScheduleTitleLength {
		return fmt.Errorf("schedule title exceeds %d characters", maxScheduleTitleLength)
	}
	if len(prompt) > maxSchedulePromptLength {
		return fmt.Errorf("schedule prompt exceeds %d characters", maxSchedulePromptLength)
	}
	switch strings.TrimSpace(input.MisfirePolicy) {
	case string(domain.MisfireSkip), string(domain.MisfireRunOnce):
	default:
		return fmt.Errorf("unsupported misfire policy %q", input.MisfirePolicy)
	}
	switch strings.TrimSpace(input.DeliveryChannel) {
	case "", "in_app", "notification":
	default:
		return fmt.Errorf("unsupported delivery channel %q", input.DeliveryChannel)
	}
	timezone := strings.TrimSpace(input.Timezone)
	if timezone == "" {
		return errors.New("schedule timezone is required")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return fmt.Errorf("unknown timezone %q", timezone)
	}
	if err := validateScheduleBudget(input.Budget); err != nil {
		return err
	}
	switch strings.TrimSpace(input.Type) {
	case string(domain.ScheduleKindOnce):
		runAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(input.RunAt))
		if err != nil {
			return fmt.Errorf("parse runAt: %w", err)
		}
		now := time.Now().UTC()
		if !runAt.UTC().After(now) {
			return errors.New("runAt must be in the future")
		}
		if runAt.UTC().After(now.Add(maxScheduleRunAtHorizon)) {
			return errors.New("runAt is further than one year in the future")
		}
		if input.IntervalSeconds != 0 || strings.TrimSpace(input.Expression) != "" {
			return errors.New("one-shot schedules must not carry an interval or CRON expression")
		}
	case string(domain.ScheduleKindInterval):
		if input.IntervalSeconds < minScheduleIntervalSeconds {
			return fmt.Errorf("intervalSeconds must be at least %d", minScheduleIntervalSeconds)
		}
		if input.IntervalSeconds > maxScheduleIntervalSeconds {
			return fmt.Errorf("intervalSeconds must be at most %d", maxScheduleIntervalSeconds)
		}
		if strings.TrimSpace(input.Expression) != "" {
			return errors.New("interval schedules must not carry a CRON expression")
		}
	case string(domain.ScheduleKindCron):
		expression := strings.TrimSpace(input.Expression)
		if expression == "" {
			return errors.New("cron schedules require an expression")
		}
		if len(expression) > maxScheduleExpressionLength {
			return fmt.Errorf("cron expression exceeds %d characters", maxScheduleExpressionLength)
		}
		if len(strings.Fields(expression)) != 5 {
			return errors.New("cron expression must have five fields")
		}
		if input.IntervalSeconds != 0 {
			return errors.New("cron schedules must not carry an interval")
		}
	default:
		return fmt.Errorf("unsupported schedule type %q", input.Type)
	}
	return nil
}

func validateScheduleBudget(budget ScheduleBudgetView) error {
	if budget.MaxDurationSeconds < 0 || budget.MaxDurationSeconds > maxScheduleDurationSeconds {
		return fmt.Errorf("budget.maxDurationSeconds must be between 0 and %d", maxScheduleDurationSeconds)
	}
	if budget.MaxTokens < 0 || budget.MaxTokens > maxScheduleTokens {
		return fmt.Errorf("budget.maxTokens must be between 0 and %d", maxScheduleTokens)
	}
	if budget.MaxToolCalls < 0 || budget.MaxToolCalls > maxScheduleToolCalls {
		return fmt.Errorf("budget.maxToolCalls must be between 0 and %d", maxScheduleToolCalls)
	}
	return nil
}
