package desktop

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const scheduleCreateToolID = "scheduler.create"

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
				"intervalSeconds":{"type":"integer","minimum":60},
				"expression":{"type":"string","description":"Standard five-field CRON expression"},
				"timezone":{"type":"string","description":"IANA timezone"},
				"misfirePolicy":{"type":"string","enum":["skip","run_once"]},
				"deliveryChannel":{"type":"string","enum":["in_app","notification"]},
				"budget":{"type":"object","properties":{"maxDurationSeconds":{"type":"integer","minimum":1},"maxTokens":{"type":"integer","minimum":1},"maxToolCalls":{"type":"integer","minimum":1}}}
			},
			"required":["title","prompt","type","timezone","misfirePolicy"]
		}`),
	}
}

func (tool scheduleAgentTool) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	if tool.bridge == nil || tool.bridge.scheduler == nil {
		return agent.ToolResult{}, fmt.Errorf("scheduler is unavailable")
	}
	var input ScheduleInput
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		return agent.ToolResult{}, fmt.Errorf("decode schedule request: %w", err)
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
