package openai

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// drainedStream is what a consumer of a Responses SSE stream actually observes:
// the ordered event types, the assembled text, and the terminal error. It is
// deliberately not the tidy collectedStream used by the happy-path tests --
// these cases are about a stream that must NOT terminate normally, so the
// terminal error is part of the observation.
type drainedStream struct {
	types []agent.ModelEventType
	text  string
	err   error
}

func (d drainedStream) has(want agent.ModelEventType) bool {
	for _, got := range d.types {
		if got == want {
			return true
		}
	}
	return false
}

func (d drainedStream) count(want agent.ModelEventType) int {
	total := 0
	for _, got := range d.types {
		if got == want {
			total++
		}
	}
	return total
}

func drainStream(t *testing.T, stream agent.ModelStream) drainedStream {
	t.Helper()
	var drained drainedStream
	for i := 0; ; i++ {
		if i > 200 {
			t.Fatal("stream did not terminate")
		}
		event, err := stream.Recv(context.Background())
		if err != nil {
			drained.err = err
			return drained
		}
		drained.types = append(drained.types, event.Type)
		drained.text += event.Delta
	}
}

// TestResponsesMalformedEventTypeCannotFabricateCompletion is the N-22
// regression. A frame whose "type" is not a string used to leave the decoded
// name empty, which routed the frame to the whole-body fallback -- and that
// fallback appends a completion unconditionally. The consumers that stop
// reading at a completion (internal/desktop/memory_extractor.go and
// reflection_model.go both `break` on agent.ModelEventCompleted) therefore
// committed a truncated answer as a whole one.
//
// The decisive assertion is the specific outcome: no completion is ever
// observed, and the stream ends in a typed decode error.
func TestResponsesMalformedEventTypeCannotFabricateCompletion(t *testing.T) {
	tests := []struct {
		name  string
		frame string
	}{
		{name: "numeric type", frame: `{"type":7,"response_id":"resp_1"}`},
		{name: "object type", frame: `{"type":{"name":"response.completed"},"response_id":"resp_1"}`},
		{name: "array type", frame: `{"type":["response.completed"],"response_id":"resp_1"}`},
		{name: "boolean type", frame: `{"type":true,"response_id":"resp_1"}`},
		{name: "numeric type carrying a body", frame: `{"type":7,"id":"resp_1","output_text":"half an answer"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\"Hello\"}\n\n" +
				"data: " + test.frame + "\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\" world\"}\n\n" +
				"data: [DONE]\n\n"
			stream, cleanup := startAgainstBody(t, APIStyleResponses, "text/event-stream", body)
			defer cleanup()

			drained := drainStream(t, stream)
			if drained.has(agent.ModelEventCompleted) {
				t.Fatalf("malformed frame fabricated a completion: events = %v", drained.types)
			}
			if drained.err == nil || errors.Is(drained.err, io.EOF) {
				t.Fatalf("stream ended with %v, want a typed provider error", drained.err)
			}
			var providerErr *ProviderError
			if !errors.As(drained.err, &providerErr) {
				t.Fatalf("error = %#v, want *ProviderError", drained.err)
			}
			if providerErr.Kind != ErrorKindDecode {
				t.Fatalf("kind = %q, want %q (%v)", providerErr.Kind, ErrorKindDecode, drained.err)
			}
			if providerErr.Message == "" {
				t.Error("provider error carries no message")
			}
			if drained.text != "Hello" {
				t.Fatalf("text = %q, want only the deltas that arrived before the bad frame", drained.text)
			}
		})
	}
}

// TestResponsesUnknownEventTypeIsIgnored fixes the other half of the decision:
// a well-formed but unrecognized type string is a forward-compatibility case,
// not a corruption case. Providers add event types over time, so an unknown
// name is skipped and the stream keeps going -- but it still must not
// manufacture a completion of its own.
func TestResponsesUnknownEventTypeIsIgnored(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\"Hello\"}\n\n" +
		"data: {\"type\":\"response.some_future_event.delta\",\"response_id\":\"resp_1\",\"delta\":\"ignored\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\" world\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response_id\":\"resp_1\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"data: [DONE]\n\n"
	stream, cleanup := startAgainstBody(t, APIStyleResponses, "text/event-stream", body)
	defer cleanup()

	drained := drainStream(t, stream)
	if !errors.Is(drained.err, io.EOF) {
		t.Fatalf("stream ended with %v, want io.EOF", drained.err)
	}
	if drained.text != "Hello world" {
		t.Fatalf("text = %q, want %q", drained.text, "Hello world")
	}
	// One from the response.completed frame, one from the [DONE] marker.
	if got := drained.count(agent.ModelEventCompleted); got != 2 {
		t.Fatalf("completions = %d, want 2 (events = %v)", got, drained.types)
	}
}

// TestResponsesTypelessFrameWithoutBodyEvidence covers the third case: a frame
// that carries no event name and no "type" at all, and does not look like a
// Responses body either. It is unrecognized rather than corrupt, so it is
// skipped -- and, again, it must not produce a completion.
func TestResponsesTypelessFrameWithoutBodyEvidence(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\"Hello\"}\n\n" +
		"data: {\"heartbeat\":1}\n\n" +
		"data: {\"type\":null,\"keepalive\":true}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\" world\"}\n\n" +
		"data: [DONE]\n\n"
	stream, cleanup := startAgainstBody(t, APIStyleResponses, "text/event-stream", body)
	defer cleanup()

	drained := drainStream(t, stream)
	if !errors.Is(drained.err, io.EOF) {
		t.Fatalf("stream ended with %v, want io.EOF", drained.err)
	}
	if drained.text != "Hello world" {
		t.Fatalf("text = %q, want %q", drained.text, "Hello world")
	}
	if got := drained.count(agent.ModelEventCompleted); got != 1 {
		t.Fatalf("completions = %d, want 1 from the [DONE] marker only (events = %v)", got, drained.types)
	}
}

// TestResponsesTypelessWholeBodyFrameStillReadable is a PRESERVATION GUARD, not
// a negative control: it must pass both before and after the N-22 fix. Some
// gateways answer a streaming request with text/event-stream framing but put
// the entire non-streamed Responses body in a single typeless frame. That is
// the one live path that legitimately reaches the whole-body fallback with an
// empty type name, and the fix must not close it.
//
// It asserts only the observable contract -- the text reaches the consumer and
// the frame still completes -- so it is coupled neither to which internal
// function handles the frame nor to anything the fix improves.
func TestResponsesTypelessWholeBodyFrameStillReadable(t *testing.T) {
	for _, test := range []struct {
		name  string
		frame string
	}{
		{name: "output_text shortcut", frame: `{"id":"resp_1","object":"response","status":"completed","output_text":"whole body"}`},
		{
			name:  "output array",
			frame: `{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"whole body"}]}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := "data: " + test.frame + "\n\ndata: [DONE]\n\n"
			stream, cleanup := startAgainstBody(t, APIStyleResponses, "text/event-stream", body)
			defer cleanup()

			collected := collectStream(t, stream)
			if collected.text != "whole body" {
				t.Fatalf("text = %q, want %q", collected.text, "whole body")
			}
			if collected.completions == 0 {
				t.Fatal("a whole-body frame produced no completion")
			}
		})
	}
}

