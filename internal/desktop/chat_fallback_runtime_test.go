package desktop

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestChatUsesExplicitFallbackBeforeVisibleOutput(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int32
	primary := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(writer, `{"error":{"message":"primary unavailable"}}`)
	}))
	defer primary.Close()
	fallback := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		writeProviderProbeSSE(writer, flusher, "response.created", map[string]any{
			"type": "response.created", "response": map[string]any{"id": "response-fallback"},
		})
		writeProviderProbeSSE(writer, flusher, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "response_id": "response-fallback", "delta": "Ответ fallback.",
		})
		writeProviderProbeSSE(writer, flusher, "response.completed", map[string]any{
			"type": "response.completed", "response_id": "response-fallback",
			"response": map[string]any{"id": "response-fallback", "usage": map[string]any{"input_tokens": 4, "output_tokens": 3, "total_tokens": 7}},
		})
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer fallback.Close()

	bridge := newOpenAIBridgeSmoke(t, primary.URL+"/v1", "sk-primary-fallback-test")
	configureBridgeFallback(t, bridge, fallback.URL+"/v1")
	conversation, err := bridge.NewConversation("Явный fallback")
	if err != nil {
		t.Fatal(err)
	}
	result, err := bridge.SendMessage(ChatRequest{ConversationID: conversation.ID, Text: "Ответь коротко"})
	if err != nil || result.Status != "complete" {
		t.Fatalf("fallback result = %#v, err=%v", result, err)
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("provider calls primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
	startedIndex, fallbackIndex := -1, -1
	for index, event := range result.Events {
		switch event.Type {
		case "run.started":
			startedIndex = index
			if event.ProviderID != "bridge-smoke" {
				t.Fatalf("primary start attribution = %#v", event)
			}
		case "run.fallback":
			fallbackIndex = index
			if event.FromProviderID != "bridge-smoke" || event.ToProviderID != "fallback-provider" || event.ToModel != "fallback-model" {
				t.Fatalf("fallback event = %#v", event)
			}
		}
	}
	if startedIndex < 0 || fallbackIndex <= startedIndex {
		t.Fatalf("fallback lifecycle order = %#v", result.Events)
	}
	stored, err := bridge.repositories.Runs.Get(context.Background(), domain.ID(result.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Inference != (domain.RunInferenceRoute{ProviderID: "fallback-provider", Model: "fallback-model"}) || stored.InferenceRouteSwitches != 1 {
		t.Fatalf("fallback run attribution = %#v", stored)
	}
	audit, err := bridge.repositories.Audit.List(context.Background(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range audit {
		if event.RunID == stored.ID && event.Action == "inference.fallback" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("fallback audit missing: %#v", audit)
	}
}

func TestChatDoesNotFallbackAfterVisibleOutput(t *testing.T) {
	var fallbackCalls atomic.Int32
	primary := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		writeProviderProbeSSE(writer, flusher, "response.created", map[string]any{
			"type": "response.created", "response": map[string]any{"id": "response-partial"},
		})
		writeProviderProbeSSE(writer, flusher, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "response_id": "response-partial", "delta": "Частичный ответ",
		})
		_, _ = fmt.Fprint(writer, "data: {not-json}\n\n")
		flusher.Flush()
	}))
	defer primary.Close()
	fallback := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer fallback.Close()

	bridge := newOpenAIBridgeSmoke(t, primary.URL+"/v1", "sk-primary-visible-test")
	configureBridgeFallback(t, bridge, fallback.URL+"/v1")
	conversation, err := bridge.NewConversation("Без fallback после текста")
	if err != nil {
		t.Fatal(err)
	}
	result, err := bridge.SendMessage(ChatRequest{ConversationID: conversation.ID, Text: "Ответь"})
	if err != nil || result.Status != "error" {
		t.Fatalf("partial failure result = %#v, err=%v", result, err)
	}
	if fallbackCalls.Load() != 0 || hasChatEvent(result.Events, "run.fallback") {
		t.Fatalf("fallback ran after visible output: calls=%d events=%#v", fallbackCalls.Load(), result.Events)
	}
	stored, err := bridge.repositories.Runs.Get(context.Background(), domain.ID(result.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Inference.ProviderID != "bridge-smoke" || stored.InferenceRouteSwitches != 0 {
		t.Fatalf("partial run attribution changed = %#v", stored)
	}
}

func configureBridgeFallback(t *testing.T, bridge *Bridge, baseURL string) {
	t.Helper()
	const credentialRef = "provider.fallback-test.api-key"
	if err := bridge.keyring.Put(context.Background(), credentialRef, "sk-explicit-fallback-test"); err != nil {
		t.Fatal(err)
	}
	bridge.config.Providers = append(bridge.config.Providers, config.ProviderConfig{
		ID: "fallback-provider", Kind: config.ProviderOpenAICompatible, DisplayName: "Fallback Test",
		BaseURL: baseURL, Model: "fallback-model", CredentialRef: credentialRef,
	})
	if _, err := bridge.UpdateActiveAgentFallbackRoute(UpdateAgentFallbackRouteInput{
		Enabled: true, ProviderID: "fallback-provider", Model: "fallback-model",
	}); err != nil {
		t.Fatal(err)
	}
}
