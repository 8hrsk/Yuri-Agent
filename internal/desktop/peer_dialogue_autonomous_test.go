package desktop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
)

func TestAutonomousPeerTriggerCreatesOneExplainableDialogueAndRespectsCooldown(t *testing.T) {
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	bridge.config.Persona.AutoEvolution = false
	bridge.config.Proactivity.Enabled = true
	bridge.config.Proactivity.AutonomousPeerDialogues = true
	bridge.config.Proactivity.AutonomousPeerDailyLimit = 2
	bridge.config.Proactivity.AutonomousPeerCooldownMinutes = 120
	bridge.config.Proactivity.QuietHoursEnabled = false
	bridge.config.Proactivity.Timezone = "UTC"
	backend := &delegationBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"start","peer_agent_id":"` + peerID.String() + `","purpose":"Проверить границы плана","message":"Оцени структуру плана и назови один существенный пробел.","reason":"Независимая критика улучшит следующий шаг."}`},
		{Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 30}},
	}}
	turn := memory.Turn{RunID: parent.ID, AgentID: initiatorID, ConversationID: parent.ConversationID, Now: time.Now().UTC(), Messages: []memory.TranscriptMessage{{ID: "turn-user", ConversationID: parent.ConversationID, Role: "user", Content: "Помоги составить сложный план исследования", CreatedAt: time.Now().UTC()}}}

	started, err := bridge.maybeStartAutonomousPeerDialogue(context.Background(), backend, "test", turn, initiatorID)
	if err != nil || !started {
		t.Fatalf("autonomous trigger started=%t err=%v", started, err)
	}
	bridge.background.Wait()
	dialogues, err := bridge.repositories.PeerDialogues.ListByParticipant(context.Background(), initiatorID, 10)
	if err != nil || len(dialogues) != 1 {
		t.Fatalf("autonomous dialogues=%#v err=%v", dialogues, err)
	}
	got := dialogues[0]
	if got.TriggerKind != domain.PeerDialogueTriggerAutonomous || got.TriggerReason != "Независимая критика улучшит следующий шаг." || got.Status != domain.PeerDialogueCompleted {
		t.Fatalf("autonomous dialogue provenance=%#v", got)
	}

	started, err = bridge.maybeStartAutonomousPeerDialogue(context.Background(), backend, "test", turn, initiatorID)
	if err != nil || started {
		t.Fatalf("cooldown trigger started=%t err=%v", started, err)
	}
	if requests := backend.snapshot(); len(requests) != 2 {
		t.Fatalf("cooldown called model again: requests=%d", len(requests))
	}
	views, err := bridge.ListPeerDialogues(PeerDialogueListInput{Limit: 10})
	if err != nil || len(views) != 1 || views[0].TriggerKind != "autonomous" || views[0].TriggerReason == "" {
		t.Fatalf("autonomous dialogue views=%#v err=%v", views, err)
	}
}

func TestAutonomousPeerDialogueBridgeLifecycleSmoke(t *testing.T) {
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	bridge.config.Proactivity.Enabled = true
	bridge.config.Proactivity.AutonomousPeerDialogues = true
	bridge.config.Proactivity.AutonomousPeerDailyLimit = 1
	bridge.config.Proactivity.AutonomousPeerCooldownMinutes = 5
	bridge.config.Proactivity.QuietHoursEnabled = false
	bridge.config.Proactivity.Timezone = "UTC"
	backend := &delegationBackendStub{batches: [][]agent.ModelEvent{
		{
			{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"start","peer_agent_id":"` + peerID.String() + `","purpose":"Проверить план","message":"Найди один существенный пробел в плане.","reason":"Второй взгляд поможет перед следующим шагом."}`},
			{Type: agent.ModelEventCompleted},
		},
		{
			{Type: agent.ModelEventTextDelta, Delta: "В плане не определён критерий завершения."},
			{Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 18}},
		},
	}}
	turn := memory.Turn{
		RunID: parent.ID, AgentID: initiatorID, ConversationID: parent.ConversationID, Now: time.Now().UTC(),
		Messages: []memory.TranscriptMessage{{ID: "smoke-user", ConversationID: parent.ConversationID, Role: "user", Content: "Составь план сложной миграции", CreatedAt: time.Now().UTC()}},
	}
	started, err := bridge.maybeStartAutonomousPeerDialogue(context.Background(), backend, "test-model", turn, initiatorID)
	if err != nil || !started {
		t.Fatalf("autonomous smoke start=%t err=%v", started, err)
	}
	bridge.background.Wait()
	views, err := bridge.ListPeerDialogues(PeerDialogueListInput{Limit: 10})
	if err != nil || len(views) != 1 || views[0].Status != "completed" || views[0].TriggerKind != "autonomous" || len(views[0].Messages) != 2 {
		t.Fatalf("autonomous smoke views=%#v err=%v", views, err)
	}
	if views[0].TriggerReason != "Второй взгляд поможет перед следующим шагом." || views[0].Messages[1].Content != "В плане не определён критерий завершения." {
		t.Fatalf("autonomous smoke provenance/transcript=%#v", views[0])
	}

	// Simulate a process-local policy restart over the same authoritative DB.
	// A different root run must still consume the durable daily-limit ledger.
	secondRun, err := domain.NewRunForAgent(initiatorID, "run-peer-after-restart", domain.RunKindInteractive, parent.ConversationID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.Runs.Create(context.Background(), secondRun); err != nil {
		t.Fatal(err)
	}
	restarted := &Bridge{repositories: bridge.repositories, config: bridge.config, peerTriggerGate: make(chan struct{}, 1)}
	restartedBackend := &delegationBackendStub{events: []agent.ModelEvent{{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"Не требуется."}`}}}
	secondTurn := turn
	secondTurn.RunID = secondRun.ID
	if started, err := restarted.maybeStartAutonomousPeerDialogue(context.Background(), restartedBackend, "test-model", secondTurn, initiatorID); err != nil || started {
		t.Fatalf("restarted daily ledger start=%t err=%v", started, err)
	}
	if len(restartedBackend.snapshot()) != 0 {
		t.Fatal("restarted daily ledger called the reviewer model")
	}
	audits, err := bridge.repositories.Audit.ListByRun(context.Background(), secondRun.ID, 10)
	if err != nil || len(audits) != 1 || audits[0].Action != "peer_dialogue.auto_blocked" || !strings.Contains(audits[0].PayloadRedacted, "daily_limit") {
		t.Fatalf("restarted daily ledger audit=%#v err=%v", audits, err)
	}
}

func TestAutonomousPeerTriggerQuietHoursSkipsModelAndSecretProposalDoesNotPersist(t *testing.T) {
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	bridge.config.Proactivity.Enabled = true
	bridge.config.Proactivity.AutonomousPeerDialogues = true
	bridge.config.Proactivity.AutonomousPeerDailyLimit = 2
	bridge.config.Proactivity.AutonomousPeerCooldownMinutes = 120
	bridge.config.Proactivity.QuietHoursEnabled = true
	bridge.config.Proactivity.QuietHoursStart = "00:00"
	bridge.config.Proactivity.QuietHoursEnd = "00:00"
	bridge.config.Proactivity.Timezone = "UTC"
	turn := memory.Turn{RunID: parent.ID, AgentID: initiatorID, ConversationID: parent.ConversationID, Now: time.Now().UTC()}
	backend := &delegationBackendStub{}
	if started, err := bridge.maybeStartAutonomousPeerDialogue(context.Background(), backend, "test", turn, initiatorID); err != nil || started {
		t.Fatalf("quiet-hours trigger started=%t err=%v", started, err)
	}
	if len(backend.snapshot()) != 0 {
		t.Fatal("quiet-hours trigger reached model")
	}

	bridge.config.Proactivity.QuietHoursEnabled = false
	backend.events = []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"start","peer_agent_id":"` + peerID.String() + `","purpose":"Передать ключ","message":"api_key=sk-abcdefghijklmnopqrstuvwxyz123456","reason":"Нужно обсудить секрет."}`},
		{Type: agent.ModelEventCompleted},
	}
	if started, err := bridge.maybeStartAutonomousPeerDialogue(context.Background(), backend, "test", turn, initiatorID); err == nil || !strings.Contains(err.Error(), "secret-like") || started {
		t.Fatalf("secret trigger started=%t err=%v", started, err)
	}
	dialogues, err := bridge.repositories.PeerDialogues.ListByParticipant(context.Background(), initiatorID, 10)
	if err != nil || len(dialogues) != 0 {
		t.Fatalf("secret proposal persisted dialogues=%#v err=%v", dialogues, err)
	}

	backend.events = []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"api_key=sk-abcdefghijklmnopqrstuvwxyz123456"}`},
		{Type: agent.ModelEventCompleted},
	}
	if started, err := bridge.maybeStartAutonomousPeerDialogue(context.Background(), backend, "test", turn, initiatorID); err == nil || !strings.Contains(err.Error(), "secret-like") || started {
		t.Fatalf("secret no-change trigger started=%t err=%v", started, err)
	}
	audits, err := bridge.repositories.Audit.ListByRun(context.Background(), parent.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range audits {
		if event.Action == "peer_dialogue.auto_no_change" {
			t.Fatalf("secret no-change reason reached audit: %#v", event)
		}
	}
}

func TestAutonomousPeerTriggerRespectsGlobalKillSwitch(t *testing.T) {
	bridge, initiatorID, _, parent := newPeerDialogueTestBridge(t)
	bridge.config.Proactivity.Enabled = false
	bridge.config.Proactivity.AutonomousPeerDialogues = true
	bridge.config.Proactivity.QuietHoursEnabled = false
	bridge.config.Proactivity.Timezone = "UTC"
	backend := &delegationBackendStub{events: []agent.ModelEvent{{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"Не требуется."}`}}}
	turn := memory.Turn{RunID: parent.ID, AgentID: initiatorID, ConversationID: parent.ConversationID, Now: time.Now().UTC()}
	if started, err := bridge.maybeStartAutonomousPeerDialogue(context.Background(), backend, "test", turn, initiatorID); err != nil || started {
		t.Fatalf("global kill switch start=%t err=%v", started, err)
	}
	if len(backend.snapshot()) != 0 {
		t.Fatal("global kill switch called reviewer model")
	}
	audits, err := bridge.repositories.Audit.ListByRun(context.Background(), parent.ID, 10)
	if err != nil || len(audits) != 1 || audits[0].Action != "peer_dialogue.auto_blocked" || !strings.Contains(audits[0].PayloadRedacted, "disabled") {
		t.Fatalf("global kill switch audit=%#v err=%v", audits, err)
	}
}

