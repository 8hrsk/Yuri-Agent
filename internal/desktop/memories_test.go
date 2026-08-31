package desktop

import (
	"context"
	"errors"
	"fmt"
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
	paths := config.Paths{
		ConfigDirectory: filepath.Join(root, "config"), ConfigFile: filepath.Join(root, "config", "config.json"),
		DataDirectory: root, DatabaseFile: filepath.Join(root, "yuri.db"),
	}
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

func TestMemoryBridgePublishesRecallsAndRevokesAcrossAgents(t *testing.T) {
	bridge := newMemoryTestBridge(t)
	memoryID, _ := seedMemoryTest(t, bridge)
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	peer, err := domain.NewAgentProfile("agent-peer", "Mika", 22, "female", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.Agents.Create(ctx, peer); err != nil {
		t.Fatal(err)
	}

	published, err := bridge.SetMemoryScope(SetMemoryScopeInput{MemoryID: string(memoryID), Scope: string(domain.MemoryScopeOwnerShared)})
	if err != nil {
		t.Fatal(err)
	}
	if published.Scope != string(domain.MemoryScopeOwnerShared) || published.Version != 2 {
		t.Fatalf("published memory = %#v", published)
	}
	versions, err := bridge.repositories.Memories.ListVersions(ctx, memoryID, 1)
	if err != nil || len(versions) != 1 || versions[0].Operation != "publish" {
		t.Fatalf("publish journal = %#v, %v", versions, err)
	}

	peerAdapter := sqliteMemoryAdapter{repositories: bridge.repositories, agentID: peer.ID}
	engine, err := memory.NewEngine(memory.Config{Store: peerAdapter, Lexical: peerAdapter})
	if err != nil {
		t.Fatal(err)
	}
	recalled, err := engine.Recall(ctx, "зелёный чай", memory.RecallOptions{Mode: memory.RecallAutomatic, Limit: 5, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled) != 1 || recalled[0].Memory.ID != memoryID || recalled[0].Memory.AgentID == peer.ID {
		t.Fatalf("peer recall = %#v", recalled)
	}

	revoked, err := bridge.SetMemoryScope(SetMemoryScopeInput{MemoryID: string(memoryID), Scope: string(domain.MemoryScopeAgentPrivate)})
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Scope != string(domain.MemoryScopeAgentPrivate) || revoked.Version != 3 {
		t.Fatalf("revoked memory = %#v", revoked)
	}
	recalled, err = engine.Recall(ctx, "зелёный чай", memory.RecallOptions{Mode: memory.RecallAutomatic, Limit: 5, Now: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled) != 0 {
		t.Fatalf("revoked private memory leaked to peer: %#v", recalled)
	}
}

func TestMemoryBridgeRejectsPublishingHighlySensitiveMemory(t *testing.T) {
	bridge := newMemoryTestBridge(t)
	memoryID, _ := seedMemoryTest(t, bridge)
	current, err := bridge.repositories.Memories.GetForAgent(context.Background(), bridge.personaProfileID(), memoryID)
	if err != nil {
		t.Fatal(err)
	}
	current.Version++
	current.Sensitivity = domain.MemorySensitivityHighlySensitive
	current.UpdatedAt = current.UpdatedAt.Add(time.Minute)
	if err := bridge.repositories.Memories.Save(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetMemoryScope(SetMemoryScopeInput{MemoryID: string(memoryID), Scope: string(domain.MemoryScopeInstallationShared)}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("publish highly-sensitive memory error = %v", err)
	}
}

func seedBackstoryMemoryTest(t *testing.T, bridge *Bridge) domain.ID {
	t.Helper()
	if _, err := bridge.CreateAgent(CreateAgentInput{Name: "Emily", Age: 21, Gender: "female"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	agentID := bridge.personaProfileID()
	seed, err := bridge.repositories.Personalization.Get(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if !now.After(seed.UpdatedAt) {
		now = seed.UpdatedAt.Add(time.Nanosecond)
	}
	previousVersion := seed.Version
	seed.Version++
	seed.RevisionID = domain.ID(fmt.Sprintf("%s:personalization:v%d", seed.AgentID, seed.Version))
	seed.ParentID = seed.RevisionID
	seed.ParentVersion = previousVersion
	seed.Operation = domain.PersonalizationOperationUpdate
	seed.Reason = "test backstory episode"
	seed.UpdatedAt = now
	seed.Backstory.Episodes = []domain.BackstoryEpisode{{
		ID: "violet-garden", Title: "Фиолетовый сад", Content: "Я впервые увидела фиолетовый сад.",
		Kind: "childhood", EmotionalValence: .5, Sequence: 1,
	}}
	seed, err = bridge.repositories.Personalization.AppendVersion(ctx, seed, previousVersion)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := memory.NewEngine(memory.Config{AgentID: agentID, Store: sqliteMemoryAdapter{repositories: bridge.repositories, agentID: agentID}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := engine.HydrateBackstory(ctx, seed)
	if err != nil || len(results) != 1 {
		t.Fatalf("hydrate = %#v, %v", results, err)
	}
	return results[0].Memory.ID
}

func TestMemoryBridgeCuratesOwnerBackstoryWithoutOrdinaryMemoryMutation(t *testing.T) {
	bridge := newMemoryTestBridge(t)
	id := seedBackstoryMemoryTest(t, bridge)
	ctx := context.Background()

	items, err := bridge.ListMemories(MemoryListInput{LifecycleState: "all"})
	if err != nil || len(items) != 1 || items[0].Fiction == nil || items[0].Fiction.Provenance != memory.FictionProvenanceOwnerSeed || len(items[0].History) != 1 {
		t.Fatalf("fiction view = %#v, %v", items, err)
	}
	content := "Нельзя переписать исходник обычным memory edit."
	if _, err := bridge.UpdateMemory(UpdateMemoryInput{MemoryID: string(id), Content: &content}); !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("ordinary update error = %v", err)
	}
	if _, err := bridge.SetMemoryScope(SetMemoryScopeInput{MemoryID: string(id), Scope: string(domain.MemoryScopeOwnerShared)}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("fiction publish error = %v", err)
	}
	if _, err := bridge.SetMemoryLifecycle(SetMemoryLifecycleInput{MemoryID: string(id), State: "dormant"}); !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("ordinary lifecycle error = %v", err)
	}
	if err := bridge.DeleteMemory(DeleteMemoryInput{MemoryID: string(id)}); !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("ordinary delete error = %v", err)
	}

	editedText := "Я впервые увидела фиолетовый сад и решила стать художницей."
	edited, err := bridge.UpdateBackstoryMemory(BackstoryMemoryInput{MemoryID: string(id), Content: editedText})
	if err != nil || edited.Content != editedText || edited.Fiction == nil || edited.Fiction.OwnerAuthored == false {
		t.Fatalf("backstory edit = %#v, %v", edited, err)
	}
	seed, err := bridge.repositories.Personalization.Get(ctx, bridge.personaProfileID())
	if err != nil || len(seed.Backstory.Episodes) != 1 || seed.Backstory.Episodes[0].Content != editedText {
		t.Fatalf("owner seed after edit = %#v, %v", seed.Backstory, err)
	}
	disabled, err := bridge.DisableBackstoryMemory(BackstoryMemoryInput{MemoryID: string(id)})
	if err != nil || disabled.Lifecycle != string(domain.MemoryLifecycleDeleted) {
		t.Fatalf("disable = %#v, %v", disabled, err)
	}
	restored, err := bridge.RehydrateBackstoryMemory(BackstoryMemoryInput{MemoryID: string(id)})
	if err != nil || restored.Lifecycle != string(domain.MemoryLifecycleActive) || restored.Content != editedText || len(restored.History) != 4 {
		t.Fatalf("rehydrate = %#v, %v", restored, err)
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

// TestListMemoriesLifecyclePagesFullyThroughBridge is the bridge-level guard for
// M-17. The lifecycle filter used to be applied in Go after SQL had already
// applied LIMIT/OFFSET, so a page could come back short — or empty — while
// matching records sat further down the unfiltered ordering, unreachable on any
// page. Ordering is pinned/salience-first, so dormant records seeded with low
// salience land behind the active ones and reproduce exactly that case.
func TestListMemoriesLifecyclePagesFullyThroughBridge(t *testing.T) {
	bridge := newMemoryTestBridge(t)
	ctx := context.Background()
	agentID := bridge.personaProfileID()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	dormantIDs := make(map[string]bool)
	for index := 0; index < 12; index++ {
		id := domain.ID(fmt.Sprintf("memory-lifecycle-%02d", index))
		record := domain.Memory{
			ID: id, AgentID: agentID, Scope: domain.MemoryScopeAgentPrivate,
			Kind: domain.MemoryKindSemantic, Content: fmt.Sprintf("запись %02d", index),
			Salience: 0.9, Confidence: 0.9, Lifecycle: domain.MemoryLifecycleActive,
			CreatedAt: now, UpdatedAt: now,
		}
		// Every third record becomes dormant, and is given a lower salience so
		// the default ordering places it behind all the active ones.
		if index%3 == 0 {
			record.Salience = 0.1
		}
		if err := bridge.repositories.Memories.Create(ctx, record); err != nil {
			t.Fatal(err)
		}
		if index%3 == 0 {
			if _, err := bridge.repositories.Memories.MarkDormant(ctx, id, 1, now, "test"); err != nil {
				t.Fatal(err)
			}
			dormantIDs[string(id)] = true
		}
	}
	if len(dormantIDs) != 4 {
		t.Fatalf("expected 4 dormant records, seeded %d", len(dormantIDs))
	}

	seen := make(map[string]bool)
	for offset := 0; offset < 12; offset += 3 {
		views, err := bridge.ListMemories(MemoryListInput{
			LifecycleState: "dormant", Limit: 3, Offset: offset,
		})
		if err != nil {
			t.Fatalf("list dormant at offset %d: %v", offset, err)
		}
		for _, view := range views {
			if !dormantIDs[view.ID] {
				t.Fatalf("offset %d returned non-dormant record %q", offset, view.ID)
			}
			if seen[view.ID] {
				t.Fatalf("record %q returned on more than one page", view.ID)
			}
			seen[view.ID] = true
		}
		// The first page must be full: four dormant records exist, so a page of
		// three can be satisfied. A short first page is the M-17 symptom.
		if offset == 0 && len(views) != 3 {
			t.Fatalf("first dormant page was short: got %d views, want 3", len(views))
		}
	}
	if len(seen) != len(dormantIDs) {
		t.Fatalf("paging reached %d dormant records, want all %d", len(seen), len(dormantIDs))
	}

	// An unparseable lifecycle must be rejected rather than silently returning
	// everything, which is what dropping the Go-side filter would otherwise do.
	if _, err := bridge.ListMemories(MemoryListInput{LifecycleState: "not-a-state"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid lifecycle: got %v, want ErrInvalidArgument", err)
	}
}
