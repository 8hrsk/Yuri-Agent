package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// collectedStream is the normalized shape a runtime consumer observes. Both the
// SSE path and the non-streamed JSON fallback must produce the same values.
type collectedStream struct {
	started      bool
	completions  int
	text         string
	finishReason string
	responseID   string
	usage        agent.Usage
	toolCalls    []collectedToolCall
}

type collectedToolCall struct {
	id        string
	name      string
	arguments string
}

func collectStream(t *testing.T, stream agent.ModelStream) collectedStream {
	t.Helper()
	var collected collectedStream
	order := make([]string, 0, 4)
	byID := make(map[string]*collectedToolCall)
	for i := 0; ; i++ {
		if i > 200 {
			t.Fatal("stream did not terminate")
		}
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if collected.responseID == "" {
			collected.responseID = event.ResponseID
		}
		collected.usage = collected.usage.Add(event.Usage)
		switch event.Type {
		case agent.ModelEventStarted:
			collected.started = true
		case agent.ModelEventTextDelta:
			collected.text += event.Delta
		case agent.ModelEventCompleted:
			collected.completions++
			if event.FinishReason != "" {
				collected.finishReason = event.FinishReason
			}
		case agent.ModelEventToolCallStarted, agent.ModelEventToolCallDelta, agent.ModelEventToolCallDone:
			call := byID[event.ToolCallID]
			if call == nil {
				call = &collectedToolCall{id: event.ToolCallID}
				byID[event.ToolCallID] = call
				order = append(order, event.ToolCallID)
			}
			if event.ToolName != "" {
				call.name = event.ToolName
			}
			if event.Arguments != "" {
				call.arguments = event.Arguments
			}
			if event.ArgumentsDelta != "" {
				call.arguments += event.ArgumentsDelta
			}
		default:
			t.Fatalf("unexpected event type %q", event.Type)
		}
	}
	for _, id := range order {
		collected.toolCalls = append(collected.toolCalls, *byID[id])
	}
	return collected
}

func startAgainstBody(t *testing.T, style APIStyle, contentType, body string) (agent.ModelStream, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, body)
	}))
	client, err := New(Config{BaseURL: server.URL, Style: style, Model: "test-model"})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return stream, func() {
		_ = stream.Close()
		server.Close()
	}
}

