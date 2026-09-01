package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func testRequest() agent.ModelRequest {
	return agent.ModelRequest{
		Model:    "test-model",
		Messages: []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
		Tools: []agent.ToolDescriptor{{
			Name: "echo", Description: "echo text", Risk: domain.RiskLow,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
		}},
	}
}

func TestResponsesStreamingTextAndToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %s, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload["stream"] != true || payload["model"] != "test-model" {
			t.Errorf("unexpected request fields: %#v", payload)
		}
		tools, ok := payload["tools"].([]any)
		if !ok || len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" {
			t.Errorf("unexpected tools: %#v", payload["tools"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEJSON(w, flusher, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1"}})
		writeSSEJSON(w, flusher, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "response_id": "resp_1", "delta": "I will echo. "})
		writeSSEJSON(w, flusher, "response.output_item.added", map[string]any{"type": "response.output_item.added", "response_id": "resp_1", "item": map[string]any{"type": "function_call", "id": "item_1", "call_id": "call_1", "name": "echo"}})
		writeSSEJSON(w, flusher, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "response_id": "resp_1", "item_id": "item_1", "delta": `{"value":"hello"}`})
		writeSSEJSON(w, flusher, "response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "response_id": "resp_1", "item_id": "item_1", "arguments": `{"value":"hello"}`})
		writeSSEJSON(w, flusher, "response.completed", map[string]any{"type": "response.completed", "response_id": "resp_1", "response": map[string]any{"id": "resp_1", "usage": map[string]any{"input_tokens": 3, "output_tokens": 5, "total_tokens": 8}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test-secret", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var events []agent.ModelEvent
	for {
		event, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		events = append(events, event)
	}
	if len(events) < 6 {
		t.Fatalf("events = %#v", events)
	}
	var foundText, foundStart, foundDelta, foundDone, foundCompleted bool
	for _, event := range events {
		switch event.Type {
		case agent.ModelEventTextDelta:
			foundText = event.Delta == "I will echo. "
		case agent.ModelEventToolCallStarted:
			foundStart = event.ToolCallID == "call_1" && event.ToolName == "echo"
		case agent.ModelEventToolCallDelta:
			foundDelta = event.ToolCallID == "call_1" && strings.Contains(event.ArgumentsDelta, `"value"`)
		case agent.ModelEventToolCallDone:
			foundDone = event.ToolCallID == "call_1" && strings.Contains(event.Arguments, "hello")
		case agent.ModelEventCompleted:
			foundCompleted = foundCompleted || event.Usage.TotalTokens == 8
		}
	}
	if !foundText || !foundStart || !foundDelta || !foundDone || !foundCompleted {
		t.Fatalf("missing normalized events: %#v", events)
	}
}

func TestChatCompletionsStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		options, ok := payload["stream_options"].(map[string]any)
		if payload["stream"] != true || !ok || options["include_usage"] != true {
			t.Errorf("unexpected request: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEJSON(w, flusher, "", chatChunk(map[string]any{"role": "assistant", "content": "Hi"}, nil))
		writeSSEJSON(w, flusher, "", chatChunk(map[string]any{"tool_calls": []any{chatToolCall(0, "call_chat", "echo", `{"value":`)}}, nil))
		writeSSEJSON(w, flusher, "", chatChunk(map[string]any{"tool_calls": []any{chatToolCall(0, "", "", `"ok"`)}}, nil))
		writeSSEJSON(w, flusher, "", chatChunk(map[string]any{}, "tool_calls"))
		writeSSEJSON(w, flusher, "", map[string]any{"id": "chat_1", "choices": []any{}, "usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 4, "total_tokens": 6}})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Style: APIStyleChatCompletions})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var sawText, sawTool, sawUsage bool
	for {
		event, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		sawText = sawText || event.Type == agent.ModelEventTextDelta && event.Delta == "Hi"
		sawTool = sawTool || event.Type == agent.ModelEventToolCallDelta && event.ToolCallID == "call_chat"
		sawUsage = sawUsage || event.Type == agent.ModelEventCompleted && event.Usage.TotalTokens == 6
	}
	if !sawText || !sawTool || !sawUsage {
		t.Fatalf("missing chat events: text=%v tool=%v usage=%v", sawText, sawTool, sawUsage)
	}
}

func TestNamedRequiredToolChoiceUsesProviderSpecificShape(t *testing.T) {
	for _, test := range []struct {
		name  string
		style APIStyle
		check func(*testing.T, map[string]any)
	}{
		{
			name: "responses", style: APIStyleResponses,
			check: func(t *testing.T, payload map[string]any) {
				choice, ok := payload["tool_choice"].(map[string]any)
				if !ok || choice["type"] != "function" || choice["name"] != "echo" {
					t.Fatalf("responses tool_choice = %#v", payload["tool_choice"])
				}
			},
		},
		{
			name: "chat", style: APIStyleChatCompletions,
			check: func(t *testing.T, payload map[string]any) {
				choice, ok := payload["tool_choice"].(map[string]any)
				function, functionOK := choice["function"].(map[string]any)
				if !ok || !functionOK || choice["type"] != "function" || function["name"] != "echo" {
					t.Fatalf("chat tool_choice = %#v", payload["tool_choice"])
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(Config{BaseURL: "https://example.invalid/v1", Style: test.style})
			if err != nil {
				t.Fatal(err)
			}
			request := testRequest()
			request.ToolChoice = agent.ToolChoice{Mode: agent.ToolChoiceRequired, Name: "echo"}
			body, _, err := client.marshalRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			test.check(t, payload)
		})
	}
}

func TestRetriesTransientResponsesBeforeReturningStream(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"temporary failure"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if got := calls.Load(); got != 3 {
		t.Fatalf("request count = %d, want 3", got)
	}
}

func TestProviderErrorsRedactCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key sk-super-secret-value"}}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "sk-super-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Start(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected provider error")
	}
	if strings.Contains(err.Error(), "sk-super-secret-value") || strings.Contains(err.Error(), "invalid api key") && !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("credential leaked in error: %v", err)
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != http.StatusUnauthorized || providerErr.Retryable {
		t.Fatalf("unexpected provider error: %#v", err)
	}
}

func TestCancellationInterruptsHTTPRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, startErr := client.Start(ctx, testRequest())
		result <- startErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not cancel")
	}
}

func TestStreamResponseLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"this is too large\"}\n\n")
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
	if err == nil {
		t.Fatal("expected response limit error")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorKindResponseLimit {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, eventName, data string) {
	if eventName != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", eventName)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func writeSSEJSON(w http.ResponseWriter, flusher http.Flusher, eventName string, value any) {
	data, _ := json.Marshal(value)
	writeSSE(w, flusher, eventName, string(data))
}

func chatChunk(delta map[string]any, finishReason any) map[string]any {
	choice := map[string]any{"index": 0, "delta": delta}
	if finishReason != nil {
		choice["finish_reason"] = finishReason
	}
	return map[string]any{"id": "chat_1", "choices": []any{choice}}
}

func chatToolCall(index int, id, name, arguments string) map[string]any {
	function := map[string]any{}
	if name != "" {
		function["name"] = name
	}
	if arguments != "" {
		function["arguments"] = arguments
	}
	call := map[string]any{"index": index, "function": function}
	if id != "" {
		call["id"] = id
	}
	return call
}
