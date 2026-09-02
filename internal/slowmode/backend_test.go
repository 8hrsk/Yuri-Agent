package slowmode

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

func TestBackendCloseReleasesSlotReconcilesUsageAndPreservesInteractive(t *testing.T) {
	coordinator := newTestCoordinator(t, Limits{TPM: 10, MaxConcurrent: 1}, nil)
	transport := &stubBackend{stream: &stubInteractiveStream{events: []agent.ModelEvent{
		{Type: agent.ModelEventCompleted, Usage: agent.Usage{InputTokens: 3}},
	}}}
	backend := Backend{
		Backend: transport, Coordinator: coordinator,
		Estimate: func(context.Context, agent.ModelRequest) (int64, error) { return 8, nil },
	}
	stream, err := backend.Start(context.Background(), validModelRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.(agent.InteractiveToolStream); !ok {
		t.Fatal("interactive stream capability was erased")
	}
	if _, err = stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := coordinator.Snapshot(context.Background())
	if snapshot.Active != 0 || snapshot.WindowTokens != 3 {
		t.Fatalf("snapshot after close = %+v", snapshot)
	}
	second, err := coordinator.Admit(context.Background(), Request{InputTokens: 7})
	if err != nil {
		t.Fatalf("slot/token reconciliation did not permit next request: %v", err)
	}
	_ = second.Finish(context.Background(), Outcome{})
}

func TestBackendStartErrorAppliesFeedbackAndReleasesSlot(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	coordinator := newTestCoordinator(t, Limits{MaxConcurrent: 1}, clock)
	rateError := errors.New("sanitized 429")
	backend := Backend{
		Backend: stubBackend{err: rateError}, Coordinator: coordinator,
		Estimate: func(context.Context, agent.ModelRequest) (int64, error) { return 1, nil },
		Feedback: func(err error) (Feedback, bool) {
			return Feedback{Kind: FeedbackShortWindow, RetryAfter: 3 * time.Second}, errors.Is(err, rateError)
		},
	}
	if _, err := backend.Start(context.Background(), validModelRequest()); !errors.Is(err, rateError) {
		t.Fatalf("Start() error = %v", err)
	}
	snapshot, _ := coordinator.Snapshot(context.Background())
	if snapshot.Active != 0 || snapshot.CooldownUntil.Sub(clock.Now()) != 3*time.Second {
		t.Fatalf("snapshot after start error = %+v", snapshot)
	}
}

func TestBackendStreamErrorAppliesFeedbackOnce(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	coordinator := newTestCoordinator(t, Limits{MaxConcurrent: 1}, clock)
	rateError := errors.New("sanitized streaming 429")
	transport := &stubBackend{stream: &errorStream{err: rateError}}
	backend := Backend{
		Backend: transport, Coordinator: coordinator,
		Estimate: func(context.Context, agent.ModelRequest) (int64, error) { return 1, nil },
		Feedback: func(err error) (Feedback, bool) {
			return Feedback{Kind: FeedbackAmbiguous, RetryAfter: 2 * time.Second}, errors.Is(err, rateError)
		},
	}
	stream, err := backend.Start(context.Background(), validModelRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(context.Background()); !errors.Is(err, rateError) {
		t.Fatalf("Recv() error = %v", err)
	}
	if _, err = stream.Recv(context.Background()); !errors.Is(err, rateError) {
		t.Fatalf("second Recv() error = %v", err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := coordinator.Snapshot(context.Background())
	if snapshot.Active != 0 || snapshot.AdaptiveLevel != 1 || snapshot.CooldownUntil.Sub(clock.Now()) != 2*time.Second {
		t.Fatalf("snapshot after streaming error = %+v", snapshot)
	}
}

func TestBackendRejectsNilStreamWithoutHoldingSlot(t *testing.T) {
	coordinator := newTestCoordinator(t, Limits{MaxConcurrent: 1}, nil)
	backend := Backend{
		Backend: stubBackend{}, Coordinator: coordinator,
		Estimate: func(context.Context, agent.ModelRequest) (int64, error) { return 1, nil },
	}
	if _, err := backend.Start(context.Background(), validModelRequest()); !errors.Is(err, agent.ErrBackend) {
		t.Fatalf("Start() error = %v", err)
	}
	snapshot, _ := coordinator.Snapshot(context.Background())
	if snapshot.Active != 0 {
		t.Fatalf("active = %d after nil stream", snapshot.Active)
	}
}

func validModelRequest() agent.ModelRequest {
	return agent.ModelRequest{Model: "model", Messages: []agent.Message{{Role: agent.RoleUser, Content: "hello"}}}
}

type stubBackend struct {
	stream agent.ModelStream
	err    error
}

func (backend stubBackend) Start(context.Context, agent.ModelRequest) (agent.ModelStream, error) {
	return backend.stream, backend.err
}

type stubInteractiveStream struct {
	events []agent.ModelEvent
	index  int
	closed int
}

type errorStream struct{ err error }

func (stream *errorStream) Recv(context.Context) (agent.ModelEvent, error) {
	return agent.ModelEvent{}, stream.err
}

func (stream *errorStream) Close() error { return nil }

func (stream *stubInteractiveStream) Recv(context.Context) (agent.ModelEvent, error) {
	if stream.index >= len(stream.events) {
		return agent.ModelEvent{}, io.EOF
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}

func (stream *stubInteractiveStream) Close() error {
	stream.closed++
	return nil
}

func (stream *stubInteractiveStream) RespondToolResult(context.Context, string, agent.ToolResult) error {
	return nil
}
