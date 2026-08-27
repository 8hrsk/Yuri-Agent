package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// BruteForceIndex is the portable Stage 2 semantic index.  It is deliberately
// small and deterministic; production adapters may replace it with a SQLite
// vector extension or another local index without changing Engine.
type BruteForceIndex struct {
	mu   sync.RWMutex
	docs map[domain.ID]VectorDocument
}

func NewBruteForceIndex() *BruteForceIndex {
	return &BruteForceIndex{docs: make(map[domain.ID]VectorDocument)}
}

func (i *BruteForceIndex) Upsert(ctx context.Context, document VectorDocument) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if i == nil {
		return fmt.Errorf("%w: vector index is nil", domain.ErrInvalidArgument)
	}
	if document.ID.Empty() {
		return fmt.Errorf("%w: vector document id is required", domain.ErrInvalidArgument)
	}
	if err := validateVector(document.Vector); err != nil {
		return err
	}
	copyVector := append([]float64(nil), document.Vector...)
	i.mu.Lock()
	if i.docs == nil {
		i.docs = make(map[domain.ID]VectorDocument)
	}
	i.docs[document.ID] = VectorDocument{ID: document.ID, Vector: copyVector, Version: document.Version}
	i.mu.Unlock()
	return nil
}

func (i *BruteForceIndex) Delete(ctx context.Context, id domain.ID) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if i == nil {
		return fmt.Errorf("%w: vector index is nil", domain.ErrInvalidArgument)
	}
	if id.Empty() {
		return fmt.Errorf("%w: vector document id is required", domain.ErrInvalidArgument)
	}
	i.mu.Lock()
	delete(i.docs, id)
	i.mu.Unlock()
	return nil
}

func (i *BruteForceIndex) Search(ctx context.Context, query []float64, limit int) ([]VectorMatch, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if i == nil {
		return nil, fmt.Errorf("%w: vector index is nil", domain.ErrInvalidArgument)
	}
	if err := validateVector(query); err != nil {
		return nil, err
	}
	// Copy under the read lock, then score without holding it.  This keeps a
	// slow query from blocking a concurrent memory update.
	i.mu.RLock()
	if limit <= 0 {
		limit = len(i.docs)
	}
	documents := make([]VectorDocument, 0, len(i.docs))
	for _, doc := range i.docs {
		documents = append(documents, VectorDocument{ID: doc.ID, Vector: append([]float64(nil), doc.Vector...), Version: doc.Version})
	}
	i.mu.RUnlock()

	matches := make([]VectorMatch, 0, len(documents))
	for index, document := range documents {
		if index%64 == 0 {
			if err := contextErr(ctx); err != nil {
				return nil, err
			}
		}
		if len(document.Vector) != len(query) {
			continue
		}
		score := cosineSimilarity(query, document.Vector)
		if score <= 0 {
			continue
		}
		matches = append(matches, VectorMatch{ID: document.ID, Score: score})
	}
	sort.Slice(matches, func(a, b int) bool {
		if math.Abs(matches[a].Score-matches[b].Score) > 1e-12 {
			return matches[a].Score > matches[b].Score
		}
		return matches[a].ID.String() < matches[b].ID.String()
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func validateVector(vector []float64) error {
	if len(vector) == 0 {
		return fmt.Errorf("%w: vector must not be empty", domain.ErrInvalidArgument)
	}
	var norm float64
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%w: vector contains a non-finite value", domain.ErrInvalidArgument)
		}
		norm += value * value
	}
	if norm <= 0 {
		return fmt.Errorf("%w: vector must not be zero", domain.ErrInvalidArgument)
	}
	return nil
}

func cosineSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var dot, leftNorm, rightNorm float64
	for index := range left {
		dot += left[index] * right[index]
		leftNorm += left[index] * left[index]
		rightNorm += right[index] * right[index]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	score := dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}
