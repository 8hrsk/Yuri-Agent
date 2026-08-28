package context

import (
	stdcontext "context"
	"fmt"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type benchmarkSource struct {
	core   []MemoryItem
	recall []MemoryItem
	hits   []ArchiveHit
}

func (s benchmarkSource) Core(stdcontext.Context, int) ([]MemoryItem, error) { return s.core, nil }
func (s benchmarkSource) Recall(stdcontext.Context, string, int) ([]MemoryItem, error) {
	return s.recall, nil
}
func (s benchmarkSource) SearchArchive(stdcontext.Context, ArchiveQuery) ([]ArchiveHit, error) {
	return s.hits, nil
}

func BenchmarkAssemblerBoundedContext(b *testing.B) {
	source := benchmarkSource{}
	for index := range 64 {
		item := MemoryItem{ID: domain.ID(fmt.Sprintf("memory-%d", index)), Kind: "semantic", Content: fmt.Sprintf("Долговременный факт %d о владельце и проекте", index), Provenance: "benchmark"}
		if index < 24 {
			source.core = append(source.core, item)
		} else {
			source.recall = append(source.recall, item)
		}
		source.hits = append(source.hits, ArchiveHit{ConversationID: domain.ID(fmt.Sprintf("old-%d", index)), MessageID: domain.ID(fmt.Sprintf("message-%d", index)), Excerpt: item.Content, OccurredAt: time.Now().UTC().Format(time.RFC3339)})
	}
	assembler, err := New(source, DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	transcript := make([]agent.Message, 80)
	for index := range transcript {
		role := agent.RoleUser
		if index%2 == 1 {
			role = agent.RoleAssistant
		}
		transcript[index] = agent.Message{Role: role, Content: fmt.Sprintf("Сообщение диалога %d с ограниченным контекстом", index)}
	}
	input := Input{ConversationID: "current", Query: "проект", ImmutablePolicy: "immutable", IdentitySeed: "identity", MutablePersona: "warm and direct", Relationship: "subjective trust", Transcript: transcript}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		snapshot, err := assembler.Assemble(stdcontext.Background(), input)
		if err != nil || len(snapshot.Messages) == 0 {
			b.Fatalf("Assemble() messages=%d error=%v", len(snapshot.Messages), err)
		}
	}
}
