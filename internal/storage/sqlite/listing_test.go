package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// countingDriver wraps the real SQLite driver and counts the statements the
// repositories execute. It deliberately does not implement QueryerContext or
// ExecerContext, so database/sql routes every statement through PrepareContext
// and each query is counted exactly once.
type countingDriver struct {
	inner driver.Driver
	count *atomic.Int64
}

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	connection, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConn{inner: connection, count: d.count}, nil
}

type countingConn struct {
	inner driver.Conn
	count *atomic.Int64
}

func (c *countingConn) Prepare(query string) (driver.Stmt, error) {
	statement, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &countingStmt{inner: statement, count: c.count}, nil
}

func (c *countingConn) Close() error { return c.inner.Close() }

func (c *countingConn) Begin() (driver.Tx, error) { return c.inner.Begin() } //nolint:staticcheck // driver.Conn requires it

type countingStmt struct {
	inner driver.Stmt
	count *atomic.Int64
}

func (s *countingStmt) Close() error  { return s.inner.Close() }
func (s *countingStmt) NumInput() int { return s.inner.NumInput() }

func (s *countingStmt) Exec(args []driver.Value) (driver.Result, error) { //nolint:staticcheck // driver.Stmt requires it
	return s.inner.Exec(args)
}

func (s *countingStmt) Query(args []driver.Value) (driver.Rows, error) { //nolint:staticcheck // driver.Stmt requires it
	s.count.Add(1)
	return s.inner.Query(args)
}

var (
	countingDriverOnce sync.Once
	countingQueries    atomic.Int64
)

