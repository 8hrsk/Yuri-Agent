package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	builtintools "github.com/OrdoAI/yuri-agent/internal/tools"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type desktopToolAuthorizer struct{ bridge *Bridge }

func (authorizer desktopToolAuthorizer) Authorize(_ context.Context, request agent.ToolAuthorizationRequest) (agent.ToolAuthorizationResult, error) {
	if authorizer.bridge != nil && (request.Tool.Name == builtintools.FilesystemReadToolID || request.Tool.Name == builtintools.FilesystemWriteToolID) {
		access, err := filesystemAccessForRoots(request.Call, authorizer.bridge.AllowedDirectories())
		if err != nil {
			return agent.ToolAuthorizationResult{Decision: domain.PermissionDeny, Reason: err.Error()}, nil
		}
		if !access.Allowed {
			return agent.ToolAuthorizationResult{
				Decision: domain.PermissionNeedsApproval,
				Reason:   fmt.Sprintf("Yuri запрашивает доступ к %s для операции %s", access.Path, access.Operation),
			}, nil
		}
	}
	switch request.Tool.Risk {
	case domain.RiskLow:
		return agent.ToolAuthorizationResult{Decision: domain.PermissionAllow, Reason: "low-risk tool"}, nil
	case domain.RiskMedium, domain.RiskHigh:
		return agent.ToolAuthorizationResult{Decision: domain.PermissionNeedsApproval, Reason: "операция требует явного подтверждения"}, nil
	case domain.RiskCritical:
		return agent.ToolAuthorizationResult{Decision: domain.PermissionDeny, Reason: "critical operations are unavailable in MVP"}, nil
	default:
		return agent.ToolAuthorizationResult{Decision: domain.PermissionDeny, Reason: "unknown tool risk"}, nil
	}
}

// backgroundToolAuthorizer prevents a lease recovery or automatic retry from
// repeating a side effect. Scheduled research can still use low-risk read
// tools; mutations and external sends require an interactive owner-confirmed
// run until tools expose a durable execution-key idempotency contract.
type backgroundToolAuthorizer struct{ bridge *Bridge }

func (authorizer backgroundToolAuthorizer) Authorize(_ context.Context, request agent.ToolAuthorizationRequest) (agent.ToolAuthorizationResult, error) {
	if authorizer.bridge != nil && (request.Tool.Name == builtintools.FilesystemReadToolID || request.Tool.Name == builtintools.FilesystemWriteToolID) {
		access, err := filesystemAccessForRoots(request.Call, authorizer.bridge.AllowedDirectories())
		if err != nil {
			return agent.ToolAuthorizationResult{Decision: domain.PermissionDeny, Reason: err.Error()}, nil
		}
		if !access.Allowed {
			return agent.ToolAuthorizationResult{Decision: domain.PermissionDeny, Reason: "фоновая задача не может запрашивать новый доступ к файлам"}, nil
		}
	}
	if request.Tool.Risk == domain.RiskLow {
		return agent.ToolAuthorizationResult{Decision: domain.PermissionAllow, Reason: "low-risk background tool"}, nil
	}
	return agent.ToolAuthorizationResult{
		Decision: domain.PermissionDeny,
		Reason:   "изменяющие и внешние действия фоновой задачи требуют интерактивного запуска",
	}, nil
}

// approvalGate hands one approval decision from the renderer to the runtime
// goroutine that is blocked waiting for it.
//
// The gate belongs to the run that created it, never to the renderer.
// ResolveApproval only delivers a decision into the gate; the entry is removed
// by the waiter in desktopApprovalHandler.Approve, or by the emitter when a run
// ends without ever reaching that wait. Deleting the entry on resolution — as
// an earlier version did — silently voided every approval answered inside the
// window between the approval.required event and the runtime's own lookup: the
// runtime then reported the request as unregistered, the tool never ran, and
// the stored approval stayed pending forever.
type approvalResolution struct {
	decision string
}

type approvalGate struct {
	decision       chan approvalResolution
	permissionRoot string
	// resolved is guarded by Bridge.mu and keeps a repeated resolution of the
	// same approval from sending on an already closed channel.
	resolved bool
}

func (b *Bridge) registerApproval(id domain.ID, permissionRoot ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	root := ""
	if len(permissionRoot) > 0 {
		root = permissionRoot[0]
	}
	b.approvals[string(id)] = &approvalGate{decision: make(chan approvalResolution, 1), permissionRoot: root}
}

func (b *Bridge) releaseApproval(id domain.ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.approvals, string(id))
}

type approvalRunKey struct{}

