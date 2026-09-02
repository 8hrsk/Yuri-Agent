package desktop

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// seedBridgeTranscript appends count messages to the conversation, oldest
// first, and returns their ids in that order.
func seedBridgeTranscript(t *testing.T, bridge *Bridge, conversationID string, count int) []string {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("msg-%04d", index)
		if err := bridge.repositories.Messages.Create(ctx, storage.Message{
			ID: domain.ID(id), ConversationID: domain.ID(conversationID), Role: "user",
			Content: fmt.Sprintf("Реплика %d", index), Status: "complete",
			// Pairs share a timestamp so the page cursor has to break the tie
			// on the id, exactly as it must in a real transcript.
			CreatedAt: base.Add(time.Duration(index/2) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func newConversationBridge(t *testing.T) (*Bridge, string) {
	t.Helper()
	bridge := newAgentTestBridge(t)
	if _, err := bridge.CreateAgent(CreateAgentInput{Name: "Юри", Gender: "female"}); err != nil {
		t.Fatal(err)
	}
	conversation, err := bridge.NewConversation("Длинный диалог")
	if err != nil {
		t.Fatal(err)
	}
	return bridge, conversation.ID
}

// TestConversationPageOptionsAreBoundedServerSide is the M-13 property applied
// to the conversation list: the renderer is untrusted, so the bounds are
// decided here. An over-large page is clamped and still answered; a negative
// one is rejected outright rather than silently normalized, so a caller bug
// surfaces instead of turning into a full-table read.
func TestConversationPageOptionsAreBoundedServerSide(t *testing.T) {
	for _, negative := range []ConversationPageOptions{{Limit: -1}, {Offset: -1}, {MessageLimit: -1}} {
		if _, err := negative.normalized(); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("normalized(%#v) error = %v, want ErrInvalidArgument", negative, err)
		}
	}
	empty, err := ConversationPageOptions{}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	// A zero MessageLimit stays zero: it means "no transcripts", the shape the
	// sidebar asks for. It is not defaulted up to a tail, which is what used to
	// drag every conversation's messages into a list that renders none of them.
	if empty.Limit != defaultConversationPageLimit || empty.MessageLimit != 0 || empty.Offset != 0 {
		t.Fatalf("zero options normalized to %#v", empty)
	}
	huge, err := ConversationPageOptions{Limit: 1 << 20, MessageLimit: 1 << 20, Offset: 3}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if huge.Limit != maxConversationPageLimit || huge.MessageLimit != maxConversationMessageLimit || huge.Offset != 3 {
		t.Fatalf("over-large options normalized to %#v", huge)
	}
}

// TestOverLargeRendererLimitsAreClampedEndToEnd is the same property as
// observed through the bridge rather than through its options helper: a
// renderer asking for a million messages is answered with the clamp, and a
// negative limit is refused at every entry point.
func TestOverLargeRendererLimitsAreClampedEndToEnd(t *testing.T) {
	bridge, conversationID := newConversationBridge(t)
	seedBridgeTranscript(t, bridge, conversationID, maxConversationMessageLimit+40)

	clamped, err := bridge.ListConversationsPage(ConversationPageOptions{MessageLimit: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(clamped) != 1 || len(clamped[0].Messages) != maxConversationMessageLimit {
		t.Fatalf("clamped page carried %d messages, want %d", len(clamped[0].Messages), maxConversationMessageLimit)
	}
	if !clamped[0].HasMoreMessages {
		t.Fatal("a clamped page that stops short of the transcript start must report more history")
	}
	over, err := bridge.ListMessages(conversationID, 1<<20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(over.Messages) != maxConversationMessageLimit {
		t.Fatalf("clamped ListMessages returned %d messages, want %d", len(over.Messages), maxConversationMessageLimit)
	}
	if !over.HasMore {
		t.Fatal("a clamped ListMessages page that stops short of the transcript start must report more history")
	}
	if _, err := bridge.ListConversationsPage(ConversationPageOptions{Limit: -1}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative page limit error = %v", err)
	}
	if _, err := bridge.ListMessages(conversationID, -1, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative message limit error = %v", err)
	}
	if _, err := bridge.ListMessages("", 10, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("missing conversation id error = %v", err)
	}
}

// TestConversationPageReturnsNewestMessagesAndPagesBack is the off-by-one
// property at the bridge: the list must carry the newest slice of a transcript
// in reading order, and "show earlier" must walk backwards from it without
// skipping or repeating a message at a page boundary.
func TestConversationPageReturnsNewestMessagesAndPagesBack(t *testing.T) {
	bridge, conversationID := newConversationBridge(t)
	const total, page = 90, 20
	seeded := seedBridgeTranscript(t, bridge, conversationID, total)

	listed, err := bridge.ListConversationsPage(ConversationPageOptions{MessageLimit: page})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d conversations, want 1", len(listed))
	}
	view := listed[0]
	if len(view.Messages) != page {
		t.Fatalf("conversation carried %d messages, want %d", len(view.Messages), page)
	}
	// Newest window, oldest-first inside it.
	for offset, message := range view.Messages {
		if want := seeded[total-page+offset]; message.ID != want {
			t.Fatalf("message %d = %s, want %s", offset, message.ID, want)
		}
	}
	if !view.HasMoreMessages {
		t.Fatal("a windowed transcript must report that history continues before it")
	}
	// The preview is still the newest message, not the oldest one in the window.
	if view.Preview != fmt.Sprintf("Реплика %d", total-1) {
		t.Fatalf("preview = %q", view.Preview)
	}

	// Walk the whole transcript backwards through the bridge.
	seen := make([]string, 0, total)
	for _, message := range view.Messages {
		seen = append(seen, message.ID)
	}
	cursor := view.Messages[0].ID
	for step := 0; step < total; step++ {
		older, pageErr := bridge.ListMessages(conversationID, page, cursor)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		if len(older.Messages) == 0 {
			if older.HasMore {
				t.Fatal("an empty page must not claim more history")
			}
			break
		}
		ids := make([]string, 0, len(older.Messages))
		for _, message := range older.Messages {
			ids = append(ids, message.ID)
		}
		seen = append(append([]string{}, ids...), seen...)
		cursor = ids[0]
	}
	if len(seen) != total {
		t.Fatalf("paging covered %d messages, want %d", len(seen), total)
	}
	unique := make(map[string]struct{}, len(seen))
	for index, id := range seen {
		if _, duplicate := unique[id]; duplicate {
			t.Fatalf("message %s was returned twice", id)
		}
		unique[id] = struct{}{}
		if id != seeded[index] {
			t.Fatalf("message %d = %s, want %s", index, id, seeded[index])
		}
	}

	// A conversation shorter than the window reports no further history, so the
	// renderer retires its "show earlier" control instead of fetching forever.
	whole, err := bridge.ListConversationsPage(ConversationPageOptions{MessageLimit: total + 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(whole[0].Messages) != total || whole[0].HasMoreMessages {
		t.Fatalf("full transcript = %d messages, hasMore = %v", len(whole[0].Messages), whole[0].HasMoreMessages)
	}
}

// TestListMessagesRefusesAnotherAgentsConversation keeps the bridge's agent
// boundary on the new read: naming a conversation id directly must not be a way
// around the ownership filter the conversation list applies.
func TestListMessagesRefusesAnotherAgentsConversation(t *testing.T) {
	bridge, conversationID := newConversationBridge(t)
	seedBridgeTranscript(t, bridge, conversationID, 4)
	if _, err := bridge.ListMessages(conversationID, 10, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CreateAgent(CreateAgentInput{Name: "Мира", Gender: "female"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.ListMessages(conversationID, 10, ""); err == nil {
		t.Fatal("another agent read a foreign transcript through ListMessages")
	}
}

func TestDeleteConversationRemovesTranscript(t *testing.T) {
	bridge, conversationID := newConversationBridge(t)
	seeded := seedBridgeTranscript(t, bridge, conversationID, 2)
	if err := bridge.DeleteConversation(DeleteConversationInput{ConversationID: conversationID}); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.repositories.Conversations.Get(context.Background(), domain.ID(conversationID)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("conversation survived delete: %v", err)
	}
	if _, err := bridge.repositories.Messages.Get(context.Background(), domain.ID(seeded[0])); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("message survived delete: %v", err)
	}
}
