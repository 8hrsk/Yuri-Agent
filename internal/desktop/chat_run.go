package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	contextbuilder "github.com/OrdoAI/yuri-agent/internal/context"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// SendMessage performs one durable interactive run. Events are both emitted
// live through Wails and returned as a fallback for non-Wails/test clients.
func (b *Bridge) SendMessage(request ChatRequest) (ChatRunResult, error) {
	return b.sendMessage(request, domain.RunKindInteractive)
}

// sendMessage runs the common agent pipeline for foreground and durable
// background work. The run kind is persisted so scheduled executions remain
// distinguishable from owner-initiated chat turns in activity and recovery.
func (b *Bridge) sendMessage(request ChatRequest, runKind domain.RunKind) (ChatRunResult, error) {
	return b.sendMessageContext(nil, request, runKind)
}

func (b *Bridge) sendMessageContext(parent context.Context, request ChatRequest, runKind domain.RunKind) (ChatRunResult, error) {
	return b.sendMessageContextWithBudget(parent, request, runKind, domain.RunBudget{})
}

func (b *Bridge) sendMessageContextWithBudget(parent context.Context, request ChatRequest, runKind domain.RunKind, requestedBudget domain.RunBudget) (chatResult ChatRunResult, chatErr error) {
	// Outer guard. A panic raised before this run owns an emitter has no run to
	// fail and no renderer stream to close, so it becomes an ordinary error on
	// the calling path — a Wails IPC handler or a scheduler worker — instead of
	// killing the process and the owner's session with it. Once the emitter
	// exists the inner guard below takes over and runs first.
	defer b.recoverBridgeGoroutine("chat_run_setup", func(recovered error) {
		chatErr = recovered
	})
	if !runKind.Valid() {
		return ChatRunResult{}, errors.New("invalid run kind")
	}
	request.Text = strings.TrimSpace(request.Text)
	if request.Text == "" && len(request.Attachments) == 0 {
		return ChatRunResult{}, errors.New("message text or attachment is required")
	}
	if strings.TrimSpace(request.RetryOfMessageID) != "" && len(request.Attachments) > 0 {
		return ChatRunResult{}, errors.New("retry uses the attachments already stored on the original user turn")
	}
	// Capture ownership before any provider or background work starts. The
	// active agent may change while this run is in flight, but all durable and
	// derived state for the run must remain scoped to this one agent.
	agentID := b.personaProfileID()
	if agentID.Empty() {
		return ChatRunResult{}, fmt.Errorf("%w: active agent is required", domain.ErrInvalidArgument)
	}
	conversationID := domain.ID(strings.TrimSpace(request.ConversationID))
	if conversationID.Empty() {
		var err error
		conversationID, err = domain.NewID("conversation")
		if err != nil {
			return ChatRunResult{}, err
		}
	}

	if parent == nil {
		b.mu.RLock()
		parent = b.appCtx
		b.mu.RUnlock()
		if parent == nil {
			parent = context.Background()
		}
	}
	runContext, cancel := context.WithCancel(parent)
	defer cancel()

	now := time.Now().UTC()
	attachments, err := b.prepareChatAttachments(request.Attachments)
	if err != nil {
		return ChatRunResult{}, err
	}
	titleSeed := request.Text
	if titleSeed == "" {
		names := make([]string, 0, len(attachments))
		for _, item := range attachments {
			names = append(names, item.Name)
		}
		titleSeed = "Вложения: " + strings.Join(names, ", ")
	}
	if err := b.ensureConversation(runContext, conversationID, titleSeed, now, agentID); err != nil {
		return ChatRunResult{}, err
	}
	var userMessageID domain.ID
	if strings.TrimSpace(request.RetryOfMessageID) == "" {
		createdID, createErr := domain.NewID("message")
		if createErr != nil {
			return ChatRunResult{}, createErr
		}
		userMessageID = createdID
		providerMeta, metadataErr := attachmentMetadataJSON(attachments)
		if metadataErr != nil {
			return ChatRunResult{}, metadataErr
		}
		if err := b.repositories.Messages.Create(runContext, storage.Message{
			ID: userMessageID, ConversationID: conversationID, Role: string(agent.RoleUser),
			Content: request.Text, Status: "complete", ProviderMeta: providerMeta, CreatedAt: now,
		}); err != nil {
			return ChatRunResult{}, err
		}
	}

	runID, err := domain.NewID("run")
	if err != nil {
		return ChatRunResult{}, err
	}
	run, err := domain.NewRunForAgent(agentID, runID, runKind, conversationID, now)
	if err != nil {
		return ChatRunResult{}, err
	}
	run.Budget = domain.RunBudget{MaxSteps: 8, MaxTokens: 32_000, MaxToolCalls: 32, MaxToolOutputBytes: 256 * 1024, MaxDurationSeconds: 600}
	if requestedBudget.MaxSteps > 0 {
		run.Budget.MaxSteps = requestedBudget.MaxSteps
	}
	if requestedBudget.MaxTokens > 0 {
		run.Budget.MaxTokens = requestedBudget.MaxTokens
	}
	if requestedBudget.MaxToolCalls > 0 {
		run.Budget.MaxToolCalls = requestedBudget.MaxToolCalls
	}
	if requestedBudget.MaxToolOutputBytes > 0 {
		run.Budget.MaxToolOutputBytes = requestedBudget.MaxToolOutputBytes
	}
	if requestedBudget.MaxDurationSeconds > 0 {
		run.Budget.MaxDurationSeconds = requestedBudget.MaxDurationSeconds
	}
	if err := b.repositories.Runs.Create(runContext, run); err != nil {
		return ChatRunResult{}, err
	}
	if err := transitionAndSave(runContext, b.repositories.Runs, &run, domain.RunStateQueued); err != nil {
		return ChatRunResult{}, err
	}
	if err := transitionAndSave(runContext, b.repositories.Runs, &run, domain.RunStateRunning); err != nil {
		return ChatRunResult{}, err
	}

	b.mu.Lock()
	b.activeRuns[string(runID)] = cancel
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.activeRuns, string(runID))
		b.mu.Unlock()
	}()

	assistantMessageID, err := domain.NewID("message")
	if err != nil {
		return ChatRunResult{}, err
	}
	emitter := newChatEmitter(b, string(conversationID), string(runID), string(assistantMessageID))
	// Whatever ends this run — success, provider error, owner cancellation —
	// the renderer gets its pending deltas, a closed assistant segment and
	// exactly one terminal event.
	defer func() { emitter.close(runContext) }()
	// Inner guard, registered after the emitter so it unwinds before it. A
	// panic anywhere in the provider, the tool registry or a nested subagent is
	// reported through the same terminal path as any other run failure:
	// failChatRun moves the durable run out of `running` and emits the single
	// run.completed error event. Recovering without that call would leave the
	// owner staring at a run that merely stopped producing events, which is
	// indistinguishable from a hang.
	defer b.recoverBridgeGoroutine("chat_run", func(recovered error) {
		chatResult, chatErr = b.failChatRun(runContext, &run, emitter, recovered), nil
	})
	// The turn gate is reentrant for this run subtree: a delegated subagent or
	// a peer dialogue executes as a tool call inside this run and must never
	// queue behind the slot this run itself holds.
	runContext = withModelTurnLease(runContext)
	// The approval handler runs inside runtime.Run on this goroutine and needs
	// this run to record waiting_approval while it blocks on the owner.
	runContext = withApprovalRunState(runContext, &run)
	backend, model, err := b.chatBackend(runContext)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	registry, err := b.chatTools(now)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	if runKind != domain.RunKindSubagent {
		if err := registry.Register(delegationAgentTool{
			bridge: b, backend: backend, model: model,
			principalAgentID: agentID, parentRunID: runID,
		}); err != nil {
			return b.failChatRun(runContext, &run, emitter, err), nil
		}
		if err := registry.Register(peerDialogueAgentTool{
			bridge: b, backend: backend, model: model,
			initiatorAgentID: agentID, triggerRunID: runID,
		}); err != nil {
			return b.failChatRun(runContext, &run, emitter, err), nil
		}
	}
	runtime, err := agent.NewRuntime(backend, registry)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	if runKind == domain.RunKindBackground {
		runtime.Authorizer = backgroundToolAuthorizer{bridge: b}
	} else {
		runtime.Authorizer = desktopToolAuthorizer{bridge: b}
	}
	runtime.Approvals = desktopApprovalHandler{bridge: b}
	emitter.tools = registry
	memoryEngine, err := b.newMemoryEngine(backend, model, agentID)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}

	transcript, err := b.repositories.Messages.ListByConversation(runContext, conversationID)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	payloadMessageID := attachmentPayloadMessageID(transcript, userMessageID, request.RetryOfMessageID)
	currentTranscript := b.transcriptForModel(transcript, payloadMessageID)
	// Only the first real user turn on a still-default conversation is eligible.
	// The durable source check avoids spending a model call for a conversation
	// whose owner supplied a title before the first turn. The title worker itself
	// is asynchronous, so it never delays the terminal chat event; the storage
	// CAS remains the guard against a concurrent owner rename.
	autoTitleEligible := runKind == domain.RunKindInteractive && strings.TrimSpace(request.RetryOfMessageID) == "" &&
		len(currentTranscript) == 1 && currentTranscript[0].Role == agent.RoleUser
	if autoTitleEligible {
		conversationForTitle, titleErr := b.repositories.Conversations.Get(runContext, conversationID)
		if titleErr != nil {
			return b.failChatRun(runContext, &run, emitter, titleErr), nil
		}
		autoTitleEligible = conversationTitleEligible(conversationForTitle, request, runKind, currentTranscript)
	}
	assembler, err := contextbuilder.New(desktopContextSource{engine: memoryEngine, repositories: b.repositories, agentID: agentID}, contextbuilder.DefaultConfig())
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	profileID := agentID
	profile, err := b.repositories.Agents.Get(runContext, profileID)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	roster, err := b.repositories.Agents.List(runContext)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	persona, err := b.repositories.Persona.Get(runContext, profileID)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	relationship, err := b.repositories.Relationship.Get(runContext, profileID)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	affect, err := b.repositories.Affect.Get(runContext, profileID)
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	snapshot, err := assembler.Assemble(runContext, contextbuilder.Input{
		AgentID: profileID, ConversationID: conversationID, Query: titleSeed,
		ImmutablePolicy: immutablePolicySystemPrompt, IdentitySeed: agentIdentitySeed(profile, roster),
		Backstory:      profile.Backstory,
		MutablePersona: formatMutablePersonaContext(persona), Relationship: formatRelationshipContext(relationship, affect),
		Transcript: currentTranscript,
	})
	if err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}

	result, runErr := runtime.Run(runContext, agent.RunRequest{
		RunID: runID, ConversationID: conversationID, Budget: run.Budget,
		ModelRequest: agent.ModelRequest{Model: model, Messages: snapshot.Messages},
		Sink:         emitter.Sink,
	})
	if runErr != nil {
		return b.failChatRun(runContext, &run, emitter, runErr), nil
	}
	finishedAt := time.Now().UTC()
	segments := emitter.AssistantSegments()
	if len(segments) == 0 {
		segments = []assistantMessageSegment{{
			ID: string(assistantMessageID), Content: result.Message.Content, CreatedAt: finishedAt,
		}}
	}
	assistantTurnMessages := make([]memory.TranscriptMessage, 0, len(segments))
	for index, segment := range segments {
		providerMeta, err := json.Marshal(map[string]any{
			"run_id": string(runID), "response_id": segment.ResponseID, "segment_index": index,
		})
		if err != nil {
			return b.failChatRun(runContext, &run, emitter, err), nil
		}
		if err := b.repositories.Messages.Create(runContext, storage.Message{
			ID: domain.ID(segment.ID), ConversationID: conversationID, Role: string(agent.RoleAssistant),
			Content: segment.Content, Status: "complete", ProviderMeta: string(providerMeta), CreatedAt: segment.CreatedAt,
		}); err != nil {
			return b.failChatRun(runContext, &run, emitter, err), nil
		}
		assistantTurnMessages = append(assistantTurnMessages, memory.TranscriptMessage{
			ID: domain.ID(segment.ID), ConversationID: conversationID, Role: string(agent.RoleAssistant),
			Content: segment.Content, CreatedAt: segment.CreatedAt,
		})
	}
	if err := transitionAndSave(runContext, b.repositories.Runs, &run, domain.RunStateCompleted); err != nil {
		return b.failChatRun(runContext, &run, emitter, err), nil
	}
	_ = b.touchConversation(runContext, conversationID, finishedAt, agentID)
	emitter.emitTerminal(ChatEvent{Type: runCompletedEventType, ConversationID: string(conversationID), RunID: string(runID), Status: "complete"})
	if autoTitleEligible && strings.TrimSpace(titleSeed) != "" {
		b.scheduleConversationTitle(backend, model, agentID, conversationID, titleSeed)
	}
	reviewContent := request.Text
	if strings.TrimSpace(reviewContent) == "" {
		attachmentNames := make([]string, 0, len(attachments))
		for _, item := range attachments {
			attachmentNames = append(attachmentNames, item.Name)
		}
		if len(attachmentNames) == 0 {
			for _, message := range transcript {
				if message.ID != payloadMessageID {
					continue
				}
				for _, item := range storedAttachments(message.ProviderMeta) {
					attachmentNames = append(attachmentNames, item.Name)
				}
				break
			}
		}
		if len(attachmentNames) > 0 {
			reviewContent = "Вложения: " + strings.Join(attachmentNames, ", ")
		} else {
			reviewContent = "Сообщение с вложением"
		}
	}
	turnMessages := []memory.TranscriptMessage{
		{ID: payloadMessageID, ConversationID: conversationID, Role: string(agent.RoleUser), Content: reviewContent, CreatedAt: now},
	}
	turnMessages = append(turnMessages, assistantTurnMessages...)
	b.reviewTurnInBackground(memoryEngine, backend, model, runKind == domain.RunKindInteractive, memory.Turn{
		RunID: runID, AgentID: profileID, ConversationID: conversationID, Now: finishedAt,
		Messages: turnMessages,
	}, agentID)
	return ChatRunResult{RunID: string(runID), Status: "complete", Events: emitter.Events()}, nil
}

