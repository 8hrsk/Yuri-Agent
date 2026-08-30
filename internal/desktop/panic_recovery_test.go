package desktop

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/providers/codexapp"
	schedulerpkg "github.com/OrdoAI/yuri-agent/internal/scheduler"
)

// panickingBackend stands in for any dependency that can fault inside a run:
// a provider adapter, a plugin tool, a corrupt decode path.
type panickingBackend struct{}

func (panickingBackend) Start(context.Context, agent.ModelRequest) (agent.ModelStream, error) {
	panic("provider backend exploded")
}

// TestChatRunPanicFailsTheRunInsteadOfKillingTheProcess pins M-9 on the chat
// path. Two properties are asserted together on purpose: the process survives,
// AND the run is reported failed. A bare recover() would satisfy only the first
// and would leave the owner with a run that silently stopped, which is
// indistinguishable from a hang.
func TestChatRunPanicFailsTheRunInsteadOfKillingTheProcess(t *testing.T) {
	ctx := context.Background()
	bridge := newOpenAIBridgeSmoke(t, "http://127.0.0.1:9/v1", "sk-panic-canary")
	// Fault injection: the run reaches ListByConversation on a nil repository
	// after the emitter exists, so the panic happens inside the guarded region
	// exactly as a nil-map or index fault in bridge code would.
	bridge.repositories.Messages = nil

	result, err := bridge.SendMessage(ChatRequest{
		ConversationID: "conversation-chat-panic", Text: "Расскажи о задаче",
		// Skips the pre-run user-message write so the injected fault lands
		// inside the run rather than during its setup.
		RetryOfMessageID: "message-retry-anchor",
	})
	if err != nil {
		t.Fatalf("panicking run returned an error to the renderer: %v", err)
	}
	if result.Status != "error" || result.RunID == "" {
		t.Fatalf("panicking run result = %#v", result)
	}
	var terminal *ChatEvent
	for index := range result.Events {
		if result.Events[index].Type == runCompletedEventType {
			terminal = &result.Events[index]
		}
	}
	if terminal == nil || terminal.Status != "error" || terminal.Error == "" {
		t.Fatalf("panicking run did not report a terminal error event: %#v", result.Events)
	}

	bridge.repositories.Messages = nil // keep the fault; the run repository is untouched
	run, err := bridge.repositories.Runs.Get(ctx, domain.ID(result.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if run.State != domain.RunStateFailed {
		t.Fatalf("panicking run state = %s, want %s", run.State, domain.RunStateFailed)
	}
	if run.Failure == "" {
		t.Fatalf("panicking run recorded no failure: %#v", run)
	}
}

// TestPeerDialogueGoroutinePanicFailsTheDialogue pins the M-9 finding at its
// original site. The recovery must also reload the dialogue: the goroutine only
// holds the queued snapshot it started from, and saving that stale copy over a
// row already advanced to running loses the failure to the optimistic version
// check, leaving the dialogue running forever.
func TestPeerDialogueGoroutinePanicFailsTheDialogue(t *testing.T) {
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	tool := peerDialogueAgentTool{
		bridge: bridge, backend: panickingBackend{}, model: "test-model",
		initiatorAgentID: initiatorID, triggerRunID: parent.ID,
	}
	result, err := tool.Execute(context.Background(), agent.ToolCall{
		ID: "peer-call-panic", Name: peerDialogueToolID,
		Arguments: json.RawMessage(`{"peer_agent_id":"` + string(peerID) + `","purpose":"Проверить панику","message":"Начинай"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.ID(result.Metadata["dialogue_id"].(string))
	stored := waitForPeerDialogue(t, bridge, id, domain.PeerDialogueFailed)
	if stored.Failure == "" {
		t.Fatalf("panicked dialogue recorded no failure: %#v", stored)
	}
}

// TestPeerDialogueWithoutMessagesFailsInsteadOfPanicking pins the index fault
// named by M-9. It calls executePeerDialogue directly, off any goroutine, so
// the assertion is about the bounds check itself and not about the recovery
// that would otherwise mask it.
func TestPeerDialogueWithoutMessagesFailsInsteadOfPanicking(t *testing.T) {
	ctx := context.Background()
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	now := time.Now().UTC()
	dialogue, err := domain.NewPeerDialogue(
		"peer_dialogue_empty", initiatorID, peerID, parent.ID, "Проверить пустой диалог",
		"peer-call-empty", "sha256:empty", defaultPeerDialogueBudget, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	initial := domain.PeerDialogueMessage{
		ID: domain.ID(string(dialogue.ID) + "_message_0"), DialogueID: dialogue.ID, Sequence: 0,
		SenderAgentID: initiatorID, RecipientAgentID: peerID, SourceRunID: parent.ID,
		Content: "Начинай", CreatedAt: now,
	}
	if err := bridge.repositories.CreatePeerDialogueWithMessage(ctx, dialogue, initial); err != nil {
		t.Fatal(err)
	}
	// A partial write or a hand-edited database, which is the anomaly the
	// finding describes.
	if _, err := bridge.database.ExecContext(ctx, `DELETE FROM peer_dialogue_messages WHERE dialogue_id = ?`, string(dialogue.ID)); err != nil {
		t.Fatal(err)
	}

	bridge.executePeerDialogue(ctx, dialogue, panickingBackend{}, "test-model")

	stored, err := bridge.repositories.PeerDialogues.Get(ctx, dialogue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.PeerDialogueFailed || stored.Failure == "" {
		t.Fatalf("empty dialogue = %#v", stored)
	}
}

// TestCodexLaunchPanicReleasesWaitersWithAnError pins the second half of the
// containment contract for the launch goroutine: recovering is not enough, the
// recovery has to resolve the launch record. Every ensureCodex caller is parked
// on launch.done, so a launch that died without publishing an error would stall
// each of them until the timeout backstop.
func TestCodexLaunchPanicReleasesWaitersWithAnError(t *testing.T) {
	bridge := codexLaunchTestBridge(t, "unused-codex-binary", 2*time.Second)
	bridge.codexStart = func(context.Context, codexapp.Options) (*codexapp.Client, error) {
		panic("codex launch exploded")
	}
	started := time.Now()
	client, err := bridge.ensureCodex(context.Background())
	elapsed := time.Since(started)
	if err == nil || client != nil {
		t.Fatalf("panicked Codex launch = %#v, err = %v", client, err)
	}
	// The backstop for a 2 s launch timeout is 2.5 s. Returning well inside
	// that proves the waiter was released by the recovery and not by the
	// timeout that hides a stranded launch.
	if elapsed >= time.Second {
		t.Fatalf("panicked Codex launch released its waiter after %s; the recovery did not resolve the launch", elapsed)
	}
	bridge.mu.RLock()
	stranded := bridge.codexLaunch
	bridge.mu.RUnlock()
	if stranded != nil {
		t.Fatal("panicked Codex launch stayed in the single-flight slot")
	}
}

// TestScheduledJobPanicIsReportedAsAFailedJob pins the guard on the bridge's
// entry point into the scheduler's worker goroutines. The panic must come back
// as this job's error so the scheduler records a failed job run under its own
// retry policy.
func TestScheduledJobPanicIsReportedAsAFailedJob(t *testing.T) {
	bridge := newAgentTestBridge(t)
	// The audit append is the first thing the job does; a nil repository turns
	// it into the same nil-pointer fault a corrupt dependency would raise. The
	// recovery reports through that same audit path, which proves a failing
	// reporter cannot re-panic the goroutine it was meant to rescue.
	bridge.repositories.Audit = nil
	job := schedulerpkg.ScheduledJob{
		Schedule: domain.Schedule{ID: "schedule-panic", Name: "Panic", PayloadJSON: `{"kind":"agent_task","prompt":"расскажи"}`},
		Run:      domain.JobRun{ID: "job-run-panic", ScheduleID: "schedule-panic"},
	}
	err := bridge.executeScheduledJob(context.Background(), job)
	if err == nil {
		t.Fatal("panicking scheduled job reported success")
	}
}

// TestDeltaFlushTimerPanicDoesNotKillTheProcess covers the one bridge goroutine
// that cannot fail its run from inside: the flush holds dispatchMu for its whole
// body and the terminal event goes out through the same lock. Containment is the
// contract here, and the run's own close path still finishes the stream.
func TestDeltaFlushTimerPanicDoesNotKillTheProcess(t *testing.T) {
	bridge := newAgentTestBridge(t)
	emitter := newChatEmitter(bridge, "conversation-flush-panic", "run-flush-panic", "message-flush-panic")
	explode := make(chan struct{})
	var exploded bool
	emitter.deliver = func(ChatEvent) {
		if !exploded {
			exploded = true
			close(explode)
			panic("renderer delivery exploded")
		}
	}
	emitter.emit(ChatEvent{Type: assistantDeltaEventType, MessageID: "message-flush-panic", Delta: "часть"})
	select {
	case <-explode:
	case <-time.After(5 * time.Second):
		t.Fatal("delta flush timer never delivered")
	}
	// dispatchMu must have been released while the panic unwound, otherwise
	// the run goroutine could never finish its stream.
	done := make(chan struct{})
	go func() {
		emitter.emit(ChatEvent{Type: runCompletedEventType, Status: "complete"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("emitter stayed locked after a panic in the flush timer")
	}
}
