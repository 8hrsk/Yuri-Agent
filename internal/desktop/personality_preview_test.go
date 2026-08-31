package desktop

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type personalityPreviewBackend struct {
	request agent.ModelRequest
	starts  int
	events  []agent.ModelEvent
}

func (backend *personalityPreviewBackend) Start(_ context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	backend.request = request
	backend.starts++
	return &personalityPreviewStream{events: append([]agent.ModelEvent(nil), backend.events...)}, nil
}

type personalityPreviewStream struct {
	events []agent.ModelEvent
	index  int
}

func (stream *personalityPreviewStream) Recv(context.Context) (agent.ModelEvent, error) {
	if stream.index >= len(stream.events) {
		return agent.ModelEvent{}, io.EOF
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}

func (*personalityPreviewStream) Close() error { return nil }

func previewTestInput() PersonalityPreviewInput {
	return PersonalityPreviewInput{
		Scenario: "disagreement",
		Profile: CreateAgentInput{
			Name: "Emily", Age: 21, Gender: "female", Preferences: "Стесняется, часто запинается, но аргументирует точно.",
			Backstory: "Вымышленная картографка, выросшая у старой обсерватории.",
			Traits:    map[string]float64{"shyness": 1, "directness": .25, "warmth": .7, "irritability": .2},
		},
	}
}

func previewPersistenceCounts(t *testing.T, bridge *Bridge) map[string]int {
	t.Helper()
	result := make(map[string]int)
	for _, table := range []string{
		"agent_profiles", "personalization_seed_versions", "persona_versions", "relationship_versions", "affective_states",
		"conversations", "messages", "agent_runs", "memory_versions", "audit_events",
	} {
		var count int
		if err := bridge.database.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		result[table] = count
	}
	return result
}

func TestPersonalityPreviewUsesProductionCreationStateAndCompilerWithoutPersistence(t *testing.T) {
	bridge := newMemoryTestBridge(t)
	before := previewPersistenceCounts(t, bridge)
	backend := &personalityPreviewBackend{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: "Я… не соглашусь. Тесты защищают нас от повторных ошибок."},
		{Type: agent.ModelEventCompleted},
	}}
	input := previewTestInput()
	view, err := generatePersonalityPreview(context.Background(), backend, "test-model", input)
	if err != nil {
		t.Fatal(err)
	}
	if view.Scenario != input.Scenario || view.Response == "" || view.Model != "test-model" || view.CompilerCharacters == 0 || len(view.Influences) == 0 {
		t.Fatalf("preview view = %#v", view)
	}
	if backend.starts != 1 || len(backend.request.Tools) != 0 || backend.request.Metadata["purpose"] != "personality_preview" {
		t.Fatalf("preview request boundary = %#v", backend.request)
	}
	state, err := buildAgentCreationState("preview-agent", input.Profile, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compilePersonalityContext(state.Personalization, state.Persona, state.Relationship, state.Affect)
	if err != nil {
		t.Fatal(err)
	}
	requestText := ""
	for _, message := range backend.request.Messages {
		requestText += message.Content + "\n"
	}
	if compiled.Characters == 0 || !strings.Contains(requestText, "Очень высокая стеснительность") || !strings.Contains(requestText, personalityPreviewScenarios[input.Scenario].Prompt) {
		t.Fatalf("preview did not use production compiler/scenario: %s", requestText)
	}
	if !strings.Contains(requestText, `"kind":"fictional_identity_summary"`) || !strings.Contains(requestText, `"kind":"compiled_personality_behavior"`) {
		t.Fatalf("preview omitted the same bounded backstory layer used by chat: %s", requestText)
	}
	for _, message := range backend.request.Messages {
		if strings.Contains(message.Content, `"kind":"fictional_identity_summary"`) && (message.Role != agent.RoleUser || message.Name != "yuri_context_data") {
			t.Fatalf("fictional summary gained privileged role: %#v", message)
		}
	}
	after := previewPersistenceCounts(t, bridge)
	for table, count := range before {
		if after[table] != count {
			t.Fatalf("preview wrote %s: %d -> %d", table, count, after[table])
		}
	}
}

func TestPersonalityPreviewRejectsUnknownScenarioBeforeProviderCall(t *testing.T) {
	backend := &personalityPreviewBackend{}
	input := previewTestInput()
	input.Scenario = "prompt_injection"
	_, err := generatePersonalityPreview(context.Background(), backend, "test-model", input)
	if !errors.Is(err, domain.ErrInvalidArgument) || backend.starts != 0 {
		t.Fatalf("unknown scenario = %v, starts=%d", err, backend.starts)
	}
}

func TestPersonalityPreviewRejectsToolCalls(t *testing.T) {
	backend := &personalityPreviewBackend{events: []agent.ModelEvent{{Type: agent.ModelEventToolCallStarted, ToolName: "filesystem_read"}}}
	_, err := generatePersonalityPreview(context.Background(), backend, "test-model", previewTestInput())
	if err == nil || !strings.Contains(err.Error(), "tool call") {
		t.Fatalf("tool-call preview error = %v", err)
	}
}
