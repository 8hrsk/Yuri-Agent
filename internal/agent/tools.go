package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// ToolRegistry is a concurrency-safe registry snapshot. Registration is
// normally performed at startup; lookups during a run do not hold a lock
// while a tool executes.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

func (r *ToolRegistry) Register(tool Tool) error {
	if r == nil || tool == nil {
		return fmt.Errorf("%w: tool is required", ErrInvalidRequest)
	}
	descriptor := tool.Descriptor()
	if !descriptor.Valid() {
		return fmt.Errorf("%w: invalid tool descriptor %q", ErrInvalidRequest, descriptor.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[descriptor.Name]; exists {
		return fmt.Errorf("%w: duplicate tool %q", ErrInvalidRequest, descriptor.Name)
	}
	r.tools[descriptor.Name] = tool
	return nil
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) Descriptors() []ToolDescriptor {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ToolDescriptor, 0, len(r.tools))
	for _, tool := range r.tools {
		result = append(result, tool.Descriptor())
	}
	// Stable ordering is useful for reproducible prompts and tests. Tool names
	// are validated on registration, so compare through a tiny insertion sort
	// without adding another dependency.
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].Name < result[j-1].Name; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

type AllowAllAuthorizer struct{}

func (AllowAllAuthorizer) Authorize(_ context.Context, _ ToolAuthorizationRequest) (ToolAuthorizationResult, error) {
	return ToolAuthorizationResult{Decision: domain.PermissionAllow}, nil
}
