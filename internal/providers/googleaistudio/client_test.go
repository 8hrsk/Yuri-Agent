package googleaistudio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

var _ agent.ModelBackend = (*Client)(nil)

type signedLoopEchoTool struct{}

func (signedLoopEchoTool) Descriptor() agent.ToolDescriptor {
	return agent.ToolDescriptor{
		Name: "echo", Risk: domain.RiskLow,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
	}
}

func (signedLoopEchoTool) Execute(_ context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	return agent.ToolResult{Content: string(call.Arguments)}, nil
}

func googleTestConfig(server *httptest.Server) Config {
	return Config{
		APIKey: "AIza-test-secret-do-not-log", Model: "gemini-2.5-flash",
		TestBaseURL: server.URL + "/v1beta/openai/", HTTPClient: server.Client(),
		Timeout: 2 * time.Second, ClientVersion: "test",
	}
}

func googleTestRequest() agent.ModelRequest {
	return agent.ModelRequest{
		Model: "gemini-2.5-flash",
		Messages: []agent.Message{
			{Role: agent.RoleSystem, Content: "System rules."},
			{Role: agent.RoleUser, Content: "hello", Parts: []agent.ContentPart{{Type: agent.ContentPartImage, MediaType: "image/png", Data: "aW1hZ2U="}}},
		},
		Metadata: map[string]string{"slow_mode_priority": "maintenance"},
		Tools: []agent.ToolDescriptor{{
			Name: "echo", Description: "Echo a value", Risk: domain.RiskLow,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
		}},
	}
}

func TestStartUsesFixedGoogleCompatibilityContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1beta/openai/chat/completions" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if got := request.URL.RawQuery; got != "" {
			t.Fatalf("API key leaked into query: %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer AIza-test-secret-do-not-log" {
			t.Fatalf("authorization = %q", got)
		}
		if got := request.Header.Get("x-goog-api-client"); got != "ordoai-yuri/test" {
			t.Fatalf("x-goog-api-client = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "gemini-2.5-flash" || payload["stream"] != true {
			t.Fatalf("payload = %#v", payload)
		}
		if _, present := payload["metadata"]; present {
			t.Fatalf("local metadata leaked to Google compatibility payload: %#v", payload["metadata"])
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chat_1\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client, err := New(googleTestConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewBackend(googleTestConfig(server))
	if err != nil || backend == nil {
		t.Fatalf("NewBackend = %T, %v", backend, err)
	}
	stream, err := client.Start(context.Background(), googleTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var text string
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == agent.ModelEventTextDelta {
			text += event.Delta
		}
	}
	if text != "OK" {
		t.Fatalf("text = %q", text)
	}
	if config := client.Config(); config.APIKey != "" || config.TestBaseURL == "" {
		t.Fatalf("Config exposed unexpected values: %#v", config)
	}
}

func TestGemini3ToolLoopReturnsThoughtSignature(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = io.WriteString(writer, "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_signed\",\"extra_content\":{\"google\":{\"thought_signature\":\"opaque-signature\"}},\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"value\\\":\\\"ok\\\"}\"}}]}}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
			return
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) < 3 {
			t.Fatalf("continuation messages = %#v", payload["messages"])
		}
		assistant, _ := messages[len(messages)-2].(map[string]any)
		calls, _ := assistant["tool_calls"].([]any)
		call, _ := calls[0].(map[string]any)
		extra, _ := call["extra_content"].(map[string]any)
		google, _ := extra["google"].(map[string]any)
		if google["thought_signature"] != "opaque-signature" {
			t.Fatalf("thought signature was not returned: %#v", call)
		}
		_, _ = io.WriteString(writer, "data: {\"id\":\"chat_2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"}}]}\n\ndata: {\"id\":\"chat_2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":14,\"completion_tokens\":1,\"total_tokens\":15}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	config := googleTestConfig(server)
	config.Model = "gemini-3.8-flash"
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	registry := agent.NewToolRegistry()
	if err := registry.Register(signedLoopEchoTool{}); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewRuntime(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		RunID: "gemini-signed-loop",
		ModelRequest: agent.ModelRequest{
			Model:    "gemini-3.8-flash",
			Messages: []agent.Message{{Role: agent.RoleUser, Content: "use echo"}},
		},
		Budget: domain.RunBudget{MaxSteps: 2, MaxTokens: 100, MaxToolCalls: 1, MaxToolOutputBytes: 1000, MaxDurationSeconds: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.Content != "OK" || requests != 2 || result.Usage.TotalTokens != 27 {
		t.Fatalf("result=%#v requests=%d", result, requests)
	}
}

func TestListModelsUsesNativeEndpointAndDoesNotClaimFreeTier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1beta/models" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("pageSize") != "1000" || request.URL.Query().Get("key") != "" {
			t.Fatalf("query = %q", request.URL.RawQuery)
		}
		if got := request.Header.Get("x-goog-api-key"); got != "AIza-test-secret-do-not-log" {
			t.Fatalf("x-goog-api-key = %q", got)
		}
		if got := request.Header.Get("x-goog-api-client"); got != "ordoai-yuri/test" {
			t.Fatalf("x-goog-api-client = %q", got)
		}
		_, _ = io.WriteString(writer, `{"models":[{"name":"models/gemini-2.5-flash","displayName":"Gemini Flash","description":"Fast","version":"2.5","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent","countTokens"]},{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]}]}`)
	}))
	defer server.Close()

	client, err := New(googleTestConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %#v", models)
	}
	model := models[0]
	if model.ID != "gemini-2.5-flash" || !model.SupportsGenerateContent || !model.SupportsCountTokens || model.InputTokenLimit != 1048576 || model.OutputTokenLimit != 65536 {
		t.Fatalf("model = %#v", model)
	}
}

