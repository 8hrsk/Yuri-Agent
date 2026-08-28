package memory

import (
	"fmt"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func BenchmarkHybridRanker1000(b *testing.B) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	candidates := make([]RankCandidate, 1_000)
	for index := range candidates {
		candidates[index] = RankCandidate{
			Memory: domain.Memory{
				ID: domain.ID(fmt.Sprintf("memory-%04d", index)), Version: 1,
				Kind: domain.MemoryKindSemantic, Nature: domain.MemoryNatureFact,
				Content:    fmt.Sprintf("Предпочтение пользователя и проектный факт номер %d", index),
				Confidence: .85, Salience: float64(index%100) / 100,
				Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionDecay,
				Lifecycle: domain.MemoryLifecycleActive, CreatedAt: now.Add(-time.Duration(index) * time.Hour), UpdatedAt: now.Add(-time.Duration(index) * time.Hour),
			},
			LexicalScore: float64(index%10) / 10, VectorScore: float64(index%17) / 17,
			AffectiveRelevance: float64(index%7) / 7,
		}
	}
	ranker := HybridRanker{Weights: DefaultRankWeights()}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		results := ranker.Rank(candidates, now)
		if len(results) != len(candidates) {
			b.Fatalf("ranked %d candidates", len(results))
		}
	}
}

func BenchmarkLexicalScoreUnicode(b *testing.B) {
	content := "Пользователь предпочитает зелёный чай сенча и проверяемые изменения в проекте Yuri"
	query := "зелёный чай проект Yuri"
	b.ReportAllocs()
	for range b.N {
		if LexicalScore(content, query) == 0 {
			b.Fatal("unexpected empty lexical score")
		}
	}
}
