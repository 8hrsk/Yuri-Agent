package desktop

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	contextbuilder "github.com/OrdoAI/yuri-agent/internal/context"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func newMemoryTestBridge(t *testing.T) *Bridge {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{DataDirectory: root, DatabaseFile: filepath.Join(root, "yuri.db")}
	database, err := storage.Open(context.Background(), paths.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Bridge{database: database, repositories: repositories, paths: paths, config: config.Default(paths)}
}

func seedMemoryTest(t *testing.T, bridge *Bridge) (domain.ID, domain.ID) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	conversationID := domain.ID("conversation-memory-test")
	messageID := domain.ID("message-memory-test")
	if err := bridge.repositories.Conversations.Create(ctx, storage.Conversation{
		ID: conversationID, AgentID: bridge.personaProfileID(), Title: "Предпочтения", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.Messages.Create(ctx, storage.Message{
		ID: messageID, ConversationID: conversationID, Role: "user", Content: "Я люблю зелёный чай сенча",
		Status: "complete", ProviderMeta: "{}", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	memoryID := domain.ID("memory-tea")
	if err := bridge.repositories.Memories.Create(ctx, domain.Memory{
		ID: memoryID, AgentID: bridge.personaProfileID(), Scope: domain.MemoryScopeAgentPrivate,
		Version: 1, Kind: domain.MemoryKindUserModel, Nature: domain.MemoryNatureFact,
		Content: "Пользователь любит зелёный чай", Confidence: .95, Salience: .8,
		Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionDecay,
		Lifecycle: domain.MemoryLifecycleActive, CreatedAt: now, UpdatedAt: now,
	}, []domain.MemorySource{{
		MemoryID: memoryID, MemoryVersion: 1, SourceType: "message", SourceID: messageID,
		ConversationID: conversationID, MessageID: messageID, CreatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	return memoryID, messageID
}

func TestMemoryBridgeListsEditsAndTransitionsLifecycle(t *testing.T) {
	bridge := newMemoryTestBridge(t)
	memoryID, _ := seedMemoryTest(t, bridge)
	items, err := bridge.ListMemories(MemoryListInput{LifecycleState: "active"})
	if err != nil || len(items) != 1 {
		t.Fatalf("ListMemories() = %#v, %v", items, err)
	}
	if len(items[0].Sources) != 1 || items[0].Sources[0].Excerpt == "" {
		t.Fatalf("provenance missing: %#v", items[0])
	}
	content := "Пользователь предпочитает сенчу"
	pinned := true
	updated, err := bridge.UpdateMemory(UpdateMemoryInput{MemoryID: string(memoryID), Content: &content, Pinned: &pinned})
	if err != nil || updated.Content != content || !updated.Pinned || updated.Version != 2 {
		t.Fatalf("UpdateMemory() = %#v, %v", updated, err)
	}
	dormant, err := bridge.SetMemoryLifecycle(SetMemoryLifecycleInput{MemoryID: string(memoryID), State: "dormant"})
	if err != nil || dormant.Lifecycle != "dormant" {
		t.Fatalf("SetMemoryLifecycle() = %#v, %v", dormant, err)
	}
	active, err := bridge.ListMemories(MemoryListInput{LifecycleState: "active"})
	if err != nil || len(active) != 0 {
		t.Fatalf("active memories = %#v, %v", active, err)
	}
	dormantItems, err := bridge.ListMemories(MemoryListInput{LifecycleState: "dormant"})
	if err != nil || len(dormantItems) != 1 {
		t.Fatalf("dormant memories = %#v, %v", dormantItems, err)
	}
}

func TestMemoryBridgeDeliberateArchiveSearchReturnsOriginalMessage(t *testing.T) {
	bridge := newMemoryTestBridge(t)
	_, messageID := seedMemoryTest(t, bridge)
	response, err := bridge.SearchArchive(ArchiveSearchInput{Query: "сенча", IncludeDormant: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].MessageID != string(messageID) {
		t.Fatalf("SearchArchive() = %#v", response)
	}
}

func TestStageTwoContextUsesCoreAndCrossSessionArchive(t *testing.T) {
	bridge := newMemoryTestBridge(t)
	seedMemoryTest(t, bridge)
	engine, err := memory.NewEngine(memory.Config{
		Store:   sqliteMemoryAdapter{repositories: bridge.repositories, agentID: bridge.personaProfileID()},
		Lexical: sqliteMemoryAdapter{repositories: bridge.repositories, agentID: bridge.personaProfileID()},
		Archive: sqliteMemoryAdapter{repositories: bridge.repositories, agentID: bridge.personaProfileID()},
	})
	if err != nil {
		t.Fatal(err)
	}
	currentID := domain.ID("conversation-current-context")
	now := time.Now().UTC()
	if err := bridge.repositories.Conversations.Create(context.Background(), storage.Conversation{
		ID: currentID, AgentID: bridge.personaProfileID(), Title: "Новый диалог", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	assembler, err := contextbuilder.New(desktopContextSource{engine: engine, repositories: bridge.repositories, agentID: bridge.personaProfileID()}, contextbuilder.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := assembler.Assemble(context.Background(), contextbuilder.Input{
		AgentID: bridge.personaProfileID(), ConversationID: currentID, Query: "чай", ImmutablePolicy: "policy", IdentitySeed: "identity",
		Transcript: []agent.Message{{Role: agent.RoleUser, Content: "Какой чай я люблю?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.CoreIDs) != 1 || len(snapshot.ArchiveMessageIDs) != 1 {
		t.Fatalf("snapshot lacks cross-session context: %#v", snapshot)
	}
	if len(snapshot.RecalledMemoryIDs) != 1 {
		t.Fatalf("snapshot lacks hybrid recall: %#v", snapshot)
	}
}
