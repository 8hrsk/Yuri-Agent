package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/observability"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	securitykeyring "github.com/OrdoAI/yuri-agent/internal/security/keyring"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func TestOpenAIProviderBridgeLifecycleSmoke(t *testing.T) {
	t.Run("streaming persists transcript", func(t *testing.T) {
		const secret = "sk-bridge-streaming-canary"
		server := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/v1/responses" || request.Header.Get("Authorization") != "Bearer "+secret {
				http.Error(writer, "unexpected request", http.StatusBadRequest)
				return
			}
			var payload struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Model != "bridge-smoke-model" {
				http.Error(writer, "unexpected payload", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			flusher := writer.(http.Flusher)
			writeProviderProbeSSE(writer, flusher, "response.created", map[string]any{
				"type": "response.created", "response": map[string]any{"id": "response-bridge-success"},
			})
			writeProviderProbeSSE(writer, flusher, "response.output_text.delta", map[string]any{
				"type": "response.output_text.delta", "response_id": "response-bridge-success", "delta": "Привет, ",
			})
			writeProviderProbeSSE(writer, flusher, "response.output_text.delta", map[string]any{
				"type": "response.output_text.delta", "response_id": "response-bridge-success", "delta": "хозяин.",
			})
			writeProviderProbeSSE(writer, flusher, "response.completed", map[string]any{
				"type": "response.completed", "response_id": "response-bridge-success",
				"response": map[string]any{"id": "response-bridge-success", "usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}},
			})
			_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
			flusher.Flush()
		}))
		defer server.Close()

		bridge := newOpenAIBridgeSmoke(t, server.URL+"/v1", secret)
		result, err := bridge.SendMessage(ChatRequest{ConversationID: "conversation-bridge-stream", Text: "Поздоровайся"})
		if err != nil || result.Status != "complete" {
			t.Fatalf("streaming Bridge result = %#v err=%v", result, err)
		}
		// Text deltas are a live-only channel now: returning them a second time
		// in the result made the bridge carry every answer twice. The result
		// keeps the lifecycle events, and the streamed text is asserted through
		// the durable transcript below.
		for _, event := range result.Events {
			if event.Type == "assistant.delta" {
				t.Fatalf("result payload repeated the delta stream: %#v", event)
			}
		}
		if !hasChatEvent(result.Events, "run.started") || !hasChatEvent(result.Events, "run.completed") {
			t.Fatalf("result lost its lifecycle events: %#v", result.Events)
		}
		conversations, err := bridge.ListConversations()
		if err != nil || len(conversations) != 1 || len(conversations[0].Messages) != 2 ||
			conversations[0].Messages[1].Content != "Привет, хозяин." ||
			conversations[0].Messages[1].RunID != result.RunID {
			t.Fatalf("durable Bridge transcript = %#v err=%v", conversations, err)
		}
	})

	t.Run("cancel active stream", func(t *testing.T) {
		started := make(chan struct{})
		var startedOnce sync.Once
		server := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			flusher := writer.(http.Flusher)
			writeProviderProbeSSE(writer, flusher, "response.created", map[string]any{
				"type": "response.created", "response": map[string]any{"id": "response-bridge-cancel"},
			})
			startedOnce.Do(func() { close(started) })
			<-request.Context().Done()
		}))
		defer server.Close()

		bridge := newOpenAIBridgeSmoke(t, server.URL+"/v1", "sk-bridge-cancel-canary")
		type outcome struct {
			result ChatRunResult
			err    error
		}
		finished := make(chan outcome, 1)
		go func() {
			result, err := bridge.SendMessage(ChatRequest{ConversationID: "conversation-bridge-cancel", Text: "Долгий ответ"})
			finished <- outcome{result: result, err: err}
		}()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("fake provider stream did not start")
		}
		runID := waitForActiveBridgeRun(t, bridge, 2*time.Second)
		if err := bridge.CancelRun(runID); err != nil {
			t.Fatalf("cancel Bridge run: %v", err)
		}
		select {
		case value := <-finished:
			if value.err != nil || value.result.Status != "cancelled" {
				t.Fatalf("cancelled Bridge result = %#v err=%v", value.result, value.err)
			}
			storedRun, err := bridge.repositories.Runs.Get(context.Background(), domain.ID(runID))
			if err != nil || storedRun.State != domain.RunStateCancelled {
				t.Fatalf("durable cancelled run = %#v err=%v", storedRun, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Bridge run did not stop after cancellation")
		}
	})

	t.Run("provider error is safe", func(t *testing.T) {
		const secret = "sk-bridge-error-canary"
		server := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(writer, `{"error":{"message":"upstream leaked secret %s"}}`, secret)
		}))
		defer server.Close()

		bridge := newOpenAIBridgeSmoke(t, server.URL+"/v1", secret)
		result, err := bridge.SendMessage(ChatRequest{ConversationID: "conversation-bridge-error", Text: "Проверь ошибку"})
		if err != nil || result.Status != "error" || len(result.Events) == 0 {
			t.Fatalf("provider error Bridge result = %#v err=%v", result, err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) || strings.Contains(strings.ToLower(string(encoded)), "upstream leaked") {
			t.Fatalf("provider error leaked sensitive upstream content: %s", encoded)
		}
	})
}

func newOpenAIBridgeSmoke(t *testing.T, baseURL, secret string) *Bridge {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{
		ConfigDirectory: filepath.Join(root, "config"), ConfigFile: filepath.Join(root, "config", "config.json"),
		DataDirectory: filepath.Join(root, "data"), DatabaseFile: filepath.Join(root, "data", "yuri.sqlite3"),
		BlobDirectory: filepath.Join(root, "data", "blobs"), LogDirectory: filepath.Join(root, "data", "logs"),
		PluginDirectory: filepath.Join(root, "data", "plugins"), PebbleDirectory: filepath.Join(root, "data", "pebble"),
	}
	database, err := storage.Open(context.Background(), paths.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	keyringBackend := &providerTestKeyring{values: make(map[string]string)}
	credentialStore, err := securitykeyring.NewWithBackend("ai.ordo.yuri.bridge-smoke", keyringBackend)
	if err != nil {
		t.Fatal(err)
	}
	const credentialRef = "provider.bridge-smoke.api-key"
	if err := credentialStore.Put(context.Background(), credentialRef, secret); err != nil {
		t.Fatal(err)
	}
	value := config.Default(paths)
	value.Persona.AutoEvolution = false
	value.Providers = []config.ProviderConfig{{
		ID: "bridge-smoke", Kind: config.ProviderOpenAICompatible, DisplayName: "Local Bridge Smoke",
		BaseURL: baseURL, Model: "bridge-smoke-model", CredentialRef: credentialRef, Enabled: true,
	}}
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	bridge := &Bridge{
		logger:   observability.NewLogger(observability.LoggerOptions{Level: slog.LevelInfo, Format: "json", Output: io.Discard}),
		database: database, repositories: repositories, paths: paths, config: value, keyring: credentialStore,
		activeRuns: make(map[string]context.CancelFunc), approvals: make(map[string]*approvalGate),
		backgroundCtx: backgroundCtx, backgroundCancel: backgroundCancel,
		pluginSupervisors: make(map[string]*plugins.Supervisor), shuttingDown: true,
	}
	if err := bridge.ensurePersonaState(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backgroundCancel)
	return bridge
}

func waitForActiveBridgeRun(t *testing.T, bridge *Bridge, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		bridge.mu.RLock()
		for runID := range bridge.activeRuns {
			bridge.mu.RUnlock()
			return runID
		}
		bridge.mu.RUnlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Bridge did not register an active run")
	return ""
}