// countingDatabase opens a fully migrated database whose queries are counted.
func countingDatabase(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	countingDriverOnce.Do(func() {
		probe, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		inner := probe.Driver()
		_ = probe.Close()
		sql.Register("sqlite-counting", &countingDriver{inner: inner, count: &countingQueries})
	})
	path := filepath.Join(t.TempDir(), "counting.sqlite3")
	database, err := sql.Open("sqlite-counting", sqliteFileDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	return database, ctx
}

// countQueries reports how many statements the body executed.
func countQueries(body func()) int64 {
	before := countingQueries.Load()
	body()
	return countingQueries.Load() - before
}

func seedAuditEvents(t *testing.T, repositories *Repositories, ctx context.Context, count int) []AuditEvent {
	t.Helper()
	base := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	events := make([]AuditEvent, 0, count)
	for index := 0; index < count; index++ {
		event := AuditEvent{
			ID:        domain.ID(fmt.Sprintf("audit-%03d", index)),
			Actor:     domain.ActorSystem,
			Action:    "notification.sent",
			Target:    fmt.Sprintf("target-%03d", index),
			CreatedAt: base.Add(time.Duration(index) * time.Minute),
		}
		if err := repositories.Audit.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

// TestAuditListReadsOneQueryAndKeepsOrder is the safety property for H-16: the
// rewritten list must return exactly the rows the id-then-get version did, in
// the same order, and must do it in a single round-trip.
func TestAuditListReadsOneQueryAndKeepsOrder(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	seeded := seedAuditEvents(t, repositories, ctx, 25)

	var listed []AuditEvent
	queries := countQueries(func() {
		listed, err = repositories.Audit.List(ctx, 25)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("Audit.List issued %d queries, want 1", queries)
	}
	if len(listed) != len(seeded) {
		t.Fatalf("Audit.List returned %d events, want %d", len(listed), len(seeded))
	}
	// The repository orders newest first.
	for index, event := range listed {
		want := seeded[len(seeded)-1-index]
		if event.ID != want.ID {
			t.Fatalf("event %d = %q, want %q", index, event.ID, want.ID)
		}
		if event.Target != want.Target || event.Action != want.Action || !event.CreatedAt.Equal(want.CreatedAt) {
			t.Fatalf("event %d = %#v, want %#v", index, event, want)
		}
	}
}

// TestAuditListPagination covers M-7: an explicit limit, an offset, and the
// bound applied when the caller supplies no limit at all.
func TestAuditListPagination(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	seeded := seedAuditEvents(t, repositories, ctx, 12)
	newest := func(offset int) domain.ID { return seeded[len(seeded)-1-offset].ID }

	page, err := repositories.Audit.List(ctx, 5)
	if err != nil || len(page) != 5 || page[0].ID != newest(0) {
		t.Fatalf("limited page = %#v, err = %v", page, err)
	}
	second, err := repositories.Audit.List(ctx, 5, 5)
	if err != nil || len(second) != 5 || second[0].ID != newest(5) {
		t.Fatalf("offset page = %#v, err = %v", second, err)
	}
	tail, err := repositories.Audit.List(ctx, 5, 10)
	if err != nil || len(tail) != 2 || tail[0].ID != newest(10) {
		t.Fatalf("tail page = %#v, err = %v", tail, err)
	}
	if _, err := repositories.Audit.List(ctx, -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative limit error = %v", err)
	}
	if _, err := repositories.Audit.List(ctx, 5, -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative offset error = %v", err)
	}
	all, err := repositories.Audit.List(ctx)
	if err != nil || len(all) != len(seeded) {
		t.Fatalf("default page = %d events, err = %v", len(all), err)
	}
}

// TestAuditListAppliesDefaultLimit proves the no-limit call is no longer an
// unbounded read of the whole journal.
func TestAuditListAppliesDefaultLimit(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	seeded := seedAuditEvents(t, repositories, ctx, defaultListLimit+5)
	all, err := repositories.Audit.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != defaultListLimit {
		t.Fatalf("default limit returned %d events, want %d", len(all), defaultListLimit)
	}
	// The default window is the newest events, which is what every caller of
	// the unbounded form actually wants.
	if all[0].ID != seeded[len(seeded)-1].ID {
		t.Fatalf("default window starts at %q, want newest %q", all[0].ID, seeded[len(seeded)-1].ID)
	}
	clamped, err := repositories.Audit.List(ctx, maxListLimit+1)
	if err != nil || len(clamped) != len(seeded) {
		t.Fatalf("clamped limit returned %d events, err = %v", len(clamped), err)
	}
}

func seedRunFixture(t *testing.T, repositories *Repositories, ctx context.Context, count int) []domain.AgentRun {
	t.Helper()
	base := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	conversation := Conversation{ID: "listing-conversation", AgentID: "owner", Title: "listing", CreatedAt: base, UpdatedAt: base}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	runs := make([]domain.AgentRun, 0, count)
	for index := 0; index < count; index++ {
		run, err := domain.NewRunForAgent("owner", domain.ID(fmt.Sprintf("run-%03d", index)),
			domain.RunKindInteractive, conversation.ID, base.Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := repositories.Runs.Create(ctx, run); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	return runs
}

// TestRunListReadsOneQueryAndKeepsOrder is the same safety property for the
// call site the review named first, runs.go list().
func TestRunListReadsOneQueryAndKeepsOrder(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	seeded := seedRunFixture(t, repositories, ctx, 20)

	var listed []domain.AgentRun
	queries := countQueries(func() {
		listed, err = repositories.Runs.ListByConversation(ctx, "listing-conversation")
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("Runs.ListByConversation issued %d queries, want 1", queries)
	}
	if len(listed) != len(seeded) {
		t.Fatalf("listed %d runs, want %d", len(listed), len(seeded))
	}
	for index, run := range listed {
		want := seeded[index]
		if run.ID != want.ID || run.AgentID != want.AgentID || run.State != want.State ||
			run.Budget != want.Budget || !run.CreatedAt.Equal(want.CreatedAt) {
			t.Fatalf("run %d = %#v, want %#v", index, run, want)
		}
	}

	byKind, err := repositories.Runs.ListByKind(ctx, domain.RunKindInteractive, 5, 5)
	if err != nil || len(byKind) != 5 || byKind[0].ID != seeded[5].ID {
		t.Fatalf("ListByKind window = %#v, err = %v", byKind, err)
	}
	if _, err := repositories.Runs.ListByAgent(ctx, "owner", 1, -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative offset error = %v", err)
	}
}

// TestToolCallListReadsOneQuery covers tool_calls.go ListByRun.
func TestToolCallListReadsOneQuery(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	runs := seedRunFixture(t, repositories, ctx, 1)
	base := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	for index := 0; index < 8; index++ {
		call := ToolCall{
			ID: domain.ID(fmt.Sprintf("call-%03d", index)), RunID: runs[0].ID, ToolID: "tool.echo",
			ArgsRedacted: `{}`, Risk: domain.RiskLow, Status: ToolCallSucceeded, Version: 1,
			CreatedAt: base.Add(time.Duration(index) * time.Second), UpdatedAt: base.Add(time.Duration(index) * time.Second),
		}
		if err := repositories.ToolCalls.Create(ctx, call); err != nil {
			t.Fatal(err)
		}
	}
	var listed []ToolCall
	queries := countQueries(func() {
		listed, err = repositories.ToolCalls.ListByRun(ctx, runs[0].ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("ToolCalls.ListByRun issued %d queries, want 1", queries)
	}
	if len(listed) != 8 || listed[0].ID != "call-000" || listed[7].ID != "call-007" {
		t.Fatalf("listed tool calls = %#v", listed)
	}
	if listed[3].ToolID != "tool.echo" || listed[3].Risk != domain.RiskLow || listed[3].Status != ToolCallSucceeded {
		t.Fatalf("tool call fields = %#v", listed[3])
	}
	page, err := repositories.ToolCalls.ListByRun(ctx, runs[0].ID, 3, 2)
	if err != nil || len(page) != 3 || page[0].ID != "call-002" {
		t.Fatalf("tool call page = %#v, err = %v", page, err)
	}
}

// TestMessageListWindowIsTranscriptTail covers M-7 for the transcript reader:
// the window is the end of the conversation, returned chronologically, and an
// omitted limit no longer reads every message with its full content.
func TestMessageListWindowIsTranscriptTail(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	conversation := Conversation{ID: "transcript", AgentID: "owner", Title: "transcript", CreatedAt: base, UpdatedAt: base}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	total := defaultListLimit + 4
	for index := 0; index < total; index++ {
		if err := repositories.Messages.Create(ctx, Message{
			ID: domain.ID(fmt.Sprintf("msg-%04d", index)), ConversationID: conversation.ID, Role: "user",
			Content: fmt.Sprintf("body %d", index), Status: "complete",
			CreatedAt: base.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var listed []Message
	queries := countQueries(func() {
		listed, err = repositories.Messages.ListByConversation(ctx, conversation.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("Messages.ListByConversation issued %d queries, want 1", queries)
	}
	if len(listed) != defaultListLimit {
		t.Fatalf("default window returned %d messages, want %d", len(listed), defaultListLimit)
	}
	if listed[len(listed)-1].ID != domain.ID(fmt.Sprintf("msg-%04d", total-1)) {
		t.Fatalf("default window ends at %q, want the newest message", listed[len(listed)-1].ID)
	}
	for index := 1; index < len(listed); index++ {
		if listed[index-1].CreatedAt.After(listed[index].CreatedAt) {
			t.Fatalf("window is not chronological at %d", index)
		}
	}

	tail, err := repositories.Messages.ListByConversation(ctx, conversation.ID, 3)
	if err != nil || len(tail) != 3 || tail[2].ID != domain.ID(fmt.Sprintf("msg-%04d", total-1)) {
		t.Fatalf("tail = %#v, err = %v", tail, err)
	}
	if tail[0].Content != fmt.Sprintf("body %d", total-3) {
		t.Fatalf("tail content = %q", tail[0].Content)
	}
	offset, err := repositories.Messages.ListByConversation(ctx, conversation.ID, 3, 3)
	if err != nil || len(offset) != 3 || offset[2].ID != domain.ID(fmt.Sprintf("msg-%04d", total-4)) {
		t.Fatalf("offset window = %#v, err = %v", offset, err)
	}
	if _, err := repositories.Messages.ListByConversation(ctx, conversation.ID, -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative limit error = %v", err)
	}
}

// TestConversationListPagesAndBounds covers M-7 for ConversationRepository.
func TestConversationListPagesAndBounds(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	total := defaultListLimit + 3
	for index := 0; index < total; index++ {
		at := base.Add(time.Duration(index) * time.Minute)
		if err := repositories.Conversations.Create(ctx, Conversation{
			ID: domain.ID(fmt.Sprintf("conv-%04d", index)), AgentID: "owner",
			Title: fmt.Sprintf("chat %d", index), CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := repositories.Conversations.List(ctx)
	if err != nil || len(all) != defaultListLimit {
		t.Fatalf("default list returned %d conversations, err = %v", len(all), err)
	}
	if all[0].ID != domain.ID(fmt.Sprintf("conv-%04d", total-1)) {
		t.Fatalf("default list starts at %q, want the most recently updated", all[0].ID)
	}
	page, err := repositories.Conversations.ListPage(ctx, ConversationListOptions{AgentID: "owner", Limit: 4, Offset: 2})
	if err != nil || len(page) != 4 || page[0].ID != domain.ID(fmt.Sprintf("conv-%04d", total-3)) {
		t.Fatalf("page = %#v, err = %v", page, err)
	}
	if page[0].Title != fmt.Sprintf("chat %d", total-3) || page[0].AgentID != "owner" {
		t.Fatalf("page fields = %#v", page[0])
	}
	if _, err := repositories.Conversations.ListPage(ctx, ConversationListOptions{Limit: -1}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative limit error = %v", err)
	}
}

// TestDelegationListDoesNotStrandConnectionOnScanError is the regression test
// for M-2. The pool holds a single connection, so a list that returns from the
// middle of its row iteration without closing Rows hangs every later database
// call in the process. Only the Scan error path could leak: database/sql closes
// Rows itself once Next reports false, which is why the rows.Err() path was
// already safe and is left alone.
func TestDelegationListDoesNotStrandConnectionOnScanError(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	conversation := Conversation{ID: "leak-conversation", AgentID: "owner", Title: "leak", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	parent, err := domain.NewRunForAgent("owner", "leak-parent", domain.RunKindInteractive, conversation.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		child, err := domain.NewRunForAgent("owner", domain.ID(fmt.Sprintf("leak-child-%d", index)), domain.RunKindSubagent, "", now)
		if err != nil {
			t.Fatal(err)
		}
		child.ParentRunID = parent.ID
		if err := repositories.Runs.Create(ctx, child); err != nil {
			t.Fatal(err)
		}
		delegation, err := domain.NewDelegation(domain.ID(fmt.Sprintf("leak-delegation-%d", index)), child.ID, "owner",
			parent.ID, `{"task":"summarize"}`, fmt.Sprintf("request-%d", index), fmt.Sprintf("hash-%d", index),
			now.Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := repositories.Delegations.Create(ctx, delegation); err != nil {
			t.Fatal(err)
		}
	}
	if listed, err := repositories.Delegations.ListByParent(ctx, "owner", parent.ID); err != nil || len(listed) != 2 {
		t.Fatalf("healthy list = %#v, err = %v", listed, err)
	}

	// Corrupt the second row so scanning fails after the iteration has already
	// started and Rows is still open.
	if _, err := database.ExecContext(ctx,
		`UPDATE delegations SET created_at = 'not-a-timestamp' WHERE id = ?`, "leak-delegation-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Delegations.ListByParent(ctx, "owner", parent.ID); err == nil {
		t.Fatal("list over a corrupt row should fail")
	}

	// The single pooled connection must be back in the pool.
	guarded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var count int
	if err := database.QueryRowContext(guarded, `SELECT count(*) FROM delegations`).Scan(&count); err != nil {
		t.Fatalf("connection stranded after scan error: %v", err)
	}
	if count != 2 {
		t.Fatalf("delegation count = %d, want 2", count)
	}
}

// TestPersonaAndRelationshipHistoryReadOneQuery covers persona.go and
// relationship.go: each revision must come back whole, newest first, from one
// query instead of one GetVersionRecord per version.
func TestPersonaAndRelationshipHistoryReadOneQuery(t *testing.T) {
	database, ctx := countingDatabase(t)
	personas := NewPersonaRepository(database)
	relationships := NewRelationshipRepository(database)
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)

	persona := domain.MutablePersona{
		ID: "persona-listing", Traits: map[string]float64{"warmth": 0.5},
		PromptText: "seed", CreatedAt: now, UpdatedAt: now,
	}
	if err := personas.Create(ctx, persona); err != nil {
		t.Fatal(err)
	}
	traitsByVersion := map[uint64]float64{1: 0.5}
	for version := uint64(2); version <= 4; version++ {
		value := 0.5 + float64(version-1)/100
		next := persona
		next.Version = version
		next.Traits = map[string]float64{"warmth": value}
		next.Reason = fmt.Sprintf("revision %d", version)
		next.Evidence = stage5Evidence()
		next.UpdatedAt = now.Add(time.Duration(version) * time.Minute)
		if _, err := personas.AppendVersion(ctx, next, version-1); err != nil {
			t.Fatal(err)
		}
		traitsByVersion[version] = value
	}

	var history []PersonaVersionRecord
	var err error
	queries := countQueries(func() {
		history, err = personas.ListVersions(ctx, "persona-listing")
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("Persona.ListVersions issued %d queries, want 1", queries)
	}
	if len(history) != 4 {
		t.Fatalf("persona history = %d records, want 4", len(history))
	}
	for index, record := range history {
		wantVersion := uint64(4 - index)
		if record.Persona.Version != wantVersion {
			t.Fatalf("record %d version = %d, want %d", index, record.Persona.Version, wantVersion)
		}
		if got := record.Persona.Traits["warmth"]; got != traitsByVersion[wantVersion] {
			t.Fatalf("record %d warmth = %v, want %v", index, got, traitsByVersion[wantVersion])
		}
		if record.RevisionID == "" || record.Persona.ID != "persona-listing" {
			t.Fatalf("record %d = %#v", index, record)
		}
	}
	page, err := personas.ListVersions(ctx, "persona-listing", 2, 1)
	if err != nil || len(page) != 2 || page[0].Persona.Version != 3 || page[1].Persona.Version != 2 {
		t.Fatalf("persona history page = %#v, err = %v", page, err)
	}
	if _, err := personas.ListVersions(ctx, "persona-listing", -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative persona limit error = %v", err)
	}

	state := domain.RelationshipState{ID: "relationship-listing", Dimensions: map[string]float64{"trust": 0.5}, CreatedAt: now, UpdatedAt: now}
	if err := relationships.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	opinion := domain.RelationshipOpinion{Subject: "owner", Claim: "usually reliable", Confidence: 0.8, Evidence: stage5Evidence(), CreatedAt: now}
	if _, err := relationships.RecordOpinion(ctx, "relationship-listing", 1, opinion, now.Add(time.Minute), "new evidence"); err != nil {
		t.Fatal(err)
	}
	if _, err := relationships.Rollback(ctx, "relationship-listing", uint64(1), "remove opinion", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var relationshipHistory []RelationshipVersionRecord
	queries = countQueries(func() {
		relationshipHistory, err = relationships.ListVersions(ctx, "relationship-listing")
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("Relationship.ListVersions issued %d queries, want 1", queries)
	}
	if len(relationshipHistory) != 3 {
		t.Fatalf("relationship history = %d records, want 3", len(relationshipHistory))
	}
	if relationshipHistory[0].Relationship.Version != 3 || relationshipHistory[2].Relationship.Version != 1 {
		t.Fatalf("relationship history order = %#v", relationshipHistory)
	}
	if len(relationshipHistory[1].Relationship.Opinions) != 1 {
		t.Fatalf("version 2 should carry the recorded opinion: %#v", relationshipHistory[1])
	}
	if len(relationshipHistory[0].Relationship.Opinions) != 0 {
		t.Fatalf("rolled back version should carry no opinion: %#v", relationshipHistory[0])
	}
	relationshipPage, err := relationships.ListVersions(ctx, "relationship-listing", 1, 1)
	if err != nil || len(relationshipPage) != 1 || relationshipPage[0].Relationship.Version != 2 {
		t.Fatalf("relationship history page = %#v, err = %v", relationshipPage, err)
	}
}

// TestPeerDialogueListReadsFullRows keeps the peer dialogue call site honest
// using the fixture the existing peer dialogue tests already build.
func TestPeerDialogueListReadsFullRows(t *testing.T) {
	// Two different pairs: one agent may not hold two active dialogues with the
	// same peer, so agent-a talks to agent-b and to agent-c.
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b", "agent-c")
	first, firstInitial := fixture.newDialogue(t, "dialogue-one", "agent-a", "agent-b", fixture.runs["agent-a"].ID, "request-1", fixture.now)
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, first, firstInitial); err != nil {
		t.Fatal(err)
	}
	second, secondInitial := fixture.newDialogue(t, "dialogue-two", "agent-a", "agent-c", fixture.runs["agent-a"].ID, "request-2", fixture.now.Add(time.Second))
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, second, secondInitial); err != nil {
		t.Fatal(err)
	}

	listed, err := fixture.repos.PeerDialogues.ListByParticipant(fixture.ctx, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d dialogues, want 2", len(listed))
	}
	// Newest first, as before the rewrite.
	if listed[0].ID != second.ID || listed[1].ID != first.ID {
		t.Fatalf("dialogue order = %q, %q", listed[0].ID, listed[1].ID)
	}
	if listed[0].PairKey != second.PairKey || listed[0].Purpose != second.Purpose ||
		listed[0].Budget != second.Budget || listed[0].IdempotencyKey != second.IdempotencyKey {
		t.Fatalf("dialogue fields = %#v, want %#v", listed[0], second)
	}
	page, err := fixture.repos.PeerDialogues.ListByParticipant(fixture.ctx, "agent-a", 1, 1)
	if err != nil || len(page) != 1 || page[0].ID != first.ID {
		t.Fatalf("dialogue page = %#v, err = %v", page, err)
	}
	if _, err := fixture.repos.PeerDialogues.ListByParticipant(fixture.ctx, "agent-a", 1, -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative offset error = %v", err)
	}
}

// seedAffectHistory appends revisions revisions to one affect state, returning
// them oldest first. Emotion values stay inside the [-1, 1] the domain
// validates, which is why the trait value cycles rather than growing.
func seedAffectHistory(t *testing.T, repository *AffectiveRepository, ctx context.Context, id domain.ID, revisions int) []domain.AffectiveState {
	t.Helper()
	base := time.Date(2026, 8, 29, 17, 0, 0, 0, time.UTC)
	seed := domain.AffectiveState{
		ID: id, Version: 1, Emotions: map[string]float64{domain.EmotionJoy: 0.01},
		Summary: "seed", CreatedAt: base, UpdatedAt: base,
	}
	if err := repository.CreateState(ctx, seed); err != nil {
		t.Fatal(err)
	}
	written, err := repository.GetState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	states := make([]domain.AffectiveState, 0, revisions)
	states = append(states, written)
	for version := uint64(2); version <= uint64(revisions); version++ {
		next := domain.AffectiveState{
			ID: id, Version: version,
			Emotions:  map[string]float64{domain.EmotionJoy: float64(version%100) / 100},
			Summary:   fmt.Sprintf("revision %d", version),
			Reason:    fmt.Sprintf("reason %d", version),
			UpdatedAt: base.Add(time.Duration(version) * time.Minute),
		}
		written, err := repository.AppendVersion(ctx, next, version-1)
		if err != nil {
			t.Fatal(err)
		}
		states = append(states, written)
	}
	return states
}

// TestAffectHistoryReadsOneQueryAndKeepsOrder is the H-16 safety property for
// the instance that rewrite missed: AffectiveRepository.ListVersions selected
// version numbers and then re-read each revision with its own
// QueryRowContext, so N revisions cost N+1 round-trips on a pool that is
// deliberately a single connection.
func TestAffectHistoryReadsOneQueryAndKeepsOrder(t *testing.T) {
	database, ctx := countingDatabase(t)
	repository := NewAffectiveRepository(database)
	const revisions = 25
	seeded := seedAffectHistory(t, repository, ctx, "affect-listing", revisions)

	var history []AffectiveVersionRecord
	var err error
	queries := countQueries(func() {
		history, err = repository.ListVersions(ctx, "affect-listing")
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("Affect.ListVersions issued %d queries, want 1", queries)
	}
	if len(history) != revisions {
		t.Fatalf("affect history = %d records, want %d", len(history), revisions)
	}
	// Newest first, every revision whole.
	for index, record := range history {
		want := seeded[len(seeded)-1-index]
		if record.State.Version != want.Version {
			t.Fatalf("record %d version = %d, want %d", index, record.State.Version, want.Version)
		}
		if record.State.ID != want.ID || record.State.Summary != want.Summary {
			t.Fatalf("record %d = %#v, want %#v", index, record.State, want)
		}
		if got := record.State.Emotions[domain.EmotionJoy]; got != want.Emotions[domain.EmotionJoy] {
			t.Fatalf("record %d joy = %v, want %v", index, got, want.Emotions[domain.EmotionJoy])
		}
		if record.RevisionID != want.RevisionID || record.ParentID != want.ParentID ||
			record.ParentVersion != want.ParentVersion || record.Operation != want.Operation ||
			record.Reason != want.Reason {
			t.Fatalf("record %d metadata = %#v, want state %#v", index, record, want)
		}
		if !record.State.CreatedAt.Equal(want.CreatedAt) || !record.State.UpdatedAt.Equal(want.UpdatedAt) {
			t.Fatalf("record %d timestamps = %v/%v", index, record.State.CreatedAt, record.State.UpdatedAt)
		}
	}

	// ListHistory is the alias and must agree exactly.
	alias, err := repository.ListHistory(ctx, "affect-listing")
	if err != nil || len(alias) != len(history) || alias[0].RevisionID != history[0].RevisionID {
		t.Fatalf("ListHistory = %d records, err = %v", len(alias), err)
	}

	page, err := repository.ListVersions(ctx, "affect-listing", 2, 1)
	if err != nil || len(page) != 2 || page[0].State.Version != revisions-1 || page[1].State.Version != revisions-2 {
		t.Fatalf("affect history page = %#v, err = %v", page, err)
	}
	if _, err := repository.ListVersions(ctx, "affect-listing", -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative affect history limit error = %v", err)
	}
	if _, err := repository.ListVersions(ctx, "affect-listing", 1, -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative affect history offset error = %v", err)
	}
}

// TestAffectListVersionsAppliesDefaultLimit proves an unbounded caller no
// longer reads every revision ever written.
func TestAffectListVersionsAppliesDefaultLimit(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewAffectiveRepository(database)
	total := defaultListLimit + 5
	seeded := seedAffectHistory(t, repository, ctx, "affect-bounded", total)

	all, err := repository.ListVersions(ctx, "affect-bounded")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != defaultListLimit {
		t.Fatalf("default affect history returned %d records, want %d", len(all), defaultListLimit)
	}
	// The default window is the newest revisions, which is what a history
	// reader actually wants.
	if all[0].State.Version != seeded[len(seeded)-1].Version {
		t.Fatalf("default window starts at version %d, want %d", all[0].State.Version, seeded[len(seeded)-1].Version)
	}
	full, err := repository.ListVersions(ctx, "affect-bounded", total)
	if err != nil || len(full) != total {
		t.Fatalf("explicit limit returned %d records, err = %v", len(full), err)
	}
	clamped, err := repository.ListVersions(ctx, "affect-bounded", maxListLimit+1)
	if err != nil || len(clamped) != total {
		t.Fatalf("clamped limit returned %d records, err = %v", len(clamped), err)
	}
}

// TestAffectListEventsAppliesDefaultLimit is the same bound for the event
// journal, which defaulted to an unlimited read of an append-only table.
func TestAffectListEventsAppliesDefaultLimit(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewAffectiveRepository(database)
	base := time.Date(2026, 8, 29, 18, 0, 0, 0, time.UTC)
	seedAffectState(t, repository, ctx, "affect-events-bounded", base)
	total := defaultListLimit + 5
	for index := 0; index < total; index++ {
		insertAffectEventRow(t, repository, ctx, domain.AffectiveEvent{
			ID: domain.ID(fmt.Sprintf("bounded-%04d", index)), AffectID: "affect-events-bounded",
			Emotion: domain.EmotionJoy, Intensity: 0.5, Valence: 1,
			DecayPolicy: domain.AffectiveDecayNone,
			CreatedAt:   base.Add(time.Duration(index) * time.Second),
		})
	}

	all, err := repository.ListEvents(ctx, "affect-events-bounded")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != defaultListLimit {
		t.Fatalf("default affect event window returned %d events, want %d", len(all), defaultListLimit)
	}
	// Newest first, so the default window is the tail of the journal.
	if all[0].ID != domain.ID(fmt.Sprintf("bounded-%04d", total-1)) {
		t.Fatalf("default window starts at %q, want the newest event", all[0].ID)
	}
	limited, err := repository.ListEvents(ctx, "affect-events-bounded", 5)
	if err != nil || len(limited) != 5 || limited[0].ID != all[0].ID {
		t.Fatalf("explicit limit = %d events, err = %v", len(limited), err)
	}
	// A caller that genuinely wants the whole journal says so with an explicit
	// limit; maxListLimit is the ceiling.
	full, err := repository.ListEvents(ctx, "affect-events-bounded", total)
	if err != nil || len(full) != total {
		t.Fatalf("explicit full limit = %d events, err = %v", len(full), err)
	}
	clamped, err := repository.ListEvents(ctx, "affect-events-bounded", maxListLimit+1)
	if err != nil || len(clamped) != total {
		t.Fatalf("clamped limit = %d events, err = %v", len(clamped), err)
	}
	if _, err := repository.ListEvents(ctx, "affect-events-bounded", -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative affect event limit error = %v", err)
	}
}
