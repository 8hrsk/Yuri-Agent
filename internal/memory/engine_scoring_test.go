package memory

import (
	"math"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// TestAffectiveRelevanceMatchesInflectedRussian pins the defect that made
// affective relevance unreachable for realistic Russian queries.
//
// tokenize does no stemming, so matching tokens for equality against the
// truncated stems ("чувств", "отнош", "эмоц") could only ever fire if the
// owner typed the truncated stem itself. Every natural inflected form scored
// 0. The English entries masked it, because they are whole words.
func TestAffectiveRelevanceMatchesInflectedRussian(t *testing.T) {
	// Deliberately neither an emotion nature nor a relationship kind, so the
	// early return cannot satisfy the assertion on its own.
	memory := domain.Memory{
		Kind:    domain.MemoryKindSemantic,
		Nature:  domain.MemoryNatureFact,
		Valence: -0.75,
	}
	want := math.Abs(memory.Valence)

	affective := []string{
		"какие у меня чувства к этой работе",
		"расскажи про мои эмоции вчера",
		"как развиваются наши отношения",
		"какое у меня было настроение",
		"чувствую ли я усталость",
		"how do I feel about this",
		"what is my mood today",
		"describe our relationship",
		"any strong emotions lately",
	}
	for _, query := range affective {
		if got := affectiveRelevance(query, memory); got != want {
			t.Errorf("affectiveRelevance(%q) = %v, want %v", query, got, want)
		}
	}

	neutral := []string{
		"когда я последний раз коммитил",
		"what is the database schema",
		"напомни пароль от роутера",
	}
	for _, query := range neutral {
		if got := affectiveRelevance(query, memory); got != 0 {
			t.Errorf("affectiveRelevance(%q) = %v, want 0", query, got)
		}
	}
}

// TestAffectiveRelevanceHonoursNatureAndKind keeps the pre-existing early
// return intact: an emotional or relational memory is affective regardless of
// how the query was phrased.
func TestAffectiveRelevanceHonoursNatureAndKind(t *testing.T) {
	query := "когда я последний раз коммитил"
	for _, memory := range []domain.Memory{
		{Kind: domain.MemoryKindSemantic, Nature: domain.MemoryNatureEmotion, Valence: 0.5},
		{Kind: domain.MemoryKindRelationship, Nature: domain.MemoryNatureFact, Valence: -0.25},
	} {
		if got := affectiveRelevance(query, memory); got != math.Abs(memory.Valence) {
			t.Errorf("affectiveRelevance(%+v) = %v, want %v", memory, got, math.Abs(memory.Valence))
		}
	}
}
