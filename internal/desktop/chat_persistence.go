package desktop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func (b *Bridge) failChatRun(ctx context.Context, run *domain.AgentRun, emitter *chatEmitter, cause error) ChatRunResult {
	status := "error"
	message := safeError(cause.Error())
	// Provider adapters flatten a cancelled transport error into a message, so
	// the run context is the authoritative signal for an owner interruption.
	if errors.Is(cause, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		status, message = "cancelled", "Запуск остановлен"
	}
	if run != nil && !run.State.Terminal() {
		if status == "cancelled" {
			if run.State == domain.RunStateRunning || run.State == domain.RunStateWaitingApproval {
				_ = transitionAndSave(context.Background(), b.repositories.Runs, run, domain.RunStateCancelling)
			}
			_ = transitionAndSave(context.Background(), b.repositories.Runs, run, domain.RunStateCancelled)
		} else if run.State == domain.RunStateRunning || run.State == domain.RunStateWaitingApproval {
			candidate := *run
			if candidate.Fail(message, time.Now().UTC()) == nil && b.repositories.Runs.Save(context.Background(), candidate) == nil {
				*run = candidate
			}
		}
	}
	if completedID := emitter.closeAssistantSegment(); completedID != "" {
		emitter.emit(ChatEvent{
			Type: "assistant.completed", ConversationID: emitter.conversationID,
			RunID: emitter.runID, MessageID: completedID,
		})
	}
	emitter.emitTerminal(ChatEvent{Type: runCompletedEventType, ConversationID: emitter.conversationID, RunID: emitter.runID, Status: status, Error: message})
	return ChatRunResult{RunID: emitter.runID, Status: status, Events: emitter.Events()}
}

func (b *Bridge) ensureConversation(ctx context.Context, id domain.ID, text string, now time.Time, agentIDs ...domain.ID) error {
	agentID := b.personaProfileID()
	if len(agentIDs) > 0 {
		agentID = agentIDs[0]
	}
	if agentID.Empty() {
		return fmt.Errorf("%w: active agent is required", domain.ErrInvalidArgument)
	}
	conversation, err := b.repositories.Conversations.Get(ctx, id)
	if err == nil {
		if conversation.AgentID != agentID {
			return errors.New("conversation does not belong to the active agent")
		}
		conversation.UpdatedAt = now
		return b.repositories.Conversations.Save(ctx, conversation)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return b.repositories.Conversations.Create(ctx, storage.Conversation{
		ID: id, AgentID: agentID, Title: storage.DefaultConversationTitle,
		TitleSource: storage.ConversationTitleSourceDefault, CreatedAt: now, UpdatedAt: now,
	})
}

func (b *Bridge) touchConversation(ctx context.Context, id domain.ID, now time.Time, agentIDs ...domain.ID) error {
	agentID := b.personaProfileID()
	if len(agentIDs) > 0 {
		agentID = agentIDs[0]
	}
	if agentID.Empty() {
		return fmt.Errorf("%w: active agent is required", domain.ErrInvalidArgument)
	}
	conversation, err := b.repositories.Conversations.Get(ctx, id)
	if err != nil {
		return err
	}
	if conversation.AgentID != agentID {
		return errors.New("conversation does not belong to the active agent")
	}
	conversation.UpdatedAt = now
	return b.repositories.Conversations.Save(ctx, conversation)
}

func transitionAndSave(ctx context.Context, repository *storage.RunRepository, run *domain.AgentRun, next domain.RunState) error {
	candidate := *run
	if err := candidate.Transition(next, time.Now().UTC()); err != nil {
		return err
	}
	if err := repository.Save(ctx, candidate); err != nil {
		return err
	}
	*run = candidate
	return nil
}
