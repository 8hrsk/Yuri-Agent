package desktop

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// recordingEmitter builds an emitter whose dispatched events are captured, so a
// test can assert what the renderer would actually receive.
func recordingEmitter(t *testing.T) (*chatEmitter, func() []ChatEvent) {
	t.Helper()
	emitter := newChatEmitter(&Bridge{}, "conversation", "run", "message-first")
	var mu sync.Mutex
	delivered := make([]ChatEvent, 0, 64)
	emitter.deliver = func(event ChatEvent) {
		mu.Lock()
		delivered = append(delivered, event)
		mu.Unlock()
	}
	return emitter, func() []ChatEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]ChatEvent(nil), delivered...)
	}
}

func deltaText(events []ChatEvent) string {
	var text strings.Builder
	for _, event := range events {
		if event.Type == assistantDeltaEventType {
			text.WriteString(event.Delta)
		}
	}
	return text.String()
}

func countChatEvents(events []ChatEvent, kind string) int {
	total := 0
	for _, event := range events {
		if event.Type == kind {
			total++
		}
	}
	return total
}

func hasChatEvent(events []ChatEvent, kind string) bool {
	return countChatEvents(events, kind) > 0
}

func TestChatEmitterCoalescesDeltasWithoutLosingText(t *testing.T) {
	emitter, delivered := recordingEmitter(t)
	const deltas = 500
	var naive string
	for index := 0; index < deltas; index++ {
		text := fmt.Sprintf("токен-%d ", index)
		naive += text
		if err := emitter.Sink(context.Background(), agent.Event{
			Type: agent.EventModelTextDelta, ResponseID: "item-1", Text: text,
		}); err != nil {
			t.Fatal(err)
		}
	}
	emitter.close(context.Background())

	events := delivered()
	batches := countChatEvents(events, assistantDeltaEventType)
	if batches == 0 || batches > deltas/10 {
		t.Fatalf("delta batches = %d for %d deltas, want a coalesced stream", batches, deltas)
	}
	if got := deltaText(events); got != naive {
		t.Fatalf("coalesced delta text differs from naive concatenation:\n got %q\nwant %q", got, naive)
	}
	segments := emitter.AssistantSegments()
	if len(segments) != 1 || segments[0].Content != naive {
		t.Fatalf("accumulated segment is not byte-identical to naive concatenation: %#v", segments)
	}
	for _, event := range emitter.Events() {
		if event.Type == assistantDeltaEventType {
			t.Fatalf("ChatRunResult payload still carries the delta stream: %#v", event)
		}
	}
}

// TestChatEmitterFlushesDeltasBeforeNonDeltaEvents pins the ordering contract:
// a queued batch is never overtaken by a tool or lifecycle event, and the
// timer flush cannot interleave with one either.
func TestChatEmitterFlushesDeltasBeforeNonDeltaEvents(t *testing.T) {
	emitter, delivered := recordingEmitter(t)
	ctx := context.Background()
	for _, text := range []string{"Смотрю", " файл", "."} {
		if err := emitter.Sink(ctx, agent.Event{Type: agent.EventModelTextDelta, ResponseID: "item-1", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	// EventRunCompleted is the closest non-delta neighbour of the last token.
	if err := emitter.Sink(ctx, agent.Event{Type: agent.EventRunCompleted}); err != nil {
		t.Fatal(err)
	}
	emitter.close(ctx)

	events := delivered()
	if len(events) < 2 {
		t.Fatalf("delivered events = %#v", events)
	}
	firstNonDelta := -1
	for index, event := range events {
		if event.Type != assistantDeltaEventType {
			firstNonDelta = index
			break
		}
	}
	if firstNonDelta < 1 {
		t.Fatalf("no delta batch was flushed before the lifecycle event: %#v", events)
	}
	for _, event := range events[firstNonDelta:] {
		if event.Type == assistantDeltaEventType {
			t.Fatalf("a delta batch was delivered after a non-delta event: %#v", events)
		}
	}
	if events[firstNonDelta].Type != "assistant.completed" {
		t.Fatalf("first non-delta event = %#v", events[firstNonDelta])
	}
	if deltaText(events) != "Смотрю файл." {
		t.Fatalf("flushed delta text = %q", deltaText(events))
	}
}

// TestChatEmitterKeepsOrderAcrossTimerFlushes exercises the timer goroutine and
// the run goroutine at the same time; it is the race-detector case for the
// batching machinery.
func TestChatEmitterKeepsOrderAcrossTimerFlushes(t *testing.T) {
	emitter, delivered := recordingEmitter(t)
	ctx := context.Background()
	var naive strings.Builder
	for round := 0; round < 4; round++ {
		for index := 0; index < 40; index++ {
			text := fmt.Sprintf("r%d-%d ", round, index)
			naive.WriteString(text)
			if err := emitter.Sink(ctx, agent.Event{Type: agent.EventModelTextDelta, ResponseID: "item-1", Text: text}); err != nil {
				t.Fatal(err)
			}
		}
		// Let the flush timer fire between rounds.
		time.Sleep(2 * assistantDeltaFlushInterval)
	}
	emitter.close(ctx)

	events := delivered()
	if countChatEvents(events, assistantDeltaEventType) < 2 {
		t.Fatalf("expected several timer flushes, got %#v", events)
	}
	if got := deltaText(events); got != naive.String() {
		t.Fatalf("timer flushes reordered or lost text:\n got %q\nwant %q", got, naive.String())
	}
	segments := emitter.AssistantSegments()
	if len(segments) != 1 || segments[0].Content != naive.String() {
		t.Fatalf("segment content = %#v", segments)
	}
}

func TestChatEmitterFinalizesCancelledRunExactlyOnce(t *testing.T) {
	emitter, delivered := recordingEmitter(t)
	if err := emitter.Sink(context.Background(), agent.Event{
		Type: agent.EventModelTextDelta, ResponseID: "item-1", Text: "Частичный ответ",
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	emitter.close(cancelled)
	// A second finalization must not produce a second terminal event.
	emitter.close(cancelled)

	events := delivered()
	if deltaText(events) != "Частичный ответ" {
		t.Fatalf("cancelled run lost its buffered delta: %#v", events)
	}
	if countChatEvents(events, "assistant.completed") != 1 {
		t.Fatalf("cancelled run did not close its assistant segment exactly once: %#v", events)
	}
	terminals := 0
	for _, event := range events {
		if event.Type != runCompletedEventType {
			continue
		}
		terminals++
		if event.Status != "cancelled" {
			t.Fatalf("terminal event status = %q, want cancelled", event.Status)
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal events = %d, want exactly one: %#v", terminals, events)
	}
}

// TestChatEmitterTerminalEventIsEmittedOnce covers the other half of the
// belt-and-braces guarantee: when the run already reported a terminal event the
// deferred finalization must stay silent.
func TestChatEmitterTerminalEventIsEmittedOnce(t *testing.T) {
	emitter, delivered := recordingEmitter(t)
	if !emitter.emitTerminal(ChatEvent{Type: runCompletedEventType, RunID: "run", Status: "complete"}) {
		t.Fatal("first terminal event was suppressed")
	}
	emitter.close(context.Background())
	events := delivered()
	if countChatEvents(events, runCompletedEventType) != 1 {
		t.Fatalf("terminal events = %#v", events)
	}
	if events[0].Status != "complete" {
		t.Fatalf("terminal status = %q, want the status the run reported", events[0].Status)
	}
}
