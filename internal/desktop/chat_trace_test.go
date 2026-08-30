package desktop

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	builtintools "github.com/OrdoAI/yuri-agent/internal/tools"
)

func TestChatEmitterSplitsAssistantMessagesByProviderItemAndToolBoundary(t *testing.T) {
	emitter := newChatEmitter(&Bridge{}, "conversation", "run", "message-first")
	if err := emitter.Sink(context.Background(), agent.Event{
		Type: agent.EventModelTextDelta, ResponseID: "item-1", Text: "Сначала посмотрю.",
	}); err != nil {
		t.Fatal(err)
	}
	if completed := emitter.closeAssistantSegment(); completed != "message-first" {
		t.Fatalf("completed message = %q", completed)
	}
	if err := emitter.Sink(context.Background(), agent.Event{
		Type: agent.EventModelTextDelta, ResponseID: "item-2", Text: "Нашла файл.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := emitter.Sink(context.Background(), agent.Event{
		Type: agent.EventModelTextDelta, ResponseID: "item-3", Text: "Вот содержимое.",
	}); err != nil {
		t.Fatal(err)
	}
	segments := emitter.AssistantSegments()
	if len(segments) != 3 {
		t.Fatalf("segments = %#v", segments)
	}
	if segments[0].Content != "Сначала посмотрю." || segments[1].Content != "Нашла файл." || segments[2].Content != "Вот содержимое." {
		t.Fatalf("segment contents = %#v", segments)
	}
	events := emitter.Events()
	completed := 0
	for _, event := range events {
		if event.Type == "assistant.completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("automatic completion events = %d, want one provider item transition", completed)
	}
}

func TestRunTraceViewRestoresRedactedToolHistory(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	conversationID := domain.ID("conversation-trace")
	if err := repositories.Conversations.Create(ctx, storage.Conversation{
		ID: conversationID, AgentID: "owner", Title: "Trace", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRunForAgent("owner", "run-trace", domain.RunKindInteractive, conversationID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(domain.RunStateQueued, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(domain.RunStateRunning, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(domain.RunStateCompleted, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"operation": "read", "path": "/allowed/note.txt"})
	if err := repositories.ToolCalls.Create(ctx, storage.ToolCall{
		ID: "tool-trace", RunID: run.ID, ToolID: builtintools.FilesystemReadToolID,
		ArgsRedacted: string(args), Risk: domain.RiskLow, Status: storage.ToolCallSucceeded,
		ResultRef: "blob:result", IdempotencyKey: "call-1", Version: 1,
		CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2500 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}

	// The tool calls of a whole page of runs are now read set-based, so the
	// projection takes the calls it was handed instead of querying per run.
	callsByRun, err := repositories.ToolCalls.ListByRuns(ctx, []domain.ID{run.ID})
	if err != nil {
		t.Fatal(err)
	}
	trace := runTraceView(run, callsByRun[run.ID])
	if trace.Status != string(domain.RunStateCompleted) || len(trace.ToolCalls) != 1 {
		t.Fatalf("trace = %#v", trace)
	}
	call := trace.ToolCalls[0]
	if call.Label != "Работа с файлами" || call.Status != "completed" || call.Result != "blob:result" || call.Args["path"] != "/allowed/note.txt" {
		t.Fatalf("tool call = %#v", call)
	}
}

func TestListChatToolsAlwaysOffersBuiltInReadTools(t *testing.T) {
	bridge := &Bridge{config: config.Config{}}
	withoutRoots, err := bridge.ListChatTools()
	if err != nil {
		t.Fatal(err)
	}
	if !hasTool(withoutRoots, builtintools.FilesystemReadToolID) || !hasTool(withoutRoots, builtintools.FilesystemWriteToolID) {
		t.Fatalf("filesystem tools must be discoverable so they can request access: %#v", withoutRoots)
	}
	if !hasTool(withoutRoots, builtintools.WebFetchToolID) {
		t.Fatalf("public web fetch must be available without a filesystem grant: %#v", withoutRoots)
	}
	if !hasTool(withoutRoots, delegationToolID) || !hasTool(withoutRoots, peerDialogueToolID) {
		t.Fatalf("per-run agent tools must remain visible in the chat tool list: %#v", withoutRoots)
	}
	if hasTool(withoutRoots, builtintools.WebSearchToolID) {
		t.Fatalf("web search must remain unavailable until an endpoint is configured: %#v", withoutRoots)
	}
	bridge.config.WebSearch = config.WebSearchConfig{Enabled: true, Provider: "searxng", Endpoint: "https://search.example.com", DefaultResultLimit: 5}
	withSearch, err := bridge.ListChatTools()
	if err != nil {
		t.Fatal(err)
	}
	if !hasTool(withSearch, builtintools.WebSearchToolID) {
		t.Fatalf("configured web search is missing: %#v", withSearch)
	}

	bridge.config.AllowedDirectories = []string{t.TempDir()}
	withRoots, err := bridge.ListChatTools()
	if err != nil {
		t.Fatal(err)
	}
	if !hasTool(withRoots, builtintools.FilesystemReadToolID) || !hasTool(withRoots, builtintools.FilesystemWriteToolID) {
		t.Fatalf("filesystem tools missing: %#v", withRoots)
	}
}

func TestWebFetchTraceRedactsURLQueryValues(t *testing.T) {
	arguments := json.RawMessage(`{"url":"https://example.com/article?token=private-token&q=secret-search#fragment","max_bytes":4096}`)
	redacted := redactedToolArguments(builtintools.WebFetchToolID, arguments, 4096)
	if strings.Contains(redacted, "private-token") || strings.Contains(redacted, "secret-search") || strings.Contains(redacted, "fragment") {
		t.Fatalf("web fetch trace leaked query data: %s", redacted)
	}
	if !strings.Contains(redacted, "example.com/article") || !strings.Contains(redacted, "token") || !strings.Contains(redacted, "q") {
		t.Fatalf("web fetch trace lost useful target metadata: %s", redacted)
	}
}

func TestWebSearchTraceRedactsQuery(t *testing.T) {
	redacted := redactedToolArguments(builtintools.WebSearchToolID, json.RawMessage(`{"query":"private question","limit":3,"language":"ru"}`), 4096)
	if strings.Contains(redacted, "private question") || !strings.Contains(redacted, "redacted 16 chars") || !strings.Contains(redacted, `"limit":3`) {
		t.Fatalf("web search trace redaction = %s", redacted)
	}
}

func hasTool(items []ChatToolDescriptorView, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}
