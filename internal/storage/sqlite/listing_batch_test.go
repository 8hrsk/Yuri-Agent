package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// seedTranscripts creates conversations owned by one agent, each with the given
// number of messages. Messages are minted in pairs that share a timestamp so
// the (created_at, id) tiebreak is actually exercised rather than assumed.
func seedTranscripts(t *testing.T, repositories *Repositories, ctx context.Context, conversations, messages int) []domain.ID {
	t.Helper()
	base := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	profile, err := domain.NewAgentProfile("agent-batch", "Юри", 22, "female", "", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Agents.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	ids := make([]domain.ID, 0, conversations)
	for index := 0; index < conversations; index++ {
		id := domain.ID(fmt.Sprintf("conversation-%03d", index))
		if err := repositories.Conversations.Create(ctx, Conversation{
			ID: id, AgentID: "agent-batch", Title: fmt.Sprintf("Диалог %d", index),
			CreatedAt: base, UpdatedAt: base.Add(time.Duration(index) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		for message := 0; message < messages; message++ {
			// Two consecutive messages share a timestamp, so a page boundary
			// that fell between them would have to be resolved by id.
			at := base.Add(time.Duration(message/2) * time.Second)
			if err := repositories.Messages.Create(ctx, Message{
				ID: domain.ID(fmt.Sprintf("%s-msg-%04d", id, message)), ConversationID: id,
				Role: "user", Content: fmt.Sprintf("Реплика %d", message), Status: "complete", CreatedAt: at,
			}); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, id)
	}
	return ids
}

func seedRunWithCalls(t *testing.T, repositories *Repositories, ctx context.Context, conversationID domain.ID, index, calls int) domain.ID {
	t.Helper()
	base := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Minute)
	runID := domain.ID(fmt.Sprintf("%s-run-%02d", conversationID, index))
	if err := repositories.Runs.Create(ctx, domain.AgentRun{
		ID: runID, AgentID: "agent-batch", Kind: domain.RunKindInteractive, ConversationID: conversationID,
		State: domain.RunStateQueued, Budget: domain.RunBudget{MaxSteps: 8, MaxTokens: 4000, MaxToolCalls: 4, MaxToolOutputBytes: 4096, MaxDurationSeconds: 60}, Version: 1,
		CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	for call := 0; call < calls; call++ {
		if err := repositories.ToolCalls.Create(ctx, ToolCall{
			ID: domain.ID(fmt.Sprintf("%s-call-%02d", runID, call)), RunID: runID, ToolID: "filesystem.read",
			ArgsRedacted: "{}", Risk: domain.RiskLow, Status: ToolCallSucceeded,
			IdempotencyKey: fmt.Sprintf("%s-key-%02d", runID, call), Version: 1,
			CreatedAt: base.Add(time.Duration(call) * time.Second), UpdatedAt: base.Add(time.Duration(call) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return runID
}

// TestConversationPageReadsAreSetBased is the M-35/H-16 safety property for the
// desktop conversation list: expanding a whole page of conversations into
// messages, runs and tool calls must cost a constant number of round-trips, not
// one per conversation and one per run. The per-conversation loop it replaces
// is counted alongside it as the control.
func TestConversationPageReadsAreSetBased(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	const conversations, messages, runsEach, callsEach = 25, 12, 2, 2
	ids := seedTranscripts(t, repositories, ctx, conversations, messages)
	for _, id := range ids {
		for run := 0; run < runsEach; run++ {
			seedRunWithCalls(t, repositories, ctx, id, run, callsEach)
		}
	}

	var tails map[domain.ID][]Message
	queries := countQueries(func() {
		tails, err = repositories.Messages.ListTailByConversations(ctx, ids, 5)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("ListTailByConversations issued %d queries for %d conversations, want 1", queries, conversations)
	}
	if len(tails) != conversations {
		t.Fatalf("tails cover %d conversations, want %d", len(tails), conversations)
	}
	for _, id := range ids {
		tail := tails[id]
		if len(tail) != 5 {
			t.Fatalf("%s tail = %d messages, want 5", id, len(tail))
		}
		// The newest five, oldest-first — the order a transcript renders in.
		for offset, message := range tail {
			want := domain.ID(fmt.Sprintf("%s-msg-%04d", id, messages-5+offset))
			if message.ID != want {
				t.Fatalf("%s tail[%d] = %s, want %s", id, offset, message.ID, want)
			}
			if message.Content != fmt.Sprintf("Реплика %d", messages-5+offset) {
				t.Fatalf("%s tail[%d] content = %q", id, offset, message.Content)
			}
		}
	}

	var runs map[domain.ID][]domain.AgentRun
	queries = countQueries(func() {
		runs, err = repositories.Runs.ListRecentByConversations(ctx, ids, 10)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("ListRecentByConversations issued %d queries, want 1", queries)
	}
	runIDs := make([]domain.ID, 0, conversations*runsEach)
	for _, id := range ids {
		if len(runs[id]) != runsEach {
			t.Fatalf("%s has %d runs, want %d", id, len(runs[id]), runsEach)
		}
		for offset, run := range runs[id] {
			if want := domain.ID(fmt.Sprintf("%s-run-%02d", id, offset)); run.ID != want {
				t.Fatalf("%s run[%d] = %s, want %s", id, offset, run.ID, want)
			}
			runIDs = append(runIDs, run.ID)
		}
	}

	var calls map[domain.ID][]ToolCall
	queries = countQueries(func() {
		calls, err = repositories.ToolCalls.ListByRuns(ctx, runIDs)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("ListByRuns issued %d queries for %d runs, want 1", queries, len(runIDs))
	}
	for _, runID := range runIDs {
		if len(calls[runID]) != callsEach {
			t.Fatalf("%s has %d tool calls, want %d", runID, len(calls[runID]), callsEach)
		}
	}

	// The control: the shape the bridge used to have. Every one of these loops
	// is a round-trip on a pool that is deliberately a single connection.
	loop := countQueries(func() {
		for _, id := range ids {
			if _, loopErr := repositories.Messages.ListByConversation(ctx, id, 5); loopErr != nil {
				t.Fatal(loopErr)
			}
			if _, loopErr := repositories.Runs.ListByConversation(ctx, id); loopErr != nil {
				t.Fatal(loopErr)
			}
		}
		for _, runID := range runIDs {
			if _, loopErr := repositories.ToolCalls.ListByRun(ctx, runID); loopErr != nil {
				t.Fatal(loopErr)
			}
		}
	})
	if want := int64(conversations*2 + len(runIDs)); loop != want {
		t.Fatalf("per-conversation loop issued %d queries, want %d", loop, want)
	}
}

// TestMessageTailAndCursorPagesCoverTranscriptExactlyOnce is the off-by-one
// property: the tail must be the newest page, and walking backwards with
// ListBefore must reconstruct the transcript with no message skipped and none
// repeated — including across a boundary that falls between two messages
// sharing a timestamp.
func TestMessageTailAndCursorPagesCoverTranscriptExactlyOnce(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	const total, page = 25, 10
	ids := seedTranscripts(t, repositories, ctx, 1, total)
	conversationID := ids[0]

	tails, err := repositories.Messages.ListTailByConversations(ctx, ids, page)
	if err != nil {
		t.Fatal(err)
	}
	tail := tails[conversationID]
	if len(tail) != page {
		t.Fatalf("tail = %d messages, want %d", len(tail), page)
	}
	if tail[0].ID != messageID(conversationID, total-page) || tail[len(tail)-1].ID != messageID(conversationID, total-1) {
		t.Fatalf("tail spans %s..%s, want %s..%s", tail[0].ID, tail[len(tail)-1].ID,
			messageID(conversationID, total-page), messageID(conversationID, total-1))
	}

	// Walk backwards from the oldest message held, exactly as the transcript's
	// "show earlier" control does.
	seen := make([]domain.ID, 0, total)
	for _, message := range tail {
		seen = append(seen, message.ID)
	}
	cursor := tail[0].ID
	for step := 0; step < 10; step++ {
		older, pageErr := repositories.Messages.ListBefore(ctx, conversationID, cursor, page)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		if len(older) == 0 {
			break
		}
		for offset := 1; offset < len(older); offset++ {
			if older[offset-1].CreatedAt.After(older[offset].CreatedAt) {
				t.Fatalf("page is not chronological at %d: %v then %v", offset, older[offset-1].ID, older[offset].ID)
			}
		}
		seen = append(append([]domain.ID{}, idsOf(older)...), seen...)
		cursor = older[0].ID
	}
	if len(seen) != total {
		t.Fatalf("paging covered %d messages, want %d", len(seen), total)
	}
	for index, id := range seen {
		if want := messageID(conversationID, index); id != want {
			t.Fatalf("message %d = %s, want %s", index, id, want)
		}
	}

	// The boundary itself. Messages are minted in pairs sharing a created_at,
	// so 14 and 15 are the same second: a cursor that compared timestamps alone
	// would drop 14 here, and one that compared "<=" would hand 15 back twice.
	boundary, err := repositories.Messages.ListBefore(ctx, conversationID, messageID(conversationID, 15), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(boundary) != 3 || boundary[2].ID != messageID(conversationID, 14) || boundary[0].ID != messageID(conversationID, 12) {
		t.Fatalf("boundary page = %v, want messages 12..14", idsOf(boundary))
	}
	// Anchoring on the older half of that same pair must not drag its newer
	// partner back into the page.
	pair, err := repositories.Messages.ListBefore(ctx, conversationID, messageID(conversationID, 14), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(pair) != 2 || pair[1].ID != messageID(conversationID, 13) || pair[0].ID != messageID(conversationID, 12) {
		t.Fatalf("same-timestamp boundary page = %v, want messages 12..13", idsOf(pair))
	}

	// The start of the transcript ends paging rather than wrapping.
	head, err := repositories.Messages.ListBefore(ctx, conversationID, messageID(conversationID, 0), page)
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != 0 {
		t.Fatalf("page before the first message = %v, want none", idsOf(head))
	}
	// An empty cursor means "the newest page", which must agree with the tail.
	newest, err := repositories.Messages.ListBefore(ctx, conversationID, "", page)
	if err != nil {
		t.Fatal(err)
	}
	if len(newest) != page || newest[0].ID != tail[0].ID || newest[page-1].ID != tail[page-1].ID {
		t.Fatalf("empty cursor page = %v, want the tail", idsOf(newest))
	}
	// An anchor that is not in the store is reported, not answered with the
	// same empty page the transcript start produces a few lines above. That
	// ambiguity is the bug: both meant "you get nothing", so a caller paging
	// with a stale id could not tell which had happened.
	unknown, err := repositories.Messages.ListBefore(ctx, conversationID, "conversation-000-msg-9999", page)
	if !errors.Is(err, ErrCursorNotFound) {
		t.Fatalf("unknown cursor page = %v, err = %v, want ErrCursorNotFound", idsOf(unknown), err)
	}
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("unknown cursor error = %v, want it to read as an invalid argument too", err)
	}
}

// TestListBeforeAnchorsInsideTheConversation covers the other half of the
// cursor fix. beforeID arrives from the renderer, and the anchor lookup used to
// be a bare "WHERE id = ?" over the whole messages table: an id belonging to
// another conversation resolved anyway, and the page came back correctly scoped
// but cut at a foreign message's position in time. That is a wrong page
// returned as if it were right, which is worse than an error.
//
// The query cost is asserted here rather than timed: the pool is one
// connection, so a round-trip is the honest unit. A page that finds rows still
// costs the single statement it always did; only an empty page pays for the
// second, and that is a primary-key point lookup on the terminal page of a walk.
func TestListBeforeAnchorsInsideTheConversation(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	const perConversation = 10
	ids := seedTranscripts(t, repositories, ctx, 2, perConversation)
	first, second := ids[0], ids[1]

	// Both conversations are seeded over the same timestamps, so a foreign
	// anchor resolved to a real position in time and produced a page that
	// looked entirely plausible. That is precisely why the old shape could be
	// wrong without ever being noticed.
	foreign := messageID(second, 5)
	if _, err := repositories.Messages.ListBefore(ctx, first, foreign, perConversation); !errors.Is(err, ErrCursorNotFound) {
		t.Fatalf("cursor from another conversation error = %v, want ErrCursorNotFound", err)
	}
	// The same id is a perfectly good cursor in the conversation it belongs to,
	// so the scoping rejects the mismatch rather than the id.
	own, err := repositories.Messages.ListBefore(ctx, second, foreign, perConversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 5 || own[0].ID != messageID(second, 0) || own[4].ID != messageID(second, 4) {
		t.Fatalf("page before %s in its own conversation = %v", foreign, idsOf(own))
	}

	// A page that returns rows is one statement.
	queries := countQueries(func() {
		_, err = repositories.Messages.ListBefore(ctx, second, messageID(second, 5), 3)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("a page with rows issued %d queries, want 1", queries)
	}
	// The terminal page pays one more to say which kind of empty it is.
	queries = countQueries(func() {
		_, err = repositories.Messages.ListBefore(ctx, second, messageID(second, 0), 3)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 2 {
		t.Fatalf("the terminal page issued %d queries, want 2", queries)
	}
}

// TestConversationPreviewsReadInOneQuery is the read that lets a conversation
// list carry the sidebar's snippet without carrying its transcripts.
func TestConversationPreviewsReadInOneQuery(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	const conversations, messages = 20, 6
	ids := seedTranscripts(t, repositories, ctx, conversations, messages)

	var previews map[domain.ID]string
	queries := countQueries(func() {
		previews, err = repositories.Messages.ListPreviewsByConversations(ctx, ids)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("previews for %d conversations issued %d queries, want 1", conversations, queries)
	}
	if len(previews) != conversations {
		t.Fatalf("previews cover %d conversations, want %d", len(previews), conversations)
	}
	for _, id := range ids {
		// The newest message, which is what the sidebar shows — not the oldest,
		// and not an arbitrary one from the middle.
		if want := fmt.Sprintf("Реплика %d", messages-1); previews[id] != want {
			t.Fatalf("%s preview = %q, want %q", id, previews[id], want)
		}
	}

	// An empty message is not a preview: the sidebar would show a blank row for
	// a conversation whose newest entry happens to be a contentless tool
	// message. The last message with something to show wins.
	blank := ids[0]
	base := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if err := repositories.Messages.Create(ctx, Message{
		ID: domain.ID(string(blank) + "-blank"), ConversationID: blank, Role: "tool",
		Content: "", Status: "complete", CreatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	previews, err = repositories.Messages.ListPreviewsByConversations(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("Реплика %d", messages-1); previews[blank] != want {
		t.Fatalf("preview after an empty newest message = %q, want %q", previews[blank], want)
	}

	// Long content is cut in the query, so a list of 200 conversations cannot
	// ship 200 whole message bodies to draw 200 one-line snippets.
	long := ids[1]
	if err := repositories.Messages.Create(ctx, Message{
		ID: domain.ID(string(long) + "-long"), ConversationID: long, Role: "assistant",
		Content: strings.Repeat("я", 4000), Status: "complete", CreatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	previews, err = repositories.Messages.ListPreviewsByConversations(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if runes := []rune(previews[long]); len(runes) != conversationPreviewRunes {
		t.Fatalf("long preview = %d runes, want %d", len(runes), conversationPreviewRunes)
	}
}

// TestBatchReadsRejectNegativeLimits pins the repository half of the bound: a
// negative per-owner limit is an error, never a silent "unbounded".
func TestBatchReadsRejectNegativeLimits(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	ids := seedTranscripts(t, repositories, ctx, 1, 3)
	if _, err := repositories.Messages.ListTailByConversations(ctx, ids, -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative tail limit error = %v", err)
	}
	if _, err := repositories.Runs.ListRecentByConversations(ctx, ids, -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative run limit error = %v", err)
	}
	if _, err := repositories.Messages.ListBefore(ctx, ids[0], "", -1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative cursor limit error = %v", err)
	}
	if _, err := repositories.Messages.ListBefore(ctx, "", "", 5); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("missing conversation id error = %v", err)
	}
	// An over-large limit is clamped rather than refused, so an over-eager
	// caller still gets an answer.
	clamped, err := repositories.Messages.ListTailByConversations(ctx, ids, maxListLimit+1)
	if err != nil || len(clamped[ids[0]]) != 3 {
		t.Fatalf("clamped tail = %d messages, err = %v", len(clamped[ids[0]]), err)
	}
	// A repeated id must not multiply a conversation's rows.
	duplicated, err := repositories.Messages.ListTailByConversations(ctx, []domain.ID{ids[0], ids[0], ""}, 10)
	if err != nil || len(duplicated) != 1 || len(duplicated[ids[0]]) != 3 {
		t.Fatalf("duplicate ids produced %#v, err = %v", duplicated, err)
	}
}

func messageID(conversationID domain.ID, index int) domain.ID {
	return domain.ID(fmt.Sprintf("%s-msg-%04d", conversationID, index))
}

func idsOf(messages []Message) []domain.ID {
	result := make([]domain.ID, 0, len(messages))
	for _, message := range messages {
		result = append(result, message.ID)
	}
	return result
}