func TestCountTokensUsesNativePayloadAndStructuredErrors(t *testing.T) {
	var countCalls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1beta/models/gemini-2.5-flash:countTokens":
			countCalls++
			if request.Method != http.MethodPost || request.URL.RawQuery != "" {
				t.Fatalf("request = %s %s", request.Method, request.URL.String())
			}
			if request.Header.Get("x-goog-api-key") != "AIza-test-secret-do-not-log" || request.Header.Get("x-goog-api-client") != "ordoai-yuri/test" {
				t.Fatalf("headers = %#v", request.Header)
			}
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["systemInstruction"] == nil || payload["tools"] == nil {
				t.Fatalf("token payload missing system/tools: %#v", payload)
			}
			_, _ = io.WriteString(writer, `{"totalTokens":42,"cachedContentTokenCount":4,"toolUsePromptTokenCount":7}`)
		case "/v1beta/models/bad:countTokens":
			writer.Header().Set("Retry-After", "9")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(writer, `{"error":{"code":429,"message":"quota exhausted: AIza-test-secret-do-not-log","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXCEEDED"}]}}`)
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(googleTestConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	count, err := client.CountTokens(context.Background(), googleTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if count.TotalTokens != 42 || count.CachedContentTokenCount != 4 || count.ToolUsePromptTokenCount != 7 || countCalls != 1 {
		t.Fatalf("count = %#v calls=%d", count, countCalls)
	}
	_, err = client.CountTokens(context.Background(), agent.ModelRequest{Model: "bad", Messages: []agent.Message{{Role: agent.RoleUser, Content: "x"}}})
	if err == nil || strings.Contains(err.Error(), "AIza-test-secret-do-not-log") {
		t.Fatalf("error = %v", err)
	}
	var googleError *Error
	if !errors.As(err, &googleError) {
		t.Fatalf("error type = %T", err)
	}
	if googleError.Reason != ErrorReasonQuotaExhausted || googleError.RetryAfter != 9*time.Second {
		t.Fatalf("google error = %#v", googleError)
	}
	failure := googleError.InferenceFailure()
	if failure.Kind != domain.RunFailureQuotaExhausted || failure.Retryable {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestParseErrorDistinguishesRateLimitAndRejectsUnsafeTestEndpoint(t *testing.T) {
	rate := ParseError("count tokens", 429, []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"slow down","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`), http.Header{"Retry-After": []string{"3"}}, "")
	if rate.Reason != ErrorReasonRateLimit || rate.InferenceFailure().Kind != domain.RunFailureRateLimit || !rate.InferenceFailure().Retryable {
		t.Fatalf("rate error = %#v, failure=%#v", rate, rate.InferenceFailure())
	}
	if _, err := New(Config{TestBaseURL: "https://example.com/v1/openai"}); err == nil {
		t.Fatal("unsafe/non-v1beta test endpoint accepted")
	}
	if _, err := New(Config{TestBaseURL: "https://user:secret@example.com/v1beta/openai"}); err == nil {
		t.Fatal("credential-bearing test endpoint accepted")
	}
}