// withApprovalRunState publishes the run executing under this context to the
// approval handler.
//
// The handler runs on this very goroutine: the agent runtime executes tool
// calls sequentially and stays blocked inside Approve for the whole wait, so
// the pointer is never read or written concurrently with the run loop that owns
// it. Carrying it in the context keeps the association exact and lifetime-bound
// instead of introducing a second run registry to leak.
func withApprovalRunState(ctx context.Context, run *domain.AgentRun) context.Context {
	return context.WithValue(ctx, approvalRunKey{}, run)
}

// beginApprovalWait moves the run into waiting_approval for as long as it is
// blocked on the owner, and returns the resume that puts it back to running.
//
// The state existed in the domain model and failChatRun already branched on it,
// but no desktop path ever set it, so a run parked on an approval card was
// indistinguishable in durable state from one still talking to the provider.
// The saves deliberately use a background context: an owner cancellation must
// still leave the correct state behind for failChatRun to finish.
func (b *Bridge) beginApprovalWait(ctx context.Context) func() {
	run, _ := ctx.Value(approvalRunKey{}).(*domain.AgentRun)
	if b == nil || b.repositories == nil || run == nil || run.State != domain.RunStateRunning {
		return func() {}
	}
	if err := transitionAndSave(context.Background(), b.repositories.Runs, run, domain.RunStateWaitingApproval); err != nil {
		return func() {}
	}
	return func() {
		if run.State != domain.RunStateWaitingApproval {
			return
		}
		_ = transitionAndSave(context.Background(), b.repositories.Runs, run, domain.RunStateRunning)
	}
}

// expireApproval retires an approval the owner never answered. It is the
// proactive half of the five-minute window the approval card advertises;
// without it the only expiry check was the one on the owner's click, so an
// abandoned card left the run blocked until its own duration budget expired.
func (b *Bridge) expireApproval(id domain.ID) error {
	ctx := context.Background()
	stored, err := b.repositories.Approvals.Get(ctx, id)
	if err != nil {
		return err
	}
	if !stored.Pending() {
		return nil
	}
	now := time.Now().UTC()
	if err := stored.Expire(now); err != nil {
		return err
	}
	if err := b.repositories.Approvals.Save(ctx, stored); err != nil {
		return err
	}
	return b.appendApprovalAudit(ctx, stored, "approval.expired", domain.PermissionDeny, domain.ActorSystem)
}

type desktopApprovalHandler struct{ bridge *Bridge }

func (handler desktopApprovalHandler) Approve(ctx context.Context, request agent.ApprovalRequest) (bool, error) {
	if handler.bridge == nil {
		return false, errors.New("approval bridge is unavailable")
	}
	id := approvalIDFor(request.RunID, request.Call.ID)
	handler.bridge.mu.RLock()
	gate := handler.bridge.approvals[string(id)]
	handler.bridge.mu.RUnlock()
	if gate == nil {
		return false, errors.New("approval request was not registered")
	}
	// The waiter owns the registration. Once the gate is in hand the map entry
	// has served its only purpose, so it is dropped however the wait ends —
	// decision received, channel closed, expired, or run cancelled.
	defer handler.bridge.releaseApproval(id)
	// Read the deadline before waiting on it. A pending record that cannot be
	// read leaves the wait unbounded exactly as before rather than denying a
	// tool call over a transient storage error.
	var expiry <-chan time.Time
	if pending, err := handler.bridge.repositories.Approvals.Get(context.Background(), id); err == nil && !pending.ExpiresAt.IsZero() {
		timer := time.NewTimer(time.Until(pending.ExpiresAt))
		defer timer.Stop()
		expiry = timer.C
	}
	resume := handler.bridge.beginApprovalWait(ctx)
	select {
	case resolution, ok := <-gate.decision:
		resume()
		if !ok {
			return false, errors.New("approval request was closed")
		}
		approved := resolution.decision == "approve" || resolution.decision == "allow_once" || resolution.decision == "allow_always"
		remember := approved && resolution.decision == "allow_always"
		if remember && gate.permissionRoot == "" {
			return false, errors.New("persistent approval is unavailable for this action")
		}
		stored, err := handler.bridge.repositories.Approvals.Get(context.Background(), id)
		if err == nil {
			now := time.Now().UTC()
			auditActor := domain.ActorUser
			if approved && !stored.ExpiresAt.IsZero() && !now.Before(stored.ExpiresAt) {
				approved = false
				auditActor = domain.ActorSystem
				err = stored.Expire(now)
			} else if approved {
				err = stored.Approve(domain.ActorUser, "confirmed in desktop UI", now)
			} else {
				err = stored.Deny(domain.ActorUser, "denied in desktop UI", now)
			}
			if err == nil {
				err = handler.bridge.repositories.Approvals.Save(context.Background(), stored)
			}
			if err == nil {
				decision := domain.PermissionDeny
				if approved {
					decision = domain.PermissionAllow
				}
				err = handler.bridge.appendApprovalAudit(context.Background(), stored, "approval.resolved", decision, auditActor)
			}
		}
		if err == nil && remember {
			err = handler.bridge.addAllowedDirectory(gate.permissionRoot)
			if err != nil {
				err = fmt.Errorf("save filesystem permission: %w", err)
			}
		}
		return approved, err
	case <-expiry:
		// The tool is refused, not errored: an unanswered approval is a denial
		// by policy, and the run continues with the denial in hand.
		resume()
		return false, handler.bridge.expireApproval(id)
	case <-ctx.Done():
		// No resume. The run stays in waiting_approval so failChatRun sees the
		// state it already knows how to finish.
		return false, ctx.Err()
	}
}

