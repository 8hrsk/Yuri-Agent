package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func countMemoryTableRows(t *testing.T, database *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := database.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return count
}

func memoryVersionCount(t *testing.T, database *sql.DB, id string) int {
	t.Helper()
	return countMemoryTableRows(t, database, `SELECT COUNT(*) FROM memory_versions WHERE memory_id = ?`, id)
}

func memoryIndexCount(t *testing.T, database *sql.DB, id string) int {
	t.Helper()
	return countMemoryTableRows(t, database, `SELECT COUNT(*) FROM memory_fts WHERE memory_id = ?`, id)
}

func memorySourceCount(t *testing.T, database *sql.DB, id string) int {
	t.Helper()
	return countMemoryTableRows(t, database, `SELECT COUNT(*) FROM memory_sources WHERE memory_id = ?`, id)
}

// TestRecordRecallDoesNotCreateContentRevisions is the H-17 regression: a
// recall is a read, and reads must not grow the database. Recording many
// recalls must leave the journal, the provenance links and the search index
// exactly as one write left them.
func TestRecordRecallDoesNotCreateContentRevisions(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	stage2ConversationAndMessage(t, ctx, repositories, now, "session-recall", "message-recall", "the owner keeps a telescope")
	item := stage2Memory(now, "memory-recall", "The owner keeps a telescope on the balcony")
	item.ContentJSON = `{"object":"telescope"}`
	sources := []domain.MemorySource{
		{ID: "source-recall-1", MemoryID: item.ID, SourceType: "message", SourceID: "message-recall", ConversationID: "session-recall", MessageID: "message-recall", ExcerptHash: "sha256:one", CreatedAt: now},
		{ID: "source-recall-2", MemoryID: item.ID, SourceType: "conversation", SourceID: "session-recall", ConversationID: "session-recall", ExcerptHash: "sha256:two", CreatedAt: now},
	}
	if err := repositories.Memories.Create(ctx, item, sources); err != nil {
		t.Fatalf("create memory: %v", err)
	}

	versionsAfterWrite := memoryVersionCount(t, database, "memory-recall")
	indexAfterWrite := memoryIndexCount(t, database, "memory-recall")
	sourcesAfterWrite := memorySourceCount(t, database, "memory-recall")
	if versionsAfterWrite != 1 || indexAfterWrite != 1 || sourcesAfterWrite != 2 {
		t.Fatalf("after one write: versions=%d index=%d sources=%d, want 1/1/2", versionsAfterWrite, indexAfterWrite, sourcesAfterWrite)
	}

	const recalls = 25
	var last domain.Memory
	for index := 0; index < recalls; index++ {
		last, err = repositories.Memories.RecordRecall(ctx, item.ID, now.Add(time.Duration(index+1)*time.Minute))
		if err != nil {
			t.Fatalf("record recall %d: %v", index, err)
		}
	}

	if got := memoryVersionCount(t, database, "memory-recall"); got != versionsAfterWrite {
		t.Fatalf("memory_versions rows after %d recalls = %d, want %d (a recall is not a content revision)", recalls, got, versionsAfterWrite)
	}
	if got := memoryIndexCount(t, database, "memory-recall"); got != indexAfterWrite {
		t.Fatalf("memory_fts rows after %d recalls = %d, want %d (a recall must not reindex content)", recalls, got, indexAfterWrite)
	}
	if got := memorySourceCount(t, database, "memory-recall"); got != sourcesAfterWrite {
		t.Fatalf("memory_sources rows after %d recalls = %d, want %d (a recall must not copy provenance forward)", recalls, got, sourcesAfterWrite)
	}
	if got := countMemoryTableRows(t, database, `SELECT COUNT(*) FROM memory_recalls WHERE memory_id = ?`, "memory-recall"); got != 1 {
		t.Fatalf("memory_recalls rows = %d, want exactly 1 bounded counter row", got)
	}

	// The recall telemetry itself must still be durable and complete.
	if last.Version != 1 {
		t.Fatalf("recall returned version %d, want the unchanged head version 1", last.Version)
	}
	if last.AccessCount != recalls {
		t.Fatalf("returned access count = %d, want %d", last.AccessCount, recalls)
	}
	stored, err := repositories.Memories.Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 1 {
		t.Fatalf("head version = %d, want 1", stored.Version)
	}
	if stored.AccessCount != recalls {
		t.Fatalf("stored access count = %d, want %d", stored.AccessCount, recalls)
	}
	wantRecalledAt := now.Add(recalls * time.Minute)
	if !stored.LastRecalledAt.Equal(wantRecalledAt) || !stored.LastAccessedAt.Equal(wantRecalledAt) {
		t.Fatalf("recall timestamps = %v / %v, want %v", stored.LastRecalledAt, stored.LastAccessedAt, wantRecalledAt)
	}

	// No content or provenance may be lost by not versioning the recall.
	if stored.Content != item.Content || stored.ContentJSON != item.ContentJSON || stored.Summary != item.Summary {
		t.Fatalf("content changed by recall: %#v", stored)
	}
	storedSources, err := repositories.Memories.ListSources(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedSources) != 2 {
		t.Fatalf("sources after recalls = %#v, want 2", storedSources)
	}
	versions, err := repositories.Memories.ListVersions(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Operation != "create" || versions[0].Memory.Content != item.Content {
		t.Fatalf("journal after recalls = %#v, want a single create revision with the original content", versions)
	}
}

// TestMemoryUpdateCreatesOneVersionAndOneIndexRow pins the other half of the
// invariant: a real content change is still journalled, and the rebuildable
// search projection still holds exactly one row per live memory.
func TestMemoryUpdateCreatesOneVersionAndOneIndexRow(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	item := stage2Memory(now, "memory-update", "The owner drinks oolong tea")
	if err := repositories.Memories.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Memories.RecordRecall(ctx, item.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	current, err := repositories.Memories.Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Version++
	current.Content = "The owner drinks genmaicha tea"
	current.Summary = current.Content
	current.UpdatedAt = now.Add(2 * time.Minute)
	if err := repositories.Memories.Save(ctx, current); err != nil {
		t.Fatalf("save update: %v", err)
	}

	if got := memoryVersionCount(t, database, "memory-update"); got != 2 {
		t.Fatalf("memory_versions rows = %d, want exactly 2 (create + update)", got)
	}
	if got := memoryIndexCount(t, database, "memory-update"); got != 1 {
		t.Fatalf("memory_fts rows = %d, want exactly 1 live row", got)
	}
	var indexedVersion int
	if err := database.QueryRow(`SELECT CAST(memory_version AS INTEGER) FROM memory_fts WHERE memory_id = ?`, "memory-update").Scan(&indexedVersion); err != nil {
		t.Fatal(err)
	}
	if indexedVersion != 2 {
		t.Fatalf("indexed version = %d, want the head version 2", indexedVersion)
	}

	// The superseded revision stays in the append-only journal, fully readable.
	previous, err := repositories.Memories.GetVersion(ctx, item.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Content != "The owner drinks oolong tea" {
		t.Fatalf("journal revision 1 content = %q", previous.Content)
	}
	hits, err := repositories.Memories.Search(ctx, "genmaicha")
	if err != nil || len(hits) != 1 {
		t.Fatalf("search for updated content = %#v, %v", hits, err)
	}
	if stale, err := repositories.Memories.Search(ctx, "oolong"); err != nil || len(stale) != 0 {
		t.Fatalf("search for superseded content = %#v, %v, want no live hit", stale, err)
	}
	// The recall counter survives a content revision and keeps counting.
	if hits[0].Memory.AccessCount != 1 {
		t.Fatalf("access count after update = %d, want 1", hits[0].Memory.AccessCount)
	}
}

// TestMemorySearchReturnsEachMemoryOnce guards against the index growing a row
// per historical revision, which inflated bm25 and made every MATCH scan dead
// postings.
func TestMemorySearchReturnsEachMemoryOnce(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	ids := []string{"memory-observatory-a", "memory-observatory-b"}
	for _, id := range ids {
		if err := repositories.Memories.Create(ctx, stage2Memory(now, id, "notes about the lunar observatory")); err != nil {
			t.Fatal(err)
		}
		for revision := 0; revision < 3; revision++ {
			current, err := repositories.Memories.Get(ctx, domain.ID(id))
			if err != nil {
				t.Fatal(err)
			}
			current.Version++
			current.Content = fmt.Sprintf("notes about the lunar observatory revision %d", revision)
			current.UpdatedAt = now.Add(time.Duration(revision+1) * time.Minute)
			if err := repositories.Memories.Save(ctx, current); err != nil {
				t.Fatal(err)
			}
			if _, err := repositories.Memories.RecordRecall(ctx, domain.ID(id), current.UpdatedAt); err != nil {
				t.Fatal(err)
			}
		}
	}

	if got := countMemoryTableRows(t, database, `SELECT COUNT(*) FROM memory_fts`); got != len(ids) {
		t.Fatalf("memory_fts rows = %d, want %d (one per live memory)", got, len(ids))
	}
	hits, err := repositories.Memories.Search(ctx, "lunar observatory")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != len(ids) {
		t.Fatalf("search hits = %d, want %d", len(hits), len(ids))
	}
	seen := make(map[domain.ID]int, len(hits))
	for _, hit := range hits {
		seen[hit.Memory.ID]++
		if hit.Memory.Version != 4 {
			t.Fatalf("hit %s returned version %d, want the head version 4", hit.Memory.ID, hit.Memory.Version)
		}
	}
	for _, id := range ids {
		if seen[domain.ID(id)] != 1 {
			t.Fatalf("memory %s appeared %d times in search results", id, seen[domain.ID(id)])
		}
	}
}

// TestRebuildProjectionsPrunesHistoricalIndexRows covers the repair path for a
// database that already accumulated one index row per revision: the existing
// rebuild surface converges it on one row per live memory and leaves the
// journal and the recall counters intact.
func TestRebuildProjectionsPrunesHistoricalIndexRows(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	item := stage2Memory(now, "memory-legacy", "The owner collects vinyl records")
	if err := repositories.Memories.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Memories.RecordRecall(ctx, item.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Simulate the rows a pre-fix install left behind: one index row for every
	// superseded revision, none of which memory_heads points at.
	for version := 2; version <= 40; version++ {
		if _, err := database.Exec(`
			INSERT INTO memory_fts(memory_id, memory_version, kind, nature, content, summary)
			VALUES (?, ?, ?, ?, ?, ?)`, "memory-legacy", version, "semantic", "fact", item.Content, item.Summary); err != nil {
			t.Fatal(err)
		}
	}
	if got := memoryIndexCount(t, database, "memory-legacy"); got != 40 {
		t.Fatalf("seeded index rows = %d, want 40", got)
	}

	if err := repositories.Memories.RebuildProjections(ctx); err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	if got := memoryIndexCount(t, database, "memory-legacy"); got != 1 {
		t.Fatalf("index rows after rebuild = %d, want 1", got)
	}
	if got := memoryVersionCount(t, database, "memory-legacy"); got != 1 {
		t.Fatalf("journal rows after rebuild = %d, want 1 (the journal is authoritative)", got)
	}
	stored, err := repositories.Memories.Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessCount != 1 {
		t.Fatalf("access count after rebuild = %d, want 1 (recall telemetry is not a projection)", stored.AccessCount)
	}
	hits, err := repositories.Memories.Search(ctx, "vinyl")
	if err != nil || len(hits) != 1 {
		t.Fatalf("search after rebuild = %#v, %v", hits, err)
	}
}

// TestListMemoriesLifecycleFilterPagesFully is the M-17 regression: the
// lifecycle predicate has to run in SQL, before LIMIT/OFFSET. Filtering the
// returned page instead yields short pages and skipped records.
func TestListMemoriesLifecycleFilterPagesFully(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	dormantIDs := make(map[domain.ID]bool)
	for index := 0; index < 12; index++ {
		id := domain.ID(fmt.Sprintf("memory-page-%02d", index))
		item := stage2Memory(now, string(id), fmt.Sprintf("paged memory %02d", index))
		// Distinct salience makes the sort order deterministic across pages.
		item.Salience = 0.9 - float64(index)*0.05
		if err := repositories.Memories.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		if index%3 != 0 {
			continue
		}
		if _, err := repositories.Memories.MarkDormant(ctx, id, 1, now.Add(time.Minute), "aged out"); err != nil {
			t.Fatal(err)
		}
		dormantIDs[id] = true
	}
	if len(dormantIDs) != 4 {
		t.Fatalf("seeded %d dormant memories, want 4", len(dormantIDs))
	}

	const pageSize = 3
	collected := make([]domain.ID, 0, len(dormantIDs))
	for offset := 0; offset < 12; offset += pageSize {
		page, err := repositories.Memories.List(ctx, MemoryListOptions{
			Lifecycle: domain.MemoryLifecycleDormant, Limit: pageSize, Offset: offset,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range page {
			if entry.Lifecycle != domain.MemoryLifecycleDormant {
				t.Fatalf("page at offset %d contained %s memory %s", offset, entry.Lifecycle, entry.ID)
			}
			collected = append(collected, entry.ID)
		}
		if offset == 0 && len(page) != pageSize {
			t.Fatalf("first page returned %d records, want a full page of %d", len(page), pageSize)
		}
	}
	if len(collected) != len(dormantIDs) {
		t.Fatalf("paging returned %d dormant memories (%v), want %d", len(collected), collected, len(dormantIDs))
	}
	seen := make(map[domain.ID]bool, len(collected))
	for _, id := range collected {
		if seen[id] {
			t.Fatalf("memory %s appeared on two pages", id)
		}
		seen[id] = true
		if !dormantIDs[id] {
			t.Fatalf("memory %s is not dormant", id)
		}
	}

	// The pre-fix shape: paginate first, filter afterwards. Kept as an
	// executable statement of why the predicate has to move into SQL.
	mixed, err := repositories.Memories.List(ctx, MemoryListOptions{IncludeDormant: true, Limit: pageSize})
	if err != nil {
		t.Fatal(err)
	}
	postFiltered := 0
	for _, entry := range mixed {
		if entry.Lifecycle == domain.MemoryLifecycleDormant {
			postFiltered++
		}
	}
	if postFiltered >= pageSize {
		t.Fatalf("post-pagination filtering unexpectedly kept a full page (%d of %d)", postFiltered, pageSize)
	}
}

// TestListCoreBoundsTheQueryNotTheResult is the M-1 regression: the core
// prefix predicate and the item budget both belong in SQL, so assembling a
// context does not pull the whole active corpus into the heap.
func TestListCoreBoundsTheQueryNotTheResult(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	for index := 0; index < 30; index++ {
		item := stage2Memory(now, fmt.Sprintf("memory-core-%02d", index), fmt.Sprintf("core candidate %02d", index))
		item.Salience = 0.9 - float64(index)*0.02
		if err := repositories.Memories.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	excluded := map[string]func(*domain.Memory){
		"memory-core-hidden":    func(m *domain.Memory) { m.HiddenFromCore = true },
		"memory-core-sensitive": func(m *domain.Memory) { m.Sensitivity = domain.MemorySensitivityHighlySensitive },
		"memory-core-dormant":   func(m *domain.Memory) { m.Lifecycle = domain.MemoryLifecycleDormant },
	}
	for id, mutate := range excluded {
		item := stage2Memory(now, id, "core candidate that must never enter the prefix")
		// Highest salience: these would sort first if they were not filtered.
		item.Salience = 1
		mutate(&item)
		if err := repositories.Memories.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	core, err := repositories.Memories.ListCore(ctx, CoreMemoryOptions{MaxItems: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(core) != 5 {
		t.Fatalf("core prefix = %d records, want 5", len(core))
	}
	for _, item := range core {
		if _, forbidden := excluded[string(item.ID)]; forbidden {
			t.Fatalf("excluded memory %s entered the core prefix", item.ID)
		}
		if item.Lifecycle != domain.MemoryLifecycleActive || item.HiddenFromCore {
			t.Fatalf("core prefix contained %#v", item)
		}
	}
	if core[0].ID != "memory-core-00" {
		t.Fatalf("core prefix is not salience ordered: %s first", core[0].ID)
	}

	// With a token budget the query keeps bounded headroom for the items the
	// budget skips; without one the SQL predicate is exact.
	if got := coreScanLimit(CoreMemoryOptions{MaxItems: 16}); got != 16 {
		t.Fatalf("coreScanLimit without a token budget = %d, want 16", got)
	}
	if got := coreScanLimit(CoreMemoryOptions{MaxItems: 16, MaxTokens: 2000}); got != 16*coreScanHeadroom {
		t.Fatalf("coreScanLimit with a token budget = %d, want %d", got, 16*coreScanHeadroom)
	}
	if got := coreScanLimit(CoreMemoryOptions{MaxTokens: 2000}); got != 0 {
		t.Fatalf("coreScanLimit without an item budget = %d, want 0 (unbounded)", got)
	}
}

// TestListVersionsReadsFullRevisionsInOneQuery covers the H-16 rewrite of the
// version listing: full rows, one query, same order and metadata.
func TestListVersionsReadsFullRevisionsInOneQuery(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	item := stage2Memory(now, "memory-journal", "revision one")
	if err := repositories.Memories.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	for revision := 2; revision <= 4; revision++ {
		current, err := repositories.Memories.Get(ctx, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		current.Version++
		current.Content = fmt.Sprintf("revision %d", revision)
		current.Reason = fmt.Sprintf("reason %d", revision)
		current.UpdatedAt = now.Add(time.Duration(revision) * time.Minute)
		if err := repositories.Memories.Save(ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := repositories.Memories.ListVersions(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 4 {
		t.Fatalf("versions = %d, want 4", len(versions))
	}
	for index, record := range versions {
		wantVersion := uint64(4 - index)
		if record.Memory.Version != wantVersion {
			t.Fatalf("record %d version = %d, want %d (newest first)", index, record.Memory.Version, wantVersion)
		}
		wantContent := fmt.Sprintf("revision %d", wantVersion)
		if wantVersion == 1 {
			wantContent = "revision one"
		}
		if record.Memory.Content != wantContent {
			t.Fatalf("record %d content = %q, want %q", index, record.Memory.Content, wantContent)
		}
		if record.RevisionID != domain.ID(fmt.Sprintf("memory-journal:v%d", wantVersion)) {
			t.Fatalf("record %d revision id = %q", index, record.RevisionID)
		}
		if wantVersion == 1 {
			if record.Operation != "create" || record.ParentVersion != 0 {
				t.Fatalf("create record = %#v", record)
			}
			continue
		}
		if record.Operation != "update" || record.ParentVersion != wantVersion-1 {
			t.Fatalf("record %d = %#v", index, record)
		}
		if record.Reason != fmt.Sprintf("reason %d", wantVersion) {
			t.Fatalf("record %d reason = %q", index, record.Reason)
		}
	}
	limited, err := repositories.Memories.ListVersions(ctx, item.ID, 2)
	if err != nil || len(limited) != 2 || limited[0].Memory.Version != 4 {
		t.Fatalf("limited versions = %#v, %v", limited, err)
	}
	single, err := repositories.Memories.GetVersionRecord(ctx, item.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if single.Memory.Content != "revision 3" || single.Operation != "update" || single.ParentVersion != 2 || single.Reason != "reason 3" {
		t.Fatalf("version record = %#v", single)
	}
}

// TestRecordRecallForAgentStaysScoped keeps the agent boundary on the new
// counter path.
func TestRecordRecallForAgentStaysScoped(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	for _, profileID := range []domain.ID{"agent-a", "agent-b"} {
		profile, profileErr := domain.NewAgentProfile(profileID, string(profileID), 20, "female", "", now)
		if profileErr != nil {
			t.Fatal(profileErr)
		}
		if err := repositories.Agents.Create(ctx, profile); err != nil {
			t.Fatal(err)
		}
	}
	item := stage2Memory(now, "memory-scoped", "agent a remembers the kettle")
	item.AgentID = "agent-a"
	item.Scope = domain.MemoryScopeAgentPrivate
	if err := repositories.Memories.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Memories.RecordRecallForAgent(ctx, "agent-b", item.ID, now.Add(time.Minute)); err == nil {
		t.Fatal("cross-agent recall was accepted")
	}
	if got := countMemoryTableRows(t, database, `SELECT COUNT(*) FROM memory_recalls WHERE memory_id = ?`, "memory-scoped"); got != 0 {
		t.Fatalf("rejected recall still wrote %d counter rows", got)
	}
	recalled, err := repositories.Memories.RecordRecallForAgent(ctx, "agent-a", item.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if recalled.AccessCount != 1 || recalled.Version != 1 {
		t.Fatalf("scoped recall = %#v", recalled)
	}
	scoped, err := repositories.Memories.GetForAgent(ctx, "agent-a", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if scoped.AccessCount != 1 {
		t.Fatalf("scoped access count = %d, want 1", scoped.AccessCount)
	}
}

// TestMigrationHealsLegacyRecallAmplification exercises the upgrade path for a
// database written by the previous scheme: touch revisions in the journal and
// one memory_fts row per revision. Migration 11 must carry the accumulated
// access count into the counter table and prune the dead index rows, without
// deleting anything from the append-only journal.
func TestMigrationHealsLegacyRecallAmplification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite3")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var recallMigration Migration
	for _, migration := range migrations {
		if migration.Version == 11 {
			recallMigration = migration
			continue
		}
		if migration.Version > 11 {
			continue
		}
		if _, err := database.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}
	if recallMigration.Version != 11 {
		t.Fatal("migration 11 is missing")
	}

	// One logical memory whose head is revision 6 because it was recalled five
	// times, with one index row left behind per revision.
	for version := 1; version <= 6; version++ {
		if _, err := database.ExecContext(ctx, `
			INSERT INTO memory_versions(
				memory_id, agent_id, scope, version, revision_id, operation, parent_version,
				kind, nature, content_text, summary, lifecycle_state, access_count,
				last_accessed_at, last_recalled_at, created_at, updated_at
			) VALUES (?, 'owner', 'agent_private', ?, ?, ?, ?, 'semantic', 'fact',
				'the owner keeps a telescope', 'telescope', 'active', ?, ?, ?, ?, ?)`,
			"memory-legacy", version, fmt.Sprintf("memory-legacy:v%d", version),
			map[bool]string{true: "create", false: "touch"}[version == 1], version-1,
			version-1,
			"2026-08-29T09:00:00Z", "2026-08-29T09:00:00Z",
			"2026-08-29T09:00:00Z", "2026-08-29T09:00:00Z"); err != nil {
			t.Fatalf("seed revision %d: %v", version, err)
		}
		if _, err := database.ExecContext(ctx, `
			INSERT INTO memory_fts(memory_id, memory_version, kind, nature, content, summary)
			VALUES (?, ?, 'semantic', 'fact', 'the owner keeps a telescope', 'telescope')`,
			"memory-legacy", version); err != nil {
			t.Fatalf("seed index row %d: %v", version, err)
		}
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO memory_heads(memory_id, version, updated_at) VALUES (?, 6, ?)`,
		"memory-legacy", "2026-08-29T09:00:00Z"); err != nil {
		t.Fatal(err)
	}

	if _, err := database.ExecContext(ctx, recallMigration.SQL); err != nil {
		t.Fatalf("apply migration 11: %v", err)
	}

	if got := countMemoryTableRows(t, database, `SELECT COUNT(*) FROM memory_fts WHERE memory_id = ?`, "memory-legacy"); got != 1 {
		t.Fatalf("index rows after migration = %d, want 1", got)
	}
	var indexedVersion int
	if err := database.QueryRow(`SELECT CAST(memory_version AS INTEGER) FROM memory_fts WHERE memory_id = ?`, "memory-legacy").Scan(&indexedVersion); err != nil {
		t.Fatal(err)
	}
	if indexedVersion != 6 {
		t.Fatalf("surviving index row is version %d, want the head version 6", indexedVersion)
	}
	if got := countMemoryTableRows(t, database, `SELECT COUNT(*) FROM memory_versions WHERE memory_id = ?`, "memory-legacy"); got != 6 {
		t.Fatalf("journal rows after migration = %d, want all 6 kept", got)
	}
	var accessCount int
	var lastRecalledAt string
	if err := database.QueryRow(`SELECT access_count, last_recalled_at FROM memory_recalls WHERE memory_id = ?`, "memory-legacy").
		Scan(&accessCount, &lastRecalledAt); err != nil {
		t.Fatalf("recall counter was not seeded: %v", err)
	}
	if accessCount != 5 || lastRecalledAt != "2026-08-29T09:00:00Z" {
		t.Fatalf("seeded counter = %d / %q, want 5 / the head recall timestamp", accessCount, lastRecalledAt)
	}

	// A recall recorded after the upgrade continues from the historical count.
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	recalled, err := repositories.Memories.RecordRecall(ctx, "memory-legacy", time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if recalled.AccessCount != 6 || recalled.Version != 6 {
		t.Fatalf("post-upgrade recall = %#v, want access count 6 at version 6", recalled)
	}
	if got := countMemoryTableRows(t, database, `SELECT COUNT(*) FROM memory_versions WHERE memory_id = ?`, "memory-legacy"); got != 6 {
		t.Fatalf("post-upgrade recall appended a revision: %d journal rows", got)
	}
}
