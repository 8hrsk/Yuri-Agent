package app

import (
	"context"
	"sort"
	"sync"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// InMemoryRunRepository is a foundation adapter for tests and local wiring.
// It is not a durable store and must not be used as the production source of
// truth once the SQLite adapter is available.
type InMemoryRunRepository struct {
	mu   sync.RWMutex
	data map[domain.ID]domain.AgentRun
}

func NewInMemoryRunRepository() *InMemoryRunRepository {
	return &InMemoryRunRepository{data: make(map[domain.ID]domain.AgentRun)}
}

func (r *InMemoryRunRepository) Create(ctx context.Context, run domain.AgentRun) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[run.ID]; ok {
		return domain.ErrConflict
	}
	r.data[run.ID] = run
	return nil
}

func (r *InMemoryRunRepository) Get(ctx context.Context, id domain.ID) (domain.AgentRun, error) {
	if err := contextError(ctx); err != nil {
		return domain.AgentRun{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	run, ok := r.data[id]
	if !ok {
		return domain.AgentRun{}, domain.ErrNotFound
	}
	return run, nil
}

func (r *InMemoryRunRepository) Save(ctx context.Context, run domain.AgentRun) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.data[run.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if run.Version != current.Version+1 {
		return domain.ErrConflict
	}
	r.data[run.ID] = run
	return nil
}

type InMemoryApprovalRepository struct {
	mu   sync.RWMutex
	data map[domain.ID]domain.Approval
}

func NewInMemoryApprovalRepository() *InMemoryApprovalRepository {
	return &InMemoryApprovalRepository{data: make(map[domain.ID]domain.Approval)}
}

func (r *InMemoryApprovalRepository) Create(ctx context.Context, approval domain.Approval) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[approval.ID]; ok {
		return domain.ErrConflict
	}
	r.data[approval.ID] = cloneApproval(approval)
	return nil
}

func (r *InMemoryApprovalRepository) Get(ctx context.Context, id domain.ID) (domain.Approval, error) {
	if err := contextError(ctx); err != nil {
		return domain.Approval{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	approval, ok := r.data[id]
	if !ok {
		return domain.Approval{}, domain.ErrNotFound
	}
	return cloneApproval(approval), nil
}

func (r *InMemoryApprovalRepository) Save(ctx context.Context, approval domain.Approval) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.data[approval.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if approval.Version != current.Version+1 {
		return domain.ErrConflict
	}
	r.data[approval.ID] = cloneApproval(approval)
	return nil
}

func (r *InMemoryApprovalRepository) ListByRun(ctx context.Context, runID domain.ID) ([]domain.Approval, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]domain.Approval, 0)
	for _, approval := range r.data {
		if approval.RunID == runID {
			result = append(result, cloneApproval(approval))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func cloneApproval(value domain.Approval) domain.Approval {
	value.Scope.Values = append([]string(nil), value.Scope.Values...)
	return value
}
