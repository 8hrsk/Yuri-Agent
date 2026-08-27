package context

import (
	stdcontext "context"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type fakeSource struct {
	core []MemoryItem
	hits []ArchiveHit
}

func (source fakeSource) Core(stdcontext.Context, int) ([]MemoryItem, error) { return source.core, nil }
func (source fakeSource) Recall(stdcontext.Context, string, int) ([]MemoryItem, error) {
	return nil, nil
}
func (source fakeSource) SearchArchive(stdcontext.Context, ArchiveQuery) ([]ArchiveHit, error) {
	return source.hits, nil
}

func TestAssemblerUsesFixedLayersAndExcludesCurrentConversationHit(t *testing.T) {
	current := domain.ID("conversation-current")
	assembler, err := New(fakeSource{
		core: []MemoryItem{{ID: "memory-1", Kind: "user", Content: "Любит зелёный чай", Provenance: "message-1"}},
		hits: []ArchiveHit{
			{ConversationID: current, MessageID: "message-current", Excerpt: "duplicate"},
			{ConversationID: "conversation-old", MessageID: "message-old", Conversation: "Про чай", Excerpt: "Выбрал сенчу"},
		},
	}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := assembler.Assemble(stdcontext.Background(), Input{
		ConversationID: current, Query: "чай", ImmutablePolicy: "POLICY", IdentitySeed: "IDENTITY",
		Transcript: []agent.Message{{Role: agent.RoleUser, Content: "Что я люблю?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 5 {
		t.Fatalf("messages = %#v", snapshot.Messages)
	}
	joined := ""
	for _, message := range snapshot.Messages {
		joined += message.Content + "\n"
	}
	for _, required := range []string{"POLICY", "IDENTITY", "untrusted evidence", "Любит зелёный чай", "conversation-old", "Что я люблю?"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("snapshot missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "duplicate") || len(snapshot.ArchiveMessageIDs) != 1 {
		t.Fatalf("current conversation hit was not excluded: %#v", snapshot)
	}
}

func TestAssemblerBoundsCoreRetrievedAndTranscript(t *testing.T) {
	config := DefaultConfig()
	config.CoreCharacters = 80
	config.RetrievedCharacters = 100
	config.RecentCharacters = 24
	assembler, err := New(fakeSource{
		core: []MemoryItem{{ID: "memory-1", Kind: "semantic", Content: strings.Repeat("я", 200)}},
		hits: []ArchiveHit{{ConversationID: "old", MessageID: "old-message", Excerpt: strings.Repeat("б", 200)}},
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := assembler.Assemble(stdcontext.Background(), Input{
		ConversationID: "current", Query: "q", ImmutablePolicy: "p", IdentitySeed: "i",
		Transcript: []agent.Message{
			{Role: agent.RoleUser, Content: strings.Repeat("с", 30)},
			{Role: agent.RoleAssistant, Content: strings.Repeat("д", 30)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CoreCharacters > config.CoreCharacters || snapshot.RetrievedCharacters > config.RetrievedCharacters {
		t.Fatalf("snapshot exceeded data budgets: %#v", snapshot)
	}
	last := snapshot.Messages[len(snapshot.Messages)-1]
	if got := len([]rune(last.Content)); got > config.RecentCharacters {
		t.Fatalf("transcript length = %d", got)
	}
}
