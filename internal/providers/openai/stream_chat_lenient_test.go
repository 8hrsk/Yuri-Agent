package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// sseChat wraps chunk bodies into a Chat Completions SSE body with the terminal
// [DONE] marker, so a case is written as the chunks a gateway would send.
func sseChat(chunks ...string) string {
	var builder strings.Builder
	for _, chunk := range chunks {
		builder.WriteString("data: " + chunk + "\n\n")
	}
	builder.WriteString("data: [DONE]\n\n")
	return builder.String()
}

// TestChatDecodeToleratesGatewayScalarTypes pins the leniency line for the Chat
// Completions decoders. Every payload here is a shape an OpenAI-compatible
// gateway really emits and that the previous typed decode rejected wholesale --
// in the SSE path, by aborting the entire stream on one chunk. The expected
// values are the normalized contract, not merely "no error": a case that
// decoded but dropped the content would still fail.
func TestChatDecodeToleratesGatewayScalarTypes(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        collectedStream
	}{
		{
			name:        "sse numeric chunk id",
			contentType: "text/event-stream",
			body: sseChat(
				`{"id":12345,"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
				`{"id":12345,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
			want: collectedStream{completions: 2, text: "hi", responseID: "12345", finishReason: "stop"},
		},
		{
			name:        "json numeric response id",
			contentType: "application/json",
			body:        `{"id":12345,"choices":[{"index":0,"message":{"content":"hi"},"finish_reason":"stop"}]}`,
			want:        collectedStream{completions: 1, text: "hi", responseID: "12345", finishReason: "stop"},
		},
		{
			name:        "sse numeric tool call id",
			contentType: "text/event-stream",
			body: sseChat(
				`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":9,"function":{"name":"echo","arguments":"{\"v\":1}"}}]}}]}`,
				`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
			want: collectedStream{
				completions: 2, responseID: "c", finishReason: "tool_calls",
				toolCalls: []collectedToolCall{{id: "9", name: "echo", arguments: `{"v":1}`}},
			},
		},
		{
			name:        "json numeric tool call id",
			contentType: "application/json",
			body: `{"id":"c","choices":[{"index":0,"message":{"tool_calls":[{"id":9,` +
				`"function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			want: collectedStream{
				completions: 1, responseID: "c", finishReason: "tool_calls",
				toolCalls: []collectedToolCall{{id: "9", name: "echo", arguments: "{}"}},
			},
		},
		{
			// A gateway that inlines the decoded object instead of the
			// JSON-encoded string the contract asks for. The arguments must
			// reach the consumer as the equivalent JSON text.
			name:        "sse object valued arguments",
			contentType: "text/event-stream",
			body: sseChat(
				`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"echo","arguments":{"v":1}}}]}}]}`,
				`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
			want: collectedStream{
				completions: 2, responseID: "c", finishReason: "tool_calls",
				toolCalls: []collectedToolCall{{id: "call_a", name: "echo", arguments: `{"v":1}`}},
			},
		},
		{
			name:        "json object valued arguments",
			contentType: "application/json",
			body: `{"id":"c","choices":[{"index":0,"message":{"tool_calls":[{"id":"call_a",` +
				`"function":{"name":"echo","arguments":{"v":1}}}]},"finish_reason":"tool_calls"}]}`,
			want: collectedStream{
				completions: 1, responseID: "c", finishReason: "tool_calls",
				toolCalls: []collectedToolCall{{id: "call_a", name: "echo", arguments: `{"v":1}`}},
			},
		},
		{
			name:        "sse quoted choice and tool call index",
			contentType: "text/event-stream",
			body: sseChat(
				`{"id":"c","choices":[{"index":"0","delta":{"tool_calls":[{"index":"0","id":"call_a","function":{"name":"echo"}}]}}]}`,
				`{"id":"c","choices":[{"index":"0","delta":{"tool_calls":[{"index":"0","function":{"arguments":"{}"}}]}}]}`,
				`{"id":"c","choices":[{"index":"0","delta":{},"finish_reason":"tool_calls"}]}`),
			want: collectedStream{
				completions: 2, responseID: "c", finishReason: "tool_calls",
				// The second chunk carries no id: it must still correlate to
				// the first through the quoted index.
				toolCalls: []collectedToolCall{{id: "call_a", name: "echo", arguments: "{}"}},
			},
		},
		{
			name:        "sse float encoded usage",
			contentType: "text/event-stream",
			body: sseChat(
				`{"id":"c","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
				`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2.0,"completion_tokens":4.0,"total_tokens":6.0}}`),
			want: collectedStream{
				completions: 2, text: "hi", responseID: "c", finishReason: "stop",
				usage: agent.Usage{InputTokens: 2, OutputTokens: 4, TotalTokens: 6},
			},
		},
		{
			name:        "json quoted usage counters",
			contentType: "application/json",
			body: `{"id":"c","choices":[{"index":0,"message":{"content":"hi"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":"2","completion_tokens":"4","total_tokens":"6"}}`,
			want: collectedStream{
				completions: 1, text: "hi", responseID: "c", finishReason: "stop",
				usage: agent.Usage{InputTokens: 2, OutputTokens: 4, TotalTokens: 6},
			},
		},
		{
			// The non-streamed reader already accepted content parts; the
			// streaming reader rejected them. Both must accept them now.
			name:        "sse content part array in delta",
			contentType: "text/event-stream",
			body: sseChat(
				`{"id":"c","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"part one "},{"type":"text","text":"part two"}]}}]}`,
				`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
			want: collectedStream{completions: 2, text: "part one part two", responseID: "c", finishReason: "stop"},
		},
		{
			name:        "sse numeric function name and finish reason",
			contentType: "text/event-stream",
			body: sseChat(
				`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":7,"arguments":"{}"}}]}}]}`,
				`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":0}]}`),
			want: collectedStream{
				completions: 2, responseID: "c", finishReason: "0",
				toolCalls: []collectedToolCall{{id: "call_a", name: "7", arguments: "{}"}},
			},
		},
		{
			// A scalar content value is rendered rather than dropped: a
			// silently discarded delta is a truncated answer with no signal.
			name:        "sse scalar content is rendered not dropped",
			contentType: "text/event-stream",
			body: sseChat(
				`{"id":"c","choices":[{"index":0,"delta":{"content":42}}]}`,
				`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
			want: collectedStream{completions: 2, text: "42", responseID: "c", finishReason: "stop"},
		},
		{
			// Regression guard on the leniency: a role-only chunk still yields
			// nothing, and an unknown scalar role does not disturb the text.
			name:        "sse role only chunk stays inert",
			contentType: "text/event-stream",
			body: sseChat(
				`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
				`{"id":"c","choices":[{"index":0,"delta":{"role":5,"content":"hi"}}]}`,
				`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
			want: collectedStream{completions: 2, text: "hi", responseID: "c", finishReason: "stop"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream, cleanup := startAgainstBody(t, APIStyleChatCompletions, test.contentType, test.body)
			defer cleanup()
			assertCollected(t, collectStream(t, stream), test.want)
		})
	}
}

func TestChatStreamPreservesOpaqueToolCallExtraContent(t *testing.T) {
	body := sseChat(
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_signed","extra_content":{"google":{"thought_signature":"opaque-signature"}},"function":{"name":"echo","arguments":"{\"value\":\"ok\"}"}}]}}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	stream, cleanup := startAgainstBody(t, APIStyleChatCompletions, "text/event-stream", body)
	defer cleanup()

	var got json.RawMessage
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(event.ToolCallProviderExtras) > 0 {
			got = event.ToolCallProviderExtras
		}
	}
	want := `{"google":{"thought_signature":"opaque-signature"}}`
	if string(got) != want {
		t.Fatalf("provider extras = %s, want %s", got, want)
	}
}

// TestResponsesUsageToleratesNonIntegerCounters covers a gap on the Responses
// path itself: the typed int64 decode returned an all-zero Usage for a
// float-encoded or quoted counter, which silently disarmed the runtime token
// budget instead of reporting a smaller number.
func TestResponsesUsageToleratesNonIntegerCounters(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{
			name: "sse float usage", contentType: "text/event-stream",
			body: "data: {\"type\":\"response.completed\",\"response_id\":\"resp_1\"," +
				"\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":3.0,\"output_tokens\":5.0,\"total_tokens\":8.0}}}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name: "json quoted usage", contentType: "application/json",
			body: `{"id":"resp_1","output_text":"","usage":{"input_tokens":"3","output_tokens":"5","total_tokens":"8"}}`,
		},
	}
	want := agent.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream, cleanup := startAgainstBody(t, APIStyleResponses, test.contentType, test.body)
			defer cleanup()
			if got := collectStream(t, stream).usage; got != want {
				t.Fatalf("usage = %+v, want %+v", got, want)
			}
		})
	}
}

// TestChatDecodeRejectsStructurallyWrongPayloads is the other half of the
// leniency line. It is a preservation guard, not a negative control: it passes
// both before and after the hardening, on purpose, because widening the
// scalar leaves must not also widen the shapes. These payloads are not a differently-serialized leaf; their
// shape is not the contract, and no reading of them is better than a guess.
// Accepting them would emit a plausible but silently emptied answer, so they
// must stay a clean decode error naming the field that failed.
func TestChatDecodeRejectsStructurallyWrongPayloads(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantField   string
	}{
		{
			name: "sse choices is an object", contentType: "text/event-stream",
			body: sseChat(`{"id":"c","choices":{"index":0,"delta":{"content":"hi"}}}`), wantField: "choices",
		},
		{
			name: "json choices is an object", contentType: "application/json",
			body: `{"id":"c","choices":{"index":0,"message":{"content":"hi"}}}`, wantField: "choices",
		},
		{
			name: "sse delta is a string", contentType: "text/event-stream",
			body: sseChat(`{"id":"c","choices":[{"index":0,"delta":"hi"}]}`), wantField: "delta",
		},
		{
			name: "sse tool_calls is an object", contentType: "text/event-stream",
			body: sseChat(`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":{"id":"call_a"}}}]}`), wantField: "tool_calls",
		},
		{
			name: "sse function is a string", contentType: "text/event-stream",
			body: sseChat(`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":"echo"}]}}]}`), wantField: "function",
		},
		{
			name: "json message is an array", contentType: "application/json",
			body: `{"id":"c","choices":[{"index":0,"message":["hi"]}]}`, wantField: "message",
		},
		{
			// A top-level non-object names no field, so only the JSON type
			// error itself is asserted here.
			name: "sse chunk is not an object", contentType: "text/event-stream",
			body: sseChat(`["not","a","chunk"]`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream, cleanup := startAgainstBody(t, APIStyleChatCompletions, test.contentType, test.body)
			defer cleanup()
			_, err := stream.Recv(context.Background())
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %#v, want *ProviderError", err)
			}
			if providerErr.Kind != ErrorKindDecode {
				t.Fatalf("kind = %q, want %q (%v)", providerErr.Kind, ErrorKindDecode, err)
			}
			// Assert the specific decode failure, not merely that something
			// errored: a limit or transport error here would be the right
			// outcome for the wrong reason.
			if !strings.Contains(providerErr.Message, "cannot unmarshal") {
				t.Fatalf("message = %q, want a JSON type error", providerErr.Message)
			}
			if test.wantField != "" && !strings.Contains(providerErr.Message, test.wantField) {
				t.Fatalf("message = %q, want it to name %q", providerErr.Message, test.wantField)
			}
		})
	}
}

// TestChatStreamAbortsRatherThanSkippingAnUndecodableChunk fixes the streaming
// abort semantics in a test. A chunk that cannot be read even leniently must
// end the stream: skipping it would splice the deltas either side together and
// hand the user a fluent, plausible, silently incomplete answer.
func TestChatStreamAbortsRatherThanSkippingAnUndecodableChunk(t *testing.T) {
	stream, cleanup := startAgainstBody(t, APIStyleChatCompletions, "text/event-stream", sseChat(
		`{"id":"c","choices":[{"index":0,"delta":{"content":"first half "}}]}`,
		`{"id":"c","choices":{"broken":true}}`,
		`{"id":"c","choices":[{"index":0,"delta":{"content":"second half"}}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))
	defer cleanup()

	event, err := stream.Recv(context.Background())
	if err != nil || event.Type != agent.ModelEventTextDelta || event.Delta != "first half " {
		t.Fatalf("first event = %+v, err = %v; want the leading text delta", event, err)
	}
	_, err = stream.Recv(context.Background())
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %#v, want *ProviderError", err)
	}
	if providerErr.Kind != ErrorKindDecode || !strings.Contains(providerErr.Message, "cannot unmarshal") {
		t.Fatalf("error = %+v, want a decode type error", providerErr)
	}
	// The text after the bad chunk must not surface: the caller has been told
	// the stream failed, so it must not later look complete.
	next, err := stream.Recv(context.Background())
	if err == nil && next.Type == agent.ModelEventTextDelta && strings.Contains(next.Delta, "second half") {
		t.Fatalf("stream resumed after a decode failure and delivered %q", next.Delta)
	}
}

// TestChatStreamFailsClosedOnMidStreamErrorFrame covers the other direction of
// the same decision. An error frame used to decode to zero events and be
// skipped, so an upstream failure ended the stream as a short answer with no
// diagnosis; the non-streamed reader already failed closed here.
func TestChatStreamFailsClosedOnMidStreamErrorFrame(t *testing.T) {
	stream, cleanup := startAgainstBody(t, APIStyleChatCompletions, "text/event-stream", sseChat(
		`{"id":"c","choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		`{"error":{"message":"upstream exploded"}}`))
	defer cleanup()

	if event, err := stream.Recv(context.Background()); err != nil || event.Delta != "partial" {
		t.Fatalf("first event = %+v, err = %v; want the leading text delta", event, err)
	}
	_, err := stream.Recv(context.Background())
	if errors.Is(err, io.EOF) {
		t.Fatal("stream ended at EOF, hiding the upstream error behind a short answer")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %#v, want *ProviderError", err)
	}
	// Specific: the upstream message must survive, and the kind must be the
	// stream kind, not the decode kind -- the frame decoded fine.
	if providerErr.Kind != ErrorKindStream {
		t.Fatalf("kind = %q, want %q (%v)", providerErr.Kind, ErrorKindStream, err)
	}
	if !strings.Contains(providerErr.Message, "upstream exploded") {
		t.Fatalf("message = %q, want the upstream message", providerErr.Message)
	}
}

func assertCollected(t *testing.T, got, want collectedStream) {
	t.Helper()
	if got.started != want.started {
		t.Errorf("started = %v, want %v", got.started, want.started)
	}
	if got.completions != want.completions {
		t.Errorf("completions = %d, want %d", got.completions, want.completions)
	}
	if got.text != want.text {
		t.Errorf("text = %q, want %q", got.text, want.text)
	}
	if got.finishReason != want.finishReason {
		t.Errorf("finish reason = %q, want %q", got.finishReason, want.finishReason)
	}
	if got.responseID != want.responseID {
		t.Errorf("response id = %q, want %q", got.responseID, want.responseID)
	}
	if got.usage != want.usage {
		t.Errorf("usage = %+v, want %+v", got.usage, want.usage)
	}
	if len(got.toolCalls) != len(want.toolCalls) {
		t.Fatalf("tool calls = %+v, want %+v", got.toolCalls, want.toolCalls)
	}
	for i, call := range got.toolCalls {
		if call != want.toolCalls[i] {
			t.Errorf("tool call %d = %+v, want %+v", i, call, want.toolCalls[i])
		}
	}
}
