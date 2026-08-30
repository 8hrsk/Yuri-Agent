package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	builtintools "github.com/OrdoAI/yuri-agent/internal/tools"
)

func (emitter *chatEmitter) createToolRecord(ctx context.Context, event agent.Event) error {
	if event.ToolCall == nil {
		return nil
	}
	if _, exists := emitter.toolRecords[event.ToolCall.ID]; exists {
		return nil
	}
	id, err := domain.NewID("tool")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	risk := domain.RiskLow
	if emitter.tools != nil {
		if tool, ok := emitter.tools.Get(event.ToolCall.Name); ok {
			risk = tool.Descriptor().Risk
		}
	}
	record := storage.ToolCall{
		ID: id, RunID: domain.ID(emitter.runID), ToolID: event.ToolCall.Name,
		ArgsRedacted: redactedToolArguments(event.ToolCall.Name, event.ToolCall.Arguments, 4096), Risk: risk,
		Status: storage.ToolCallPending, IdempotencyKey: event.ToolCall.ID,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	record.ApprovalID = emitter.approvalRecords[event.ToolCall.ID]
	if err := emitter.b.repositories.ToolCalls.Create(ctx, record); err != nil {
		return err
	}
	emitter.toolRecords[event.ToolCall.ID] = record
	auditID, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	decision := domain.PermissionAllow
	if risk == domain.RiskMedium || risk == domain.RiskHigh {
		decision = domain.PermissionNeedsApproval
	}
	return emitter.b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: auditID, RunID: domain.ID(emitter.runID), ToolCallID: record.ID,
		Actor: domain.ActorAgent, Action: "tool.proposed", Target: event.ToolCall.Name,
		Decision: decision, PayloadRedacted: record.ArgsRedacted, CreatedAt: now,
	})
}

func (emitter *chatEmitter) startToolRecord(ctx context.Context, event agent.Event) error {
	if event.ToolCall == nil {
		return nil
	}
	if err := emitter.createToolRecord(ctx, event); err != nil {
		return err
	}
	record, exists := emitter.toolRecords[event.ToolCall.ID]
	if !exists {
		return nil
	}
	record.Status = storage.ToolCallRunning
	record.Version++
	record.UpdatedAt = time.Now().UTC()
	if err := emitter.b.repositories.ToolCalls.Save(ctx, record); err != nil {
		return err
	}
	emitter.toolRecords[event.ToolCall.ID] = record
	auditID, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	return emitter.b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: auditID, RunID: domain.ID(emitter.runID), ToolCallID: record.ID, ApprovalID: record.ApprovalID,
		Actor: domain.ActorAgent, Action: "tool.execute", Target: event.ToolCall.Name,
		Decision: domain.PermissionAllow, PayloadRedacted: record.ArgsRedacted, CreatedAt: record.UpdatedAt,
	})
}

func redactedToolArguments(toolID string, arguments json.RawMessage, maxBytes int) string {
	if toolID == delegationToolID {
		return redactedDelegationArguments(arguments, maxBytes)
	}
	if toolID == peerDialogueToolID {
		return redactedPeerDialogueArguments(arguments, maxBytes)
	}
	if toolID != builtintools.FilesystemWriteToolID {
		return boundedJSONObject(arguments, maxBytes)
	}
	var value map[string]any
	if json.Unmarshal(arguments, &value) != nil || value == nil {
		return "{}"
	}
	if content, ok := value["content"].(string); ok {
		digest := sha256.Sum256([]byte(content))
		value["content"] = fmt.Sprintf("[redacted %d bytes]", len(content))
		value["content_sha256"] = hex.EncodeToString(digest[:])
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return boundedJSONObject(encoded, maxBytes)
}

func (emitter *chatEmitter) applyToolRisk(view *ToolCallView) {
	if view == nil || emitter.tools == nil {
		return
	}
	if tool, ok := emitter.tools.Get(view.Name); ok {
		view.Risk = string(tool.Descriptor().Risk)
	}
}

func (emitter *chatEmitter) finishToolRecord(ctx context.Context, event agent.Event, status string) error {
	if event.ToolCall == nil {
		return nil
	}
	record, exists := emitter.toolRecords[event.ToolCall.ID]
	if !exists {
		return nil
	}
	record.Status = storage.ToolCallSucceeded
	if status == "failed" {
		record.Status = storage.ToolCallFailed
	} else if status == "denied" {
		record.Status = storage.ToolCallDenied
	}
	if event.ToolResult != nil && event.ToolResult.Metadata != nil {
		if delegationID, ok := event.ToolResult.Metadata["delegation_id"].(string); ok && strings.TrimSpace(delegationID) != "" {
			record.ResultRef = "delegation:" + truncateRunes(strings.TrimSpace(delegationID), 200)
		}
		if dialogueID, ok := event.ToolResult.Metadata["dialogue_id"].(string); ok && strings.TrimSpace(dialogueID) != "" {
			record.ResultRef = "peer-dialogue:" + truncateRunes(strings.TrimSpace(dialogueID), 200)
		}
	}
	record.Version++
	record.UpdatedAt = time.Now().UTC()
	if err := emitter.b.repositories.ToolCalls.Save(ctx, record); err != nil {
		return err
	}
	auditID, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	decision := domain.PermissionAllow
	if status == "failed" || status == "denied" {
		decision = domain.PermissionDeny
	}
	payload, _ := json.Marshal(map[string]string{"status": record.Status})
	return emitter.b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: auditID, RunID: record.RunID, ToolCallID: record.ID, ApprovalID: record.ApprovalID,
		Actor: domain.ActorSystem, Action: "tool.completed", Target: record.ToolID,
		Decision: decision, PayloadRedacted: string(payload), CreatedAt: record.UpdatedAt,
	})
}

func toolCallView(call *agent.ToolCall, status, result string, now time.Time) *ToolCallView {
	if call == nil {
		return nil
	}
	args := make(map[string]any)
	_ = json.Unmarshal([]byte(redactedToolArguments(call.Name, call.Arguments, 4096)), &args)
	view := &ToolCallView{ID: call.ID, Name: call.Name, Label: toolLabel(call.Name), Risk: string(domain.RiskLow), Status: status, Args: args, Result: result}
	if status == "running" || status == "pending" {
		view.StartedAt = now.Format(time.RFC3339Nano)
	} else {
		view.FinishedAt = now.Format(time.RFC3339Nano)
	}
	return view
}

func toolLabel(name string) string {
	switch name {
	case builtintools.FilesystemReadToolID:
		return "Работа с файлами"
	case builtintools.FilesystemWriteToolID:
		return "Изменение файла"
	case scheduleCreateToolID:
		return "Создание задачи"
	case delegationToolID:
		return "Субагент"
	case peerDialogueToolID:
		return "Диалог с агентом"
	default:
		if strings.TrimSpace(name) == "" {
			return "Инструмент"
		}
		return name
	}
}
