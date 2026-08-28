package desktop

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/reflection"
)

type reflectionBackendStub struct {
	request agent.ModelRequest
	events  []agent.ModelEvent
}

func (backend *reflectionBackendStub) Start(_ context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	backend.request = request
	return &reflectionStreamStub{events: append([]agent.ModelEvent(nil), backend.events...)}, nil
}

type reflectionStreamStub struct {
	events []agent.ModelEvent
	index  int
}

func (stream *reflectionStreamStub) Recv(context.Context) (agent.ModelEvent, error) {
	if stream.index >= len(stream.events) {
		return agent.ModelEvent{}, io.EOF
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}
func (*reflectionStreamStub) Close() error { return nil }

func TestModelReflectionBackendUsesNoToolsAndReturnsStrictJSON(t *testing.T) {
	provider := &reflectionBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"insufficient durable evidence"}`},
		{Type: agent.ModelEventCompleted, Usage: agent.Usage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}},
	}}
	backend := modelReflectionBackend{backend: provider, model: "test-model"}
	response, err := backend.Complete(context.Background(), reflection.ModelRequest{
		Snapshot: reflection.InputSnapshot{
			ProfileID: "owner", RunID: "run-1", Trigger: reflection.TriggerPostTurn,
			CapturedAt: time.Now().UTC(), ImmutablePolicy: "immutable", IdentitySeed: "identity",
		},
		Budget: reflection.ReflectionBudget{MaxTokens: 100, MaxOutputBytes: 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.request.Tools) != 0 || provider.request.MaxOutputTokens != 100 {
		t.Fatalf("unsafe or unbounded provider request = %#v", provider.request)
	}
	if provider.request.Metadata["purpose"] != "background_reflection" || !strings.Contains(provider.request.Messages[0].Content, "Do not call tools") {
		t.Fatalf("reflection safety envelope missing: %#v", provider.request)
	}
	if string(response.JSON) != `{"outcome":"no_change","reason":"insufficient durable evidence"}` || response.Usage.TotalTokens != 14 {
		t.Fatalf("response = %#v", response)
	}
}

func TestModelReflectionBackendRejectsMarkdownAndOversizedOutput(t *testing.T) {
	base := reflection.ModelRequest{
		Snapshot: reflection.InputSnapshot{ProfileID: domain.ID("owner"), RunID: domain.ID("run-1"), Trigger: reflection.TriggerPostTurn, CapturedAt: time.Now().UTC()},
		Budget:   reflection.ReflectionBudget{MaxTokens: 100, MaxOutputBytes: 16},
	}
	provider := &reflectionBackendStub{events: []agent.ModelEvent{{Type: agent.ModelEventTextDelta, Delta: "```json\n{}\n```"}}}
	_, err := (modelReflectionBackend{backend: provider, model: "test"}).Complete(context.Background(), base)
	if err == nil {
		t.Fatal("expected non-JSON output rejection")
	}
	provider.events = []agent.ModelEvent{{Type: agent.ModelEventTextDelta, Delta: strings.Repeat("x", 17)}}
	_, err = (modelReflectionBackend{backend: provider, model: "test"}).Complete(context.Background(), base)
	if err == nil || !strings.Contains(err.Error(), reflection.ErrBudgetExceeded.Error()) {
		t.Fatalf("oversized output error = %v", err)
	}
}
