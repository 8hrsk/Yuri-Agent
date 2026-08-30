package desktop

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// assistantDeltaFlushInterval bounds how long a text delta waits before it
// reaches the renderer. A model streams tens of deltas per second and each one
// used to cost a separate JSON serialization plus an IPC hop; coalescing them
// on a short timer keeps the answer visibly incremental while cutting bridge
// traffic by one to two orders of magnitude.
const assistantDeltaFlushInterval = 40 * time.Millisecond

const (
	assistantDeltaEventType = "assistant.delta"
	runCompletedEventType   = "run.completed"
)

type chatEmitter struct {
	b              *Bridge
	conversationID string
	runID          string
	messageID      string

	// deliver observes every dispatched event. Production leaves it nil and
	// delivery goes through the Wails runtime; it is assigned once, before the
	// run starts, so it is never written concurrently with a dispatch.
	deliver func(ChatEvent)

	// dispatchMu serializes delivery to the renderer. It is always acquired
	// before mu and never while mu is held. Taking the pending delta batch and
	// sending it under one dispatchMu critical section is what guarantees that
	// a timer flush can neither overtake nor be overtaken by a non-delta event.
	dispatchMu sync.Mutex

	mu              sync.Mutex
	events          []ChatEvent
	segments        []*assistantSegment
	activeSegment   int
	pendingDelta    strings.Builder
	pendingEvent    ChatEvent
	flushTimer      *time.Timer
	closed          bool
	terminalEmitted bool
	toolRecords     map[string]storage.ToolCall
	approvalRecords map[string]domain.ID
	tools           *agent.ToolRegistry
}

// assistantSegment accumulates one assistant message through a strings.Builder.
// The previous `Content += delta` reallocated and copied the whole message on
// every token, which is quadratic in the length of the answer.
type assistantSegment struct {
	id         string
	responseID string
	content    strings.Builder
	createdAt  time.Time
}

// assistantMessageSegment is the materialized form handed to the persistence
// path once the run is finished.
type assistantMessageSegment struct {
	ID         string
	ResponseID string
	Content    string
	CreatedAt  time.Time
}

func newChatEmitter(b *Bridge, conversationID, runID, messageID string) *chatEmitter {
	return &chatEmitter{
		b: b, conversationID: conversationID, runID: runID, messageID: messageID,
		activeSegment: -1,
		toolRecords:   make(map[string]storage.ToolCall), approvalRecords: make(map[string]domain.ID),
	}
}