// TestResponsesTypelessFailureBodyDoesNotComplete closes the last fabrication
// route through the whole-body fallback: a typeless frame carrying a failure
// must surface the failure rather than append a completion over it.
func TestResponsesTypelessFailureBodyDoesNotComplete(t *testing.T) {
	tests := []struct {
		name  string
		frame string
	}{
		{name: "error envelope", frame: `{"id":"resp_1","object":"response","error":{"message":"upstream refused"}}`},
		{name: "failed status", frame: `{"id":"resp_1","object":"response","status":"failed","message":"model overloaded"}`},
		{name: "incomplete status", frame: `{"id":"resp_1","object":"response","status":"incomplete","output_text":"half"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := "data: " + test.frame + "\n\ndata: [DONE]\n\n"
			stream, cleanup := startAgainstBody(t, APIStyleResponses, "text/event-stream", body)
			defer cleanup()

			drained := drainStream(t, stream)
			if drained.has(agent.ModelEventCompleted) {
				t.Fatalf("a failure frame fabricated a completion: events = %v", drained.types)
			}
			var providerErr *ProviderError
			if !errors.As(drained.err, &providerErr) {
				t.Fatalf("error = %#v, want *ProviderError", drained.err)
			}
			if providerErr.Kind != ErrorKindStream {
				t.Fatalf("kind = %q, want %q (%v)", providerErr.Kind, ErrorKindStream, drained.err)
			}
		})
	}
}