// TestNonStreamedJSONMatchesSSEContract covers the gateway that ignores
// stream=true and answers with a plain application/json body. The SSE cases are
// the regression guard: an equivalent payload must normalize identically.
func TestNonStreamedJSONMatchesSSEContract(t *testing.T) {
	tests := []struct {
		name        string
		style       APIStyle
		contentType string
		body        string
		want        collectedStream
	}{
		{
			name:        "responses json output array",
			style:       APIStyleResponses,
			contentType: "application/json",
			body: `{"id":"resp_1","object":"response","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[
					{"type":"output_text","text":"Hello "},
					{"type":"output_text","text":"world"}]}],
				"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`,
			want: collectedStream{
				started: true, completions: 1, text: "Hello world", responseID: "resp_1",
				usage: agent.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
			},
		},
		{
			name:        "responses sse output text",
			style:       APIStyleResponses,
			contentType: "text/event-stream",
			body: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\"Hello \"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\"world\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response_id\":\"resp_1\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"total_tokens\":8}}}\n\n" +
				"data: [DONE]\n\n",
			// The SSE path emits a second, empty completion for the terminal
			// [DONE] marker; the JSON body has no such marker.
			want: collectedStream{
				started: true, completions: 2, text: "Hello world", responseID: "resp_1",
				usage: agent.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
			},
		},
		{
			name:        "responses json output_text shortcut",
			style:       APIStyleResponses,
			contentType: "application/json; charset=utf-8",
			body:        `{"id":"resp_2","output_text":"compact body","usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			want: collectedStream{
				started: true, completions: 1, text: "compact body", responseID: "resp_2",
				usage: agent.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
			},
		},
		{
			name:        "responses json without trailing newline",
			style:       APIStyleResponses,
			contentType: "application/json",
			body:        `{"id":"resp_3","output_text":"no newline"}`,
			want: collectedStream{
				started: true, completions: 1, text: "no newline", responseID: "resp_3",
			},
		},
		{
			name:        "responses json with trailing newline",
			style:       APIStyleResponses,
			contentType: "application/json",
			body:        "{\"id\":\"resp_3\",\"output_text\":\"trailing newline\"}\n",
			want: collectedStream{
				started: true, completions: 1, text: "trailing newline", responseID: "resp_3",
			},
		},
		{
			name:        "responses json wrapped in response envelope",
			style:       APIStyleResponses,
			contentType: "application/json",
			body:        `{"response":{"id":"resp_4","output_text":"wrapped","usage":{"input_tokens":2,"output_tokens":2,"total_tokens":4}}}`,
			want: collectedStream{
				started: true, completions: 1, text: "wrapped", responseID: "resp_4",
				usage: agent.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
			},
		},
		{
			name:        "responses json function call",
			style:       APIStyleResponses,
			contentType: "application/json",
			body: `{"id":"resp_5","status":"completed","output":[
				{"type":"message","content":[{"type":"output_text","text":"calling"}]},
				{"type":"function_call","id":"item_1","call_id":"call_1","name":"echo","arguments":"{\"value\":\"hello\"}"}],
				"usage":{"input_tokens":4,"output_tokens":6,"total_tokens":10}}`,
			want: collectedStream{
				started: true, completions: 1, text: "calling", responseID: "resp_5",
				usage:     agent.Usage{InputTokens: 4, OutputTokens: 6, TotalTokens: 10},
				toolCalls: []collectedToolCall{{id: "call_1", name: "echo", arguments: `{"value":"hello"}`}},
			},
		},
		{
			name:        "chat json message",
			style:       APIStyleChatCompletions,
			contentType: "application/json",
			body: `{"id":"chat_1","object":"chat.completion","choices":[
				{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":2,"completion_tokens":4,"total_tokens":6}}`,
			want: collectedStream{
				completions: 1, text: "Hi there", responseID: "chat_1", finishReason: "stop",
				usage: agent.Usage{InputTokens: 2, OutputTokens: 4, TotalTokens: 6},
			},
		},
		{
			name:        "chat sse message",
			style:       APIStyleChatCompletions,
			contentType: "text/event-stream",
			body: "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi there\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":4,\"total_tokens\":6}}\n\n" +
				"data: [DONE]\n\n",
			want: collectedStream{
				completions: 2, text: "Hi there", responseID: "chat_1", finishReason: "stop",
				usage: agent.Usage{InputTokens: 2, OutputTokens: 4, TotalTokens: 6},
			},
		},
		{
			name:        "chat json tool calls",
			style:       APIStyleChatCompletions,
			contentType: "application/json",
			body: `{"id":"chat_2","choices":[{"index":0,"message":{"role":"assistant","content":null,
				"tool_calls":[{"id":"call_chat","type":"function","function":{"name":"echo","arguments":"{\"value\":\"ok\"}"}}]},
				"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`,
			want: collectedStream{
				completions: 1, responseID: "chat_2", finishReason: "tool_calls",
				usage:     agent.Usage{InputTokens: 5, OutputTokens: 7, TotalTokens: 12},
				toolCalls: []collectedToolCall{{id: "call_chat", name: "echo", arguments: `{"value":"ok"}`}},
			},
		},
		{
			name:        "chat json content parts",
			style:       APIStyleChatCompletions,
			contentType: "application/json",
			body: `{"id":"chat_3","choices":[{"index":0,"message":{"role":"assistant",
				"content":[{"type":"text","text":"part one "},{"type":"text","text":"part two"}]},"finish_reason":"stop"}]}`,
			want: collectedStream{
				completions: 1, text: "part one part two", responseID: "chat_3", finishReason: "stop",
			},
		},
		{
			name:        "chat json tool call without id falls back to ordinal",
			style:       APIStyleChatCompletions,
			contentType: "application/json",
			body: `{"id":"chat_4","choices":[{"index":0,"message":{"role":"assistant",
				"tool_calls":[{"type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			want: collectedStream{
				completions: 1, responseID: "chat_4", finishReason: "tool_calls",
				toolCalls: []collectedToolCall{{id: ordinalID(0), name: "echo", arguments: "{}"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream, cleanup := startAgainstBody(t, test.style, test.contentType, test.body)
			defer cleanup()
			got := collectStream(t, stream)
			if got.started != test.want.started {
				t.Errorf("started = %v, want %v", got.started, test.want.started)
			}
			if got.completions != test.want.completions {
				t.Errorf("completions = %d, want %d", got.completions, test.want.completions)
			}
			if got.text != test.want.text {
				t.Errorf("text = %q, want %q", got.text, test.want.text)
			}
			if got.finishReason != test.want.finishReason {
				t.Errorf("finish reason = %q, want %q", got.finishReason, test.want.finishReason)
			}
			if got.responseID != test.want.responseID {
				t.Errorf("response id = %q, want %q", got.responseID, test.want.responseID)
			}
			if got.usage != test.want.usage {
				t.Errorf("usage = %+v, want %+v", got.usage, test.want.usage)
			}
			if len(got.toolCalls) != len(test.want.toolCalls) {
				t.Fatalf("tool calls = %+v, want %+v", got.toolCalls, test.want.toolCalls)
			}
			for i, call := range got.toolCalls {
				if call != test.want.toolCalls[i] {
					t.Errorf("tool call %d = %+v, want %+v", i, call, test.want.toolCalls[i])
				}
			}
			// The body carries exactly one response: the stream must stay at EOF.
			if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
				t.Errorf("second EOF = %v, want io.EOF", err)
			}
		})
	}
}

// TestNonStreamedJSONFailureModes asserts that an unusable body produces a
// typed provider error rather than a panic or a silent empty stream.
func TestNonStreamedJSONFailureModes(t *testing.T) {
	tests := []struct {
		name        string
		style       APIStyle
		contentType string
		body        string
		wantKind    ErrorKind
	}{
		{name: "responses empty body", style: APIStyleResponses, contentType: "application/json", body: "", wantKind: ErrorKindDecode},
		{name: "responses whitespace body", style: APIStyleResponses, contentType: "application/json", body: "  \n\t ", wantKind: ErrorKindDecode},
		{name: "responses truncated json", style: APIStyleResponses, contentType: "application/json", body: `{"id":"resp_1","output_text":`, wantKind: ErrorKindDecode},
		{name: "responses non object json", style: APIStyleResponses, contentType: "application/json", body: `["not","an","object"]`, wantKind: ErrorKindDecode},
		{name: "responses plain text body", style: APIStyleResponses, contentType: "text/plain", body: "gateway is warming up", wantKind: ErrorKindDecode},
		{name: "chat empty body", style: APIStyleChatCompletions, contentType: "application/json", body: "", wantKind: ErrorKindDecode},
		{name: "chat truncated json", style: APIStyleChatCompletions, contentType: "application/json", body: `{"id":"chat_1","choices":[`, wantKind: ErrorKindDecode},
		{
			name: "responses error envelope with 200", style: APIStyleResponses, contentType: "application/json",
			body: `{"error":{"message":"upstream refused"}}`, wantKind: ErrorKindStream,
		},
		{
			name: "responses failed status", style: APIStyleResponses, contentType: "application/json",
			body: `{"id":"resp_1","status":"failed","error":null,"message":"model overloaded"}`, wantKind: ErrorKindStream,
		},
		{
			name: "chat error envelope with 200", style: APIStyleChatCompletions, contentType: "application/json",
			body: `{"error":{"message":"upstream refused"}}`, wantKind: ErrorKindStream,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream, cleanup := startAgainstBody(t, test.style, test.contentType, test.body)
			defer cleanup()
			_, err := stream.Recv(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, io.EOF) {
				t.Fatalf("got io.EOF, want a typed provider error")
			}
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %#v, want *ProviderError", err)
			}
			if providerErr.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q (%v)", providerErr.Kind, test.wantKind, err)
			}
			if providerErr.Message == "" {
				t.Error("provider error carries no message")
			}
		})
	}
}

// TestNonStreamedJSONRespectsResponseLimit keeps the byte budget identical to
// the SSE path so an oversized fallback body cannot be read unbounded.
func TestNonStreamedJSONRespectsResponseLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","output_text":"this body is far larger than the configured limit"}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, MaxResponseBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv(context.Background())
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorKindResponseLimit {
		t.Fatalf("error = %#v, want response limit", err)
	}
}

// TestNonStreamedJSONRedactsCredentials guards the fallback error path against
// echoing the API key back through a decode failure message.
func TestNonStreamedJSONRedactsCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"message":"rejected key sk-super-secret-value"}}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "sk-super-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); strings.Contains(got, "sk-super-secret-value") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("error did not redact the credential: %q", got)
	}
}
