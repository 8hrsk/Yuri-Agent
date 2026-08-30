package desktop

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// The bridge is where a round-trip count is worth asserting: the pool is a
// single connection, so every extra statement the conversation list issues
// serializes against every writer in the process. Durations would only measure
// the machine. The driver below counts statements the same way the storage
// package's own counting driver does — it deliberately implements neither
// QueryerContext nor ExecerContext, so database/sql routes everything through
// PrepareContext and each query is counted exactly once.

type bridgeCountingDriver struct {
	inner driver.Driver
	count *atomic.Int64
}

func (d *bridgeCountingDriver) Open(name string) (driver.Conn, error) {
	connection, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &bridgeCountingConn{inner: connection, count: d.count}, nil
}

type bridgeCountingConn struct {
	inner driver.Conn
	count *atomic.Int64
}

func (c *bridgeCountingConn) Prepare(query string) (driver.Stmt, error) {
	statement, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &bridgeCountingStmt{inner: statement, count: c.count}, nil
}

func (c *bridgeCountingConn) Close() error { return c.inner.Close() }

func (c *bridgeCountingConn) Begin() (driver.Tx, error) { return c.inner.Begin() } //nolint:staticcheck // driver.Conn requires it

type bridgeCountingStmt struct {
	inner driver.Stmt
	count *atomic.Int64
}

func (s *bridgeCountingStmt) Close() error  { return s.inner.Close() }
func (s *bridgeCountingStmt) NumInput() int { return s.inner.NumInput() }

func (s *bridgeCountingStmt) Exec(args []driver.Value) (driver.Result, error) { //nolint:staticcheck // driver.Stmt requires it
	return s.inner.Exec(args)
}

func (s *bridgeCountingStmt) Query(args []driver.Value) (driver.Rows, error) { //nolint:staticcheck // driver.Stmt requires it
	s.count.Add(1)
	return s.inner.Query(args)
}

var (
	bridgeCountingOnce    sync.Once
	bridgeCountingQueries atomic.Int64
)

// newCountingBridge builds a bridge over a migrated database whose statements
// are counted, and returns the id of the agent that owns what it creates.
func newCountingBridge(t *testing.T) *Bridge {
	t.Helper()
	bridgeCountingOnce.Do(func() {
		probe, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		inner := probe.Driver()
		_ = probe.Close()
		sql.Register("sqlite-bridge-counting", &bridgeCountingDriver{inner: inner, count: &bridgeCountingQueries})
	})
	root := t.TempDir()
	paths := config.Paths{
		ConfigDirectory: filepath.Join(root, "config"), ConfigFile: filepath.Join(root, "config", "config.json"),
		DataDirectory: filepath.Join(root, "data"), DatabaseFile: filepath.Join(root, "data", "yuri.sqlite3"),
		PebbleDirectory: filepath.Join(root, "data", "pebble"), BlobDirectory: filepath.Join(root, "data", "blobs"),
		LogDirectory: filepath.Join(root, "data", "logs"), PluginDirectory: filepath.Join(root, "data", "plugins"),
	}
	if err := os.MkdirAll(paths.DataDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite-bridge-counting", "file:"+paths.DatabaseFile+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	if err := storage.Migrate(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{database: database, repositories: repositories, paths: paths, config: config.Default(paths)}
	if _, err := bridge.CreateAgent(CreateAgentInput{Name: "Юри", Gender: "female"}); err != nil {
		t.Fatal(err)
	}
	return bridge
}

func countBridgeQueries(body func()) int64 {
	before := bridgeCountingQueries.Load()
	body()
	return bridgeCountingQueries.Load() - before
}

// seedConversations creates count conversations owned by the bridge's active
// agent, each with messagesEach messages, and returns their ids newest-updated
// first — the order the conversation list returns them in.
func seedConversations(t *testing.T, bridge *Bridge, count, messagesEach int) []string {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	newestFirst := make([]string, count)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("conversation-%04d", index)
		// updated_at ascends with the index, so the list — ordered
		// updated_at DESC — returns the highest index first.
		updated := base.Add(time.Duration(index) * time.Minute)
		if err := bridge.repositories.Conversations.Create(ctx, storage.Conversation{
			ID: domain.ID(id), AgentID: bridge.personaProfileID(), Title: fmt.Sprintf("Диалог %04d", index),
			CreatedAt: base, UpdatedAt: updated,
		}); err != nil {
			t.Fatal(err)
		}
		for message := 0; message < messagesEach; message++ {
			if err := bridge.repositories.Messages.Create(ctx, storage.Message{
				ID: domain.ID(fmt.Sprintf("%s-msg-%04d", id, message)), ConversationID: domain.ID(id),
				Role: "user", Content: fmt.Sprintf("Реплика %d диалога %04d", message, index),
				Status: "complete", CreatedAt: updated.Add(time.Duration(message) * time.Second),
			}); err != nil {
				t.Fatal(err)
			}
		}
		newestFirst[count-1-index] = id
	}
	return newestFirst
}