func (emitter *chatEmitter) createApproval(ctx context.Context, event agent.Event) (*ApprovalView, error) {
	if event.ToolCall == nil {
		return nil, errors.New("approval event is missing tool call")
	}
	risk := domain.RiskHigh
	capabilities := []string{"plugin tool"}
	// A tool that can describe the concrete effect of this specific call gets
	// to replace the generic capability list on the approval card: the owner
	// should approve "every 3600 s, in_app, budget N" rather than "scheduling".
	describedScope := ""
	if emitter.tools != nil {
		if tool, ok := emitter.tools.Get(event.ToolCall.Name); ok {
			descriptor := tool.Descriptor()
			risk = descriptor.Risk
			capabilities = capabilities[:0]
			for _, capability := range descriptor.Capabilities {
				capabilities = append(capabilities, string(capability))
			}
			if scoper, ok := tool.(ToolApprovalScoper); ok {
				if scope, described := scoper.ApprovalScope(event.ToolCall.Arguments); described {
					describedScope = scope
				}
			}
		}
	}
	if len(capabilities) == 0 {
		capabilities = []string{"no external capability"}
	}
	id := approvalIDFor(domain.ID(emitter.runID), event.ToolCall.ID)
	fingerprint := append([]byte(event.ToolCall.Name+"\x00"), event.ToolCall.Arguments...)
	hash := sha256.Sum256(fingerprint)
	now := time.Now().UTC()
	scope := domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{event.ToolCall.Name}}
	approvalScope := strings.Join(capabilities, ", ")
	approvalKind := "action"
	approvalTitle := "Разрешить действие Yuri?"
	approvalPath := ""
	permissionRoot := ""
	canRemember := false
	if describedScope != "" {
		approvalScope = describedScope
	}
	action := "execute tool " + event.ToolCall.Name
	if event.ToolCall.Name == builtintools.FilesystemReadToolID || event.ToolCall.Name == builtintools.FilesystemWriteToolID {
		access, accessErr := filesystemAccessForRoots(*event.ToolCall, emitter.b.AllowedDirectories())
		if accessErr != nil {
			return nil, accessErr
		}
		approvalPath = access.Path
		action = fmt.Sprintf("filesystem.%s %s", access.Operation, access.Path)
		if !access.Allowed {
			approvalKind = "filesystem_access"
			approvalTitle = "Разрешить Yuri доступ к файлам?"
			permissionRoot = access.PermissionRoot
			canRemember = true
			scope = domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{permissionRoot}}
			approvalScope = fmt.Sprintf("%s · доступ к %s", action, permissionRoot)
		}
	}
	if event.ToolCall.Name == builtintools.FilesystemWriteToolID {
		var request builtintools.WriteRequest
		if err := json.Unmarshal(event.ToolCall.Arguments, &request); err != nil {
			return nil, fmt.Errorf("decode filesystem write approval: %w", err)
		}
		path := approvalPath
		if approvalKind == "action" {
			scope = domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{path}}
		}
		contentHash := sha256.Sum256([]byte(request.Content))
		writeDescription := fmt.Sprintf("%s · %s · %d bytes · SHA-256 %s…", request.Operation, path, len(request.Content), hex.EncodeToString(contentHash[:6]))
		if approvalKind == "filesystem_access" {
			approvalScope = writeDescription + " · доступ к " + permissionRoot
		} else {
			approvalScope = writeDescription
		}
		action = fmt.Sprintf("filesystem.%s %s", request.Operation, path)
	}
	record, err := domain.NewApproval(
		id, domain.ID(emitter.runID), hex.EncodeToString(hash[:]), action,
		risk, scope, now,
	)
	if err != nil {
		return nil, err
	}
	record.ToolID = event.ToolCall.Name
	record.ExpiresAt = now.Add(5 * time.Minute)
	if err := emitter.b.repositories.Approvals.Create(ctx, record); err != nil {
		return nil, err
	}
	if err := emitter.b.appendApprovalAudit(ctx, record, "approval.requested", domain.PermissionNeedsApproval, domain.ActorAgent); err != nil {
		return nil, err
	}
	emitter.b.registerApproval(id, permissionRoot)
	emitter.approvalRecords[event.ToolCall.ID] = id
	if toolRecord, exists := emitter.toolRecords[event.ToolCall.ID]; exists {
		toolRecord.ApprovalID = id
		toolRecord.Version++
		toolRecord.UpdatedAt = now
		if err := emitter.b.repositories.ToolCalls.Save(ctx, toolRecord); err != nil {
			return nil, err
		}
		emitter.toolRecords[event.ToolCall.ID] = toolRecord
	}
	return &ApprovalView{
		ID: string(id), ToolCallID: event.ToolCall.ID, Title: approvalTitle,
		Explanation: event.Error, Risk: string(risk), Scope: approvalScope,
		ExpiresAt: record.ExpiresAt.Format(time.RFC3339Nano), Kind: approvalKind,
		Path: approvalPath, PermissionRoot: permissionRoot, CanRemember: canRemember,
	}, nil
}

