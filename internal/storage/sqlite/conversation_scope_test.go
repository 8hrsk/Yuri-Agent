package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestConversationAndArchiveAreScopedByAgent(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, conversation := range []Conversation{
		{ID: "conversation-a", AgentID: "agent-a", Title: "A", CreatedAt: now, UpdatedAt: now},
		{ID: "conversation-b", AgentID: "agent-b", Title: "B", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)},
	} {
		if err := repositories.Conversations.Create(ctx, conversation); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Messages.Create(ctx, Message{
			ID:             domain.ID("message-" + strings.TrimPrefix(string(conversation.ID), "conversation-")),
			ConversationID: conversation.ID, Role: "user", Content: "общий секретный маркер",
			Status: "complete", CreatedAt: conversation.CreatedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, test := range []struct {
		agent domain.ID
		want  domain.ID
	}{{"agent-a", "conversation-a"}, {"agent-b", "conversation-b"}} {
		conversations, listErr := repositories.Conversations.ListByAgent(ctx, test.agent)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(conversations) != 1 || conversations[0].ID != test.want || conversations[0].AgentID != test.agent {
			t.Fatalf("ListByAgent(%q) = %#v", test.agent, conversations)
		}
		hits, searchErr := repositories.Archive.Search(ctx, "секретный маркер", ArchiveSearchOptions{AgentID: test.agent})
		if searchErr != nil {
			t.Fatal(searchErr)
		}
		if len(hits) != 1 || hits[0].ConversationID != test.want {
			t.Fatalf("Archive.Search(%q) = %#v", test.agent, hits)
		}
	}
}

func TestConversationRequiresAgentAtRepositoryAndDatabaseBoundaries(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewConversationRepository(database)
	now := time.Now().UTC()
	if err := repository.Create(ctx, Conversation{ID: "missing-agent", Title: "invalid", CreatedAt: now}); err == nil {
		t.Fatal("Create accepted conversation without agent id")
	}
	if _, err := database.ExecContext(context.Background(), `
		INSERT INTO conversations(id, title, created_at, updated_at)
		VALUES ('raw-missing-agent', 'invalid', ?, ?)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err == nil {
		t.Fatal("database trigger accepted conversation without agent id")
	}
}