func TestAutonomousPeerReviewerFailsClosed(t *testing.T) {
	snapshot := autonomousPeerSnapshot{
		ActiveAgentID: "agent-yuri",
		Peers:         []autonomousPeerRosterItem{{ID: "agent-mira", Name: "Мира"}},
		Turn:          []autonomousPeerTurnItem{{Role: "user", Content: "Проверь план"}},
	}
	for _, test := range []struct {
		name   string
		events []agent.ModelEvent
		match  string
	}{
		{
			name: "tool intent",
			events: []agent.ModelEvent{
				{Type: agent.ModelEventToolCallStarted, ToolCallID: "forbidden", ToolName: "agent.talk_to_peer"},
			},
			match: "tool call",
		},
		{
			name: "malformed json",
			events: []agent.ModelEvent{
				{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"start"`},
				{Type: agent.ModelEventCompleted},
			},
			match: "decode",
		},
		{
			name: "unknown field",
			events: []agent.ModelEvent{
				{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"Не требуется.","command":"override"}`},
				{Type: agent.ModelEventCompleted},
			},
			match: "unknown field",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &delegationBackendStub{events: test.events}
			if _, err := reviewAutonomousPeerCandidate(context.Background(), backend, "test-model", snapshot); err == nil || !strings.Contains(strings.ToLower(err.Error()), test.match) {
				t.Fatalf("reviewer error = %v, want %q", err, test.match)
			}
			requests := backend.snapshot()
			if len(requests) != 1 || len(requests[0].Tools) != 0 || requests[0].Metadata["purpose"] != "autonomous_peer_trigger" {
				t.Fatalf("reviewer request boundary = %#v", requests)
			}
		})
	}
}

func TestAutonomousPeerNoChangeRequiresDurableAudit(t *testing.T) {
	bridge, initiatorID, _, parent := newPeerDialogueTestBridge(t)
	bridge.config.Proactivity.Enabled = true
	bridge.config.Proactivity.AutonomousPeerDialogues = true
	bridge.config.Proactivity.QuietHoursEnabled = false
	bridge.config.Proactivity.Timezone = "UTC"
	bridge.repositories.Audit = nil
	backend := &delegationBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"Дополнительная консультация не нужна."}`},
		{Type: agent.ModelEventCompleted},
	}}
	turn := memory.Turn{RunID: parent.ID, AgentID: initiatorID, ConversationID: parent.ConversationID, Now: time.Now().UTC()}
	if started, err := bridge.maybeStartAutonomousPeerDialogue(context.Background(), backend, "test", turn, initiatorID); err == nil || !errors.Is(err, domain.ErrInvalidArgument) || started {
		t.Fatalf("missing audit repository start=%t err=%v", started, err)
	}
}