func (b *Bridge) RetryMessage(request ChatRequest) (ChatRunResult, error) {
	return b.SendMessage(request)
}

func (b *Bridge) CancelRun(runID string) error {
	b.mu.RLock()
	cancel := b.activeRuns[strings.TrimSpace(runID)]
	b.mu.RUnlock()
	if cancel == nil {
		return nil
	}
	cancel()
	return nil
}

// ResolveApproval is already exposed for the common approval UI contract.
// Stage 1 only registers the low-risk read-only tool, so no normal run should
// reach this method yet.
func (b *Bridge) ResolveApproval(input approvalDecisionInput) error {
	id := strings.TrimSpace(input.ApprovalID)
	decision := strings.ToLower(strings.TrimSpace(input.Decision))
	switch decision {
	case "approve", "allow_once", "allow_always", "deny":
	default:
		return fmt.Errorf("%w: unsupported approval decision %q", domain.ErrInvalidArgument, input.Decision)
	}
	b.mu.Lock()
	gate := b.approvals[id]
	if decision == "allow_always" && gate != nil && gate.permissionRoot == "" {
		b.mu.Unlock()
		return fmt.Errorf("%w: this action cannot receive a persistent permission", domain.ErrInvalidArgument)
	}
	deliver := gate != nil && !gate.resolved
	if deliver {
		gate.resolved = true
	}
	b.mu.Unlock()
	if !deliver {
		return nil
	}
	// resolved makes this the only send for this gate and the channel holds one
	// buffered decision, so neither the send nor the close can block. The map
	// entry deliberately stays: removing it here is what used to strand a
	// runtime that had not yet reached its own lookup.
	gate.decision <- approvalResolution{decision: decision}
	close(gate.decision)
	return nil
}
