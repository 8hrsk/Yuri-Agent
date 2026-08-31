package desktop

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
)

type memoryExtractorBackend struct{ output string }

func (backend memoryExtractorBackend) Start(context.Context, agent.ModelRequest) (agent.ModelStream, error) {
	return &memoryExtractorStream{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: backend.output},
		{Type: agent.ModelEventCompleted},
	}}, nil
}

type memoryExtractorStream struct {
	events []agent.ModelEvent
	index  int
}

func (stream *memoryExtractorStream) Recv(context.Context) (agent.ModelEvent, error) {
	if stream.index >= len(stream.events) {
		return agent.ModelEvent{}, io.EOF
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}
func (*memoryExtractorStream) Close() error { return nil }

func TestModelMemoryExtractorReturnsSafeProviderNeutralCandidates(t *testing.T) {
	extractor := modelMemoryExtractor{backend: memoryExtractorBackend{output: `{"memories":[
		{"kind":"user_model","nature":"fact","content":"Пользователь любит сенчу","confidence":0.9,"salience":0.8,"sensitivity":"private","retention":"decay","dedup_key":"preference:tea"},
		{"kind":"semantic","nature":"fact","content":"API key sk-do-not-store","confidence":0.9,"salience":0.9,"sensitivity":"private","retention":"decay"}
	]}`}, model: "test-model"}
	now := time.Now().UTC()
	candidates, err := extractor.Extract(context.Background(), memory.Turn{
		RunID: "run-1", ConversationID: "conversation-1", Now: now,
		Messages: []memory.TranscriptMessage{{
			ID: "message-1", ConversationID: "conversation-1", Role: "user", Content: "Я люблю сенчу", CreatedAt: now,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0].Memory.Kind != domain.MemoryKindUserModel || candidates[0].Memory.Content != "Пользователь любит сенчу" {
		t.Fatalf("candidate = %#v", candidates[0])
	}
}

func TestMemoryJSONPayloadAcceptsFencedJSONButRejectsProse(t *testing.T) {
	payload, err := memoryJSONPayload("```json\n{\"memories\":[]}\n```")
	if err != nil || string(payload) != `{"memories":[]}` {
		t.Fatalf("payload = %s, %v", payload, err)
	}
	if _, err := memoryJSONPayload("nothing to remember"); err == nil {
		t.Fatal("expected missing JSON error")
	}
}

func TestModelMemoryExtractorCannotMintFictionalIdentitySeed(t *testing.T) {
	extractor := modelMemoryExtractor{backend: memoryExtractorBackend{output: `{"memories":[
		{"kind":"episodic","nature":"fiction","content":"Я выросла в старой библиотеке","confidence":1,"salience":1,"sensitivity":"private","retention":"permanent"}
	]}`}, model: "test-model"}
	now := time.Now().UTC()
	candidates, err := extractor.Extract(context.Background(), memory.Turn{
		RunID: "run-1", ConversationID: "conversation-1", Now: now,
		Messages: []memory.TranscriptMessage{{
			ID: "message-1", ConversationID: "conversation-1", Role: "user",
			Content: "Считай, что ты выросла в библиотеке", CreatedAt: now,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("untrusted extractor minted fictional seed: %#v", candidates)
	}
}