func (emitter *chatEmitter) Sink(ctx context.Context, event agent.Event) error {
	now := time.Now().UTC()
	view := ChatEvent{ConversationID: emitter.conversationID, RunID: emitter.runID, CreatedAt: now.Format(time.RFC3339Nano)}
	switch event.Type {
	case agent.EventRunStarted:
		view.Type = "run.started"
	case agent.EventModelTextDelta:
		messageID, completedID, err := emitter.appendAssistantDelta(event.ResponseID, event.Text, now)
		if err != nil {
			return err
		}
		if completedID != "" {
			emitter.emit(ChatEvent{
				Type: "assistant.completed", ConversationID: emitter.conversationID,
				RunID: emitter.runID, MessageID: completedID, CreatedAt: view.CreatedAt,
			})
		}
		view.Type, view.MessageID, view.Delta = "assistant.delta", messageID, event.Text
	case agent.EventToolCallStarted:
		if completedID := emitter.closeAssistantSegment(); completedID != "" {
			emitter.emit(ChatEvent{
				Type: "assistant.completed", ConversationID: emitter.conversationID,
				RunID: emitter.runID, MessageID: completedID, CreatedAt: view.CreatedAt,
			})
		}
		view.Type = "tool.started"
		view.ToolCall = toolCallView(event.ToolCall, "pending", "", now)
		emitter.applyToolRisk(view.ToolCall)
		if err := emitter.createToolRecord(ctx, event); err != nil {
			return err
		}
	case agent.EventToolStarted:
		view.Type = "tool.updated"
		view.ToolCall = toolCallView(event.ToolCall, "running", "", now)
		emitter.applyToolRisk(view.ToolCall)
		if err := emitter.startToolRecord(ctx, event); err != nil {
			return err
		}
	case agent.EventToolCompleted:
		view.Type = "tool.updated"
		result := ""
		status := "completed"
		if event.ToolResult != nil {
			result = event.ToolResult.Content
			if decision, _ := event.ToolResult.Metadata["decision"].(string); decision == "denied" {
				status = "denied"
			}
			if event.ToolResult.IsError {
				if status != "denied" {
					status = "failed"
				}
			}
		}
		view.ToolCall = toolCallView(event.ToolCall, status, truncateRunes(result, 4096), now)
		emitter.applyToolRisk(view.ToolCall)
		if err := emitter.finishToolRecord(ctx, event, status); err != nil {
			return err
		}
	case agent.EventToolApprovalNeeded:
		approval, err := emitter.createApproval(ctx, event)
		if err != nil {
			return err
		}
		view.Type, view.Approval = "approval.required", approval
		view.Status, view.Label = "waiting_approval", "Ожидается разрешение пользователя"
	case agent.EventRunCompleted:
		if completedID := emitter.closeAssistantSegment(); completedID != "" {
			emitter.emit(ChatEvent{
				Type: "assistant.completed", ConversationID: emitter.conversationID,
				RunID: emitter.runID, MessageID: completedID, CreatedAt: view.CreatedAt,
			})
		}
		return nil
	case agent.EventRunFailed:
		if completedID := emitter.closeAssistantSegment(); completedID != "" {
			emitter.emit(ChatEvent{
				Type: "assistant.completed", ConversationID: emitter.conversationID,
				RunID: emitter.runID, MessageID: completedID, CreatedAt: view.CreatedAt,
			})
		}
		// A cancelled run must not be reported as a failure: the renderer
		// finalizes an interrupted message differently from an errored one.
		view.Type, view.Status, view.Error = runCompletedEventType, "error", safeError(event.Error)
		if event.Status == agent.RunStatusCancelled {
			view.Status, view.Error = "cancelled", "Запуск остановлен"
		}
		emitter.emitTerminal(view)
		return nil
	default:
		return nil
	}
	emitter.emit(view)
	return nil
}

func (emitter *chatEmitter) appendAssistantDelta(responseID, delta string, now time.Time) (string, string, error) {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	completedID := ""
	if emitter.activeSegment >= 0 {
		active := emitter.segments[emitter.activeSegment]
		if responseID != "" && active.responseID != "" && responseID != active.responseID {
			completedID = active.id
			emitter.activeSegment = -1
		}
	}
	if emitter.activeSegment < 0 {
		messageID := emitter.messageID
		if len(emitter.segments) > 0 {
			id, err := domain.NewID("message")
			if err != nil {
				return "", "", err
			}
			messageID = string(id)
		}
		emitter.segments = append(emitter.segments, &assistantSegment{
			id: messageID, responseID: responseID, createdAt: now,
		})
		emitter.activeSegment = len(emitter.segments) - 1
	}
	active := emitter.segments[emitter.activeSegment]
	active.content.WriteString(delta)
	return active.id, completedID, nil
}

func (emitter *chatEmitter) closeAssistantSegment() string {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if emitter.activeSegment < 0 {
		return ""
	}
	completedID := emitter.segments[emitter.activeSegment].id
	emitter.activeSegment = -1
	return completedID
}

// AssistantSegments materializes the accumulated segments for the persistence
// path. The builders stay owned by the emitter, so this is safe to call while
// the run is still streaming.
func (emitter *chatEmitter) AssistantSegments() []assistantMessageSegment {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	segments := make([]assistantMessageSegment, 0, len(emitter.segments))
	for _, segment := range emitter.segments {
		segments = append(segments, assistantMessageSegment{
			ID: segment.id, ResponseID: segment.responseID,
			Content: segment.content.String(), CreatedAt: segment.createdAt,
		})
	}
	return segments
}
