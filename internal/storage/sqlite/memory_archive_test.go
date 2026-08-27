package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func stage2Memory(at time.Time, id, content string) domain.Memory {
	return domain.Memory{
		ID: domain.ID(id), Kind: domain.MemoryKindSemantic, Nature: domain.MemoryNatureFact,
		Content: content, Summary: content, Confidence: 0.9, Salience: 0.8,
		Valence: 0.1, Sensitivity: domain.MemorySensitivityPrivate,
		Retention: domain.MemoryRetentionDecay, Lifecycle: domain.MemoryLifecycleActive,
		CreatedAt: at, UpdatedAt: at,
	}
}

func stage2ConversationAndMessage(t *testing.T, databaseCtx context.Context, repositories *Repositories, now time.Time, conversationID, messageID, content string) {
	t.Helper()
	if err := repositories.Conversations.Create(databaseCtx, Conversation{
		ID: domain.ID(conversationID), Title: conversationID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Messages.Create(databaseCtx, Message{
		ID: domain.ID(messageID), ConversationID: domain.ID(conversationID), Role: "user", Content: content,
		Status: "complete", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryRepositoryVersionsProvenanceAndLifecycle(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	stage2ConversationAndMessage(t, ctx, repositories, now, "session-1", "message-1", "I prefer dark chocolate and tea")
	memory := stage2Memory(now, "memory-1", "The owner prefers dark chocolate")
	source := domain.MemorySource{
		ID: "source-1", MemoryID: memory.ID, SourceType: "message", SourceID: "message-1",
		ConversationID: "session-1", MessageID: "message-1", ExcerptHash: "sha256:excerpt", CreatedAt: now,
	}
	if err := repositories.Memories.Create(ctx, memory, []domain.MemorySource{source}); err != nil {
		t.Fatalf("create memory: %v", err)
	}
	stored, err := repositories.Memories.Get(ctx, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 1 || stored.Content != memory.Content {
		t.Fatalf("stored memory = %#v", stored)
	}
	sources, err := repositories.Memories.ListSources(ctx, memory.ID)
	if err != nil || len(sources) != 1 || sources[0].MessageID != "message-1" {
		t.Fatalf("sources = %#v, %v", sources, err)
	}

	revised := stored
	revised.Version = 2
	revised.Content = "The owner prefers dark chocolate, tea, and quiet mornings"
	revised.Summary = revised.Content
	revised.UpdatedAt = now.Add(time.Hour)
	if err := repositories.Memories.Save(ctx, revised); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	if _, err := repositories.Memories.GetVersion(ctx, memory.ID, 1); err != nil {
		t.Fatalf("get immutable version: %v", err)
	}
	current, err := repositories.Memories.Get(ctx, memory.ID)
	if err != nil || current.Version != 2 {
		t.Fatalf("current revision = %#v, %v", current, err)
	}
	sources, err = repositories.Memories.ListSources(ctx, memory.ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("copied sources = %#v, %v", sources, err)
	}
	if err := repositories.Memories.Save(ctx, revised); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	versions, err := repositories.Memories.ListVersions(ctx, memory.ID)
	if err != nil || len(versions) != 2 || versions[0].Operation != "update" || versions[0].ParentVersion != 1 {
		t.Fatalf("version journal = %#v, %v", versions, err)
	}
	revised.Version = 3
	revised.UpdatedAt = now.Add(90 * time.Minute)
	if _, err := repositories.Memories.AppendVersionWithMetadata(ctx, revised, 2, MemoryVersionMetadata{
		RevisionID: "revision-explicit", Operation: "merge", ParentVersion: 2, Reason: "consolidated evidence",
	}); err != nil {
		t.Fatalf("append metadata revision: %v", err)
	}
	metadata, err := repositories.Memories.GetVersionRecord(ctx, memory.ID, 3)
	if err != nil || metadata.RevisionID != "revision-explicit" || metadata.Operation != "merge" || metadata.ParentVersion != 2 {
		t.Fatalf("metadata revision = %#v, %v", metadata, err)
	}

	current = revised
	dormant, err := repositories.Memories.MarkDormant(ctx, memory.ID, current.Version, now.Add(2*time.Hour), "decayed")
	if err != nil {
		t.Fatalf("mark dormant: %v", err)
	}
	if !dormant.IsDormant() || dormant.Version != 4 || dormant.DormantAt.IsZero() {
		t.Fatalf("dormant memory = %#v", dormant)
	}
	if found, err := repositories.Memories.Search(ctx, "dark chocolate"); err != nil || len(found) != 0 {
		t.Fatalf("ordinary dormant search = %#v, %v", found, err)
	}
	found, err := repositories.Memories.Search(ctx, "dark chocolate", MemorySearchOptions{Deliberate: true})
	if err != nil || len(found) != 1 || found[0].Memory.Lifecycle != domain.MemoryLifecycleDormant {
		t.Fatalf("deliberate dormant search = %#v, %v", found, err)
	}

	active, err := repositories.Memories.Restore(ctx, memory.ID, dormant.Version, now.Add(3*time.Hour), "recalled by user")
	if err != nil || !active.IsActive() || active.Version != 5 {
		t.Fatalf("restore = %#v, %v", active, err)
	}
	deleted, err := repositories.Memories.SoftDelete(ctx, memory.ID, active.Version, now.Add(4*time.Hour), "forgotten")
	if err != nil || !deleted.IsDeleted() || deleted.DeletedAt.IsZero() {
		t.Fatalf("soft delete = %#v, %v", deleted, err)
	}
	if list, err := repositories.Memories.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("active list after delete = %#v, %v", list, err)
	}
	list, err := repositories.Memories.List(ctx, MemoryListOptions{IncludeDeleted: true})
	if err != nil || len(list) != 1 || !list[0].IsDeleted() {
		t.Fatalf("deleted list = %#v, %v", list, err)
	}
	if sources, err := repositories.Memories.ListSources(ctx, memory.ID, 1); err != nil || len(sources) != 1 {
		t.Fatalf("source after tombstone = %#v, %v", sources, err)
	}
}

func TestMemoryCoreSelectionAndArchiveSearchAreBounded(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	stage2ConversationAndMessage(t, ctx, repositories, now, "session-active", "message-active", "Discussed a rare lunar observatory project")
	stage2ConversationAndMessage(t, ctx, repositories, now.Add(time.Minute), "session-old", "message-old", "Discussed a private lunar observatory project")
	if err := repositories.Conversations.Archive(ctx, "session-old", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, memory := range []domain.Memory{
		stage2Memory(now, "memory-high", "Pinned project: lunar observatory"),
		stage2Memory(now.Add(time.Second), "memory-hidden", "The owner likes observatory diagrams"),
		stage2Memory(now.Add(2*time.Second), "memory-low", "The owner enjoys astronomy books"),
	} {
		if err := repositories.Memories.Create(ctx, memory); err != nil {
			t.Fatal(err)
		}
	}
	current, err := repositories.Memories.Get(ctx, "memory-high")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Memories.Pin(ctx, current.ID, current.Version, true, now.Add(time.Hour), "core curation"); err != nil {
		t.Fatal(err)
	}
	current, err = repositories.Memories.Get(ctx, "memory-hidden")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Memories.HideFromCore(ctx, current.ID, current.Version, true, now.Add(time.Hour), "not useful in prefix"); err != nil {
		t.Fatal(err)
	}
	core, err := repositories.Memories.ListCore(ctx, CoreMemoryOptions{MaxItems: 2, MaxTokens: 50})
	if err != nil || len(core) != 2 {
		t.Fatalf("core = %#v, %v", core, err)
	}
	if core[0].ID != "memory-high" {
		t.Fatalf("pinned memory was not first: %#v", core)
	}
	for _, item := range core {
		if item.ID == "memory-hidden" {
			t.Fatalf("hidden memory entered core: %#v", core)
		}
	}

	results, err := repositories.Archive.Search(ctx, "lunar observatory")
	if err != nil || len(results) != 2 {
		t.Fatalf("full archive search = %#v, %v", results, err)
	}
	results, err = repositories.Archive.Search(ctx, "lunar observatory", ArchiveSearchOptions{ExcludeArchived: true})
	if err != nil || len(results) != 1 || results[0].Message.ID != "message-active" {
		t.Fatalf("active-only archive search = %#v, %v", results, err)
	}
	window, err := repositories.Archive.Window(ctx, "message-active", 0, 0)
	if err != nil || len(window) != 1 || window[0].ID != "message-active" {
		t.Fatalf("archive window = %#v, %v", window, err)
	}

	if _, err := database.Exec(`DELETE FROM messages_fts`); err != nil {
		t.Fatal(err)
	}
	if results, err := repositories.Archive.Search(ctx, "lunar observatory"); err == nil && len(results) != 0 {
		t.Fatalf("stale archive index unexpectedly returned results: %#v", results)
	}
	if err := repositories.Memories.RebuildProjections(ctx); err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	if results, err := repositories.Archive.Search(ctx, "lunar observatory", ArchiveSearchOptions{ExcludeArchived: true}); err != nil || len(results) != 1 {
		t.Fatalf("rebuilt archive search = %#v, %v", results, err)
	}
}

func TestSafeFTSQueryRejectsOperatorsAndKeepsTerms(t *testing.T) {
	query, err := safeFTSQuery(`dark OR chocolate*`)
	if err != nil {
		t.Fatal(err)
	}
	if query != `"dark" AND "OR" AND "chocolate"` {
		t.Fatalf("safe FTS query = %q", query)
	}
	if _, err := safeFTSQuery("***"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("punctuation-only query error = %v", err)
	}
}