func (b *Bridge) appendApprovalAudit(ctx context.Context, approval domain.Approval, action string, decision domain.PermissionDecision, actor domain.Actor) error {
	id, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"tool": approval.ToolID, "risk": approval.Risk, "scope": approval.Scope,
	})
	if err != nil {
		return err
	}
	return b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: id, RunID: approval.RunID, ApprovalID: approval.ID, Actor: actor,
		Action: action, Target: approval.ToolID, Decision: decision,
		PayloadRedacted: string(payload), CreatedAt: time.Now().UTC(),
	})
}

func approvalIDFor(runID domain.ID, callID string) domain.ID {
	digest := sha256.Sum256([]byte(string(runID) + "\x00" + callID))
	return domain.ID("approval_" + hex.EncodeToString(digest[:16]))
}

// emit delivers one renderer event. Text deltas are coalesced into a batch that
// is flushed on a timer; every other event first flushes whatever deltas are
// pending, so the renderer always observes deltas, tool events and lifecycle
// events in the order the runtime produced them.
func (emitter *chatEmitter) emit(event ChatEvent) {
	if event.ProviderID == "" {
		event.ProviderID = emitter.providerID
	}
	if event.Model == "" {
		event.Model = emitter.model
	}
	if event.Type == runCompletedEventType {
		event.InputTokens = emitter.usage.InputTokens
		event.OutputTokens = emitter.usage.OutputTokens
		event.TotalTokens = emitter.usage.TotalTokens
	}
	if event.CreatedAt == "" {
		event.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if event.Type == assistantDeltaEventType {
		emitter.queueDelta(event)
		return
	}
	emitter.dispatchMu.Lock()
	defer emitter.dispatchMu.Unlock()
	emitter.flushPendingLocked()
	emitter.record(event)
	emitter.dispatch(event)
}

// emitTerminal emits the single run.completed event of this run. Both the
// runtime sink and the bridge's own finalization can reach it; only the first
// one wins so the renderer never sees two terminal events for one run.
func (emitter *chatEmitter) emitTerminal(event ChatEvent) bool {
	emitter.mu.Lock()
	if emitter.terminalEmitted {
		emitter.mu.Unlock()
		return false
	}
	emitter.terminalEmitted = true
	emitter.mu.Unlock()
	emitter.emit(event)
	return true
}

func (emitter *chatEmitter) queueDelta(event ChatEvent) {
	emitter.dispatchMu.Lock()
	defer emitter.dispatchMu.Unlock()
	emitter.mu.Lock()
	boundary, hasBoundary := ChatEvent{}, false
	if emitter.pendingDelta.Len() > 0 && emitter.pendingEvent.MessageID != event.MessageID {
		boundary, hasBoundary = emitter.takePendingLocked()
	}
	if emitter.pendingDelta.Len() == 0 {
		emitter.pendingEvent = event
		emitter.pendingEvent.Delta = ""
	}
	emitter.pendingDelta.WriteString(event.Delta)
	immediate := emitter.closed
	if !immediate && emitter.flushTimer == nil {
		emitter.flushTimer = time.AfterFunc(assistantDeltaFlushInterval, emitter.flushPendingFromTimer)
	}
	batch, hasBatch := ChatEvent{}, false
	if immediate {
		batch, hasBatch = emitter.takePendingLocked()
	}
	emitter.mu.Unlock()
	if hasBoundary {
		emitter.dispatch(boundary)
	}
	if hasBatch {
		emitter.dispatch(batch)
	}
}

// flushPendingFromTimer is what the delta timer goroutine runs. A panic in the
// renderer delivery path would otherwise kill the process from a goroutine the
// run never sees. It deliberately does not fail the run: this call holds
// dispatchMu for the whole flush, and the terminal event goes out through emit,
// which takes the same lock — reporting from here would deadlock. The run
// goroutine still reaches its own terminal path through emitter.close, because
// flushPending releases dispatchMu while unwinding.
func (emitter *chatEmitter) flushPendingFromTimer() {
	defer emitter.b.recoverBridgeGoroutine("chat_delta_flush", nil)
	emitter.flushPending()
}

// flushPending is the explicit flush used before any non-delta delivery.
func (emitter *chatEmitter) flushPending() {
	emitter.dispatchMu.Lock()
	defer emitter.dispatchMu.Unlock()
	emitter.flushPendingLocked()
}

// flushPendingLocked requires dispatchMu.
func (emitter *chatEmitter) flushPendingLocked() {
	emitter.mu.Lock()
	batch, ok := emitter.takePendingLocked()
	emitter.mu.Unlock()
	if ok {
		emitter.dispatch(batch)
	}
}

// takePendingLocked requires mu.
func (emitter *chatEmitter) takePendingLocked() (ChatEvent, bool) {
	if emitter.flushTimer != nil {
		emitter.flushTimer.Stop()
		emitter.flushTimer = nil
	}
	if emitter.pendingDelta.Len() == 0 {
		return ChatEvent{}, false
	}
	batch := emitter.pendingEvent
	batch.Delta = emitter.pendingDelta.String()
	emitter.pendingDelta.Reset()
	return batch, true
}

// record keeps only non-delta events in the result payload. Returning the whole
// delta stream a second time as ChatRunResult.Events sent every answer across
// the bridge twice and forced the renderer to deduplicate by serializing every
// event; live deltas are the single delivery path now.
func (emitter *chatEmitter) record(event ChatEvent) {
	if event.Type == assistantDeltaEventType {
		return
	}
	emitter.mu.Lock()
	emitter.events = append(emitter.events, event)
	emitter.mu.Unlock()
}

func (emitter *chatEmitter) dispatch(event ChatEvent) {
	emitter.b.mu.RLock()
	appContext := emitter.b.appCtx
	emitter.b.mu.RUnlock()
	if appContext != nil {
		wailsruntime.EventsEmit(appContext, chatEventName, event)
	}
	if emitter.deliver != nil {
		emitter.deliver(event)
	}
}

// close finalizes the run for the renderer whatever happened to it: the last
// partial delta batch is flushed, a still-open assistant segment is completed,
// and a terminal event is emitted if no path emitted one yet. Without this a
// cancelled run left its assistant message streaming forever.
func (emitter *chatEmitter) close(ctx context.Context) {
	emitter.mu.Lock()
	emitter.closed = true
	emitter.mu.Unlock()
	emitter.flushPending()
	if completedID := emitter.closeAssistantSegment(); completedID != "" {
		emitter.emit(ChatEvent{
			Type: "assistant.completed", ConversationID: emitter.conversationID,
			RunID: emitter.runID, MessageID: completedID,
		})
	}
	status, message := "error", "Запуск завершился без результата"
	if ctx != nil && ctx.Err() != nil {
		status, message = "cancelled", "Запуск остановлен"
	}
	emitter.emitTerminal(ChatEvent{
		Type: runCompletedEventType, ConversationID: emitter.conversationID,
		RunID: emitter.runID, Status: status, Error: message,
	})
	emitter.releaseApprovals()
}

// releaseApprovals drops the gates this run registered but never waited on. A
// run that dies between the approval.required event and the runtime's wait
// would otherwise leave its registration behind for the life of the process.
// close runs after the run goroutine has returned, so no waiter can still hold
// a gate this removes.
func (emitter *chatEmitter) releaseApprovals() {
	if emitter.b == nil {
		return
	}
	for _, id := range emitter.approvalRecords {
		emitter.b.releaseApproval(id)
	}
}

func (emitter *chatEmitter) Events() []ChatEvent {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	return append([]ChatEvent(nil), emitter.events...)
}