// seedRunWithCall gives a conversation one run carrying one tool call, so the
// transcript-carrying page actually exercises all four of its reads. Without a
// run the tool-call read is skipped for an empty id set and the page costs
// three, which would make a "four queries" assertion pass for the wrong reason.
func seedRunWithCall(t *testing.T, bridge *Bridge, conversationID string, index int) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Minute)
	runID := domain.ID(conversationID + "-run")
	if err := bridge.repositories.Runs.Create(ctx, domain.AgentRun{
		ID: runID, AgentID: bridge.personaProfileID(), Kind: domain.RunKindInteractive,
		ConversationID: domain.ID(conversationID), State: domain.RunStateQueued,
		Budget:  domain.RunBudget{MaxSteps: 8, MaxTokens: 4000, MaxToolCalls: 4, MaxToolOutputBytes: 4096, MaxDurationSeconds: 60},
		Version: 1, CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.ToolCalls.Create(ctx, storage.ToolCall{
		ID: runID + "-call", RunID: runID, ToolID: "filesystem.read", ArgsRedacted: "{}",
		Risk: domain.RiskLow, Status: storage.ToolCallSucceeded, IdempotencyKey: string(runID) + "-key",
		Version: 1, CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestConversationListPagesBeyondTheFirstPage is M-10's headline property: a
// store larger than one page must be reachable. The sidebar used to call the
// list with no offset and receive the newest 200 conversations, with nothing in
// the response saying that anything had been left out — an owner with 250
// conversations simply could not open the oldest 50.
//
// It asserts the identity of the conversations each page returns, not merely
// that more than 200 arrived, and it asserts that walking the offsets
// terminates rather than repeating the last page forever.
func TestConversationListPagesBeyondTheFirstPage(t *testing.T) {
	bridge := newCountingBridge(t)
	const total = 250
	newestFirst := seedConversations(t, bridge, total, 2)

	first, err := bridge.ListConversationsPage(ConversationPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != maxConversationPageLimit {
		t.Fatalf("first page = %d conversations, want %d", len(first), maxConversationPageLimit)
	}
	if first[0].ID != newestFirst[0] || first[len(first)-1].ID != newestFirst[maxConversationPageLimit-1] {
		t.Fatalf("first page spans %s..%s, want %s..%s",
			first[0].ID, first[len(first)-1].ID, newestFirst[0], newestFirst[maxConversationPageLimit-1])
	}

	// The page that was unreachable. Every one of the remaining 50, by id, in
	// order — not a count.
	second, err := bridge.ListConversationsPage(ConversationPageOptions{Offset: maxConversationPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != total-maxConversationPageLimit {
		t.Fatalf("second page = %d conversations, want %d", len(second), total-maxConversationPageLimit)
	}
	for offset, view := range second {
		if want := newestFirst[maxConversationPageLimit+offset]; view.ID != want {
			t.Fatalf("second page[%d] = %s, want %s", offset, view.ID, want)
		}
	}
	// conversation-0000 is the oldest of the 250 and the last one the walk
	// reaches; before the offset was passed it was unreachable entirely.
	if second[len(second)-1].ID != "conversation-0000" {
		t.Fatalf("last conversation of the walk = %s, want conversation-0000", second[len(second)-1].ID)
	}

	// Termination: the page past the end is empty, so a renderer walking
	// offsets stops instead of re-reading the tail forever.
	past, err := bridge.ListConversationsPage(ConversationPageOptions{Offset: total})
	if err != nil {
		t.Fatal(err)
	}
	if len(past) != 0 {
		t.Fatalf("page past the end = %d conversations, want none", len(past))
	}

	// Every conversation is covered exactly once by the walk.
	seen := make(map[string]struct{}, total)
	for _, page := range [][]ConversationView{first, second, past} {
		for _, view := range page {
			if _, duplicate := seen[view.ID]; duplicate {
				t.Fatalf("conversation %s appeared on two pages", view.ID)
			}
			seen[view.ID] = struct{}{}
		}
	}
	if len(seen) != total {
		t.Fatalf("the walk covered %d conversations, want %d", len(seen), total)
	}
}

// TestConversationMetadataPageCostsTwoQueriesAndCarriesNoTranscripts is the
// M-10 payload property, and the H-16/M-35 preservation guard alongside it.
//
// The transcript-carrying shape must still cost four queries however many
// conversations are on the page — that is the property H-16/M-35 bought, and
// this test fails if a change to the metadata path reintroduces a per-owner
// read there. The metadata shape must cost two and carry no transcripts, while
// still carrying the preview the sidebar draws: "metadata only" that dropped
// the snippet would be a different, worse list.
func TestConversationMetadataPageCostsTwoQueriesAndCarriesNoTranscripts(t *testing.T) {
	bridge := newCountingBridge(t)
	const conversations, messagesEach = 40, 12
	ids := seedConversations(t, bridge, conversations, messagesEach)
	for index, id := range ids {
		seedRunWithCall(t, bridge, id, index)
	}

	var metadata []ConversationView
	var err error
	queries := countBridgeQueries(func() {
		metadata, err = bridge.ListConversationsPage(ConversationPageOptions{})
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 2 {
		t.Fatalf("metadata page issued %d queries for %d conversations, want 2", queries, conversations)
	}
	if len(metadata) != conversations {
		t.Fatalf("metadata page = %d conversations, want %d", len(metadata), conversations)
	}
	for _, view := range metadata {
		if len(view.Messages) != 0 || len(view.Traces) != 0 {
			t.Fatalf("%s carried %d messages and %d traces on a metadata page", view.ID, len(view.Messages), len(view.Traces))
		}
		if view.HasMoreMessages {
			t.Fatalf("%s claimed more history on a page that loaded no transcript", view.ID)
		}
		if view.Title == "" || view.UpdatedAt == "" {
			t.Fatalf("%s lost its metadata: %#v", view.ID, view)
		}
	}
	// The snippet survives, and it is the newest message rather than an
	// arbitrary one — the sidebar shows the last thing said.
	newest := metadata[0]
	if want := fmt.Sprintf("Реплика %d диалога 0039", messagesEach-1); newest.Preview != want {
		t.Fatalf("preview = %q, want %q", newest.Preview, want)
	}

	// Preservation guard (H-16/M-35): the transcript-carrying shape is still
	// five queries for the whole page (the fifth batch-loads anonymous child
	// traces), not one read per conversation. Asserted twice at different page
	// sizes, because "five" and "constant in the page
	// size" are different claims and only the second is the property that
	// matters — a per-conversation read would satisfy neither.
	var full []ConversationView
	queries = countBridgeQueries(func() {
		full, err = bridge.ListConversationsPage(ConversationPageOptions{MessageLimit: 5})
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 5 {
		t.Fatalf("transcript page issued %d queries for %d conversations, want 5", queries, conversations)
	}
	narrow := countBridgeQueries(func() {
		_, err = bridge.ListConversationsPage(ConversationPageOptions{Limit: 4, MessageLimit: 5})
	})
	if err != nil {
		t.Fatal(err)
	}
	if narrow != queries {
		t.Fatalf("transcript page cost %d queries for 4 conversations and %d for %d: the cost must not scale with the page",
			narrow, queries, conversations)
	}
	if len(full) != conversations || len(full[0].Messages) != 5 || !full[0].HasMoreMessages {
		t.Fatalf("transcript page = %d conversations, first carried %d messages, hasMore=%v",
			len(full), len(full[0].Messages), full[0].HasMoreMessages)
	}
	// Both shapes agree on the snippet, so opening the sidebar and opening a
	// conversation cannot disagree about what the last message was.
	if full[0].Preview != newest.Preview {
		t.Fatalf("transcript page preview = %q, metadata page preview = %q", full[0].Preview, newest.Preview)
	}
}

// TestUnknownCursorIsDistinguishableFromTheTranscriptStart is the second half
// of the paging fix, at the bridge.
//
// "That cursor is not a message here" and "there is nothing older" used to be
// the same answer — an empty page — so a renderer holding a stale or
// never-persisted id stopped paging with no way to tell a bug from the end of
// the list. The genuine end of the transcript must stay an ordinary empty page,
// or the renderer would report an error every time a reader reached the top.
func TestUnknownCursorIsDistinguishableFromTheTranscriptStart(t *testing.T) {
	bridge, conversationID := newConversationBridge(t)
	seeded := seedBridgeTranscript(t, bridge, conversationID, 12)

	// The real start of the transcript: empty page, no error, no more history.
	start, err := bridge.ListMessages(conversationID, 5, seeded[0])
	if err != nil {
		t.Fatalf("the start of the transcript must not be an error: %v", err)
	}
	if len(start.Messages) != 0 || start.HasMore {
		t.Fatalf("page before the first message = %d messages, hasMore=%v", len(start.Messages), start.HasMore)
	}

	// An id that was never persisted — the shape the renderer mints for its
	// optimistic user bubble, which never reaches the store.
	if _, err := bridge.ListMessages(conversationID, 5, "user-3f1c9d2e-0000-4000-8000-000000000000"); err == nil {
		t.Fatal("an unknown cursor must not be answered with the same empty page as the transcript start")
	} else if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("unknown cursor error = %v, want ErrInvalidArgument", err)
	}
}
