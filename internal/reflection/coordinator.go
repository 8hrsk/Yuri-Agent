package reflection

import (
	"context"
	"fmt"
	"sync"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// Coordinator serializes reflection callbacks per local profile. It does not
// serialize different profiles, and it never holds its mutex while user/model
// code runs. Waiting callers can cancel their wait through context.
type Coordinator struct {
	mu       sync.Mutex
	profiles map[domain.ID]*profileGate
}

type profileGate struct {
	running bool
	waiters int
	done    chan struct{}
}

// NewCoordinator returns an empty one-run-per-profile coordinator.
func NewCoordinator() *Coordinator {
	return &Coordinator{profiles: make(map[domain.ID]*profileGate)}
}

// Do waits for the profile slot and invokes fn exactly once after acquiring
// it. The slot is released even when fn returns an error. A nil callback is a
// caller error and does not reserve a slot.
func (c *Coordinator) Do(ctx context.Context, profileID domain.ID, fn func(context.Context) (ReflectionResult, error)) (ReflectionResult, error) {
	if c == nil {
		return ReflectionResult{}, fmt.Errorf("%w: nil coordinator", ErrInvalidSnapshot)
	}
	if err := ContextError(ctx); err != nil {
		return ReflectionResult{}, err
	}
	if profileID.Empty() {
		return ReflectionResult{}, fmt.Errorf("%w: profile id is required", ErrInvalidSnapshot)
	}
	if fn == nil {
		return ReflectionResult{}, fmt.Errorf("%w: nil reflection callback", ErrInvalidSnapshot)
	}
	release, err := c.acquire(ctx, profileID, false)
	if err != nil {
		return ReflectionResult{}, err
	}
	defer release()
	return fn(ctx)
}

// Run is a naming alias for Do for callers that model the coordinator as a
// small background runner.
func (c *Coordinator) Run(ctx context.Context, profileID domain.ID, fn func(context.Context) (ReflectionResult, error)) (ReflectionResult, error) {
	return c.Do(ctx, profileID, fn)
}

// TryDo is the non-blocking variant of Do. It returns ErrProfileBusy if the
// profile already has a reflection in progress.
func (c *Coordinator) TryDo(ctx context.Context, profileID domain.ID, fn func(context.Context) (ReflectionResult, error)) (ReflectionResult, error) {
	if c == nil {
		return ReflectionResult{}, fmt.Errorf("%w: nil coordinator", ErrInvalidSnapshot)
	}
	if err := ContextError(ctx); err != nil {
		return ReflectionResult{}, err
	}
	if profileID.Empty() {
		return ReflectionResult{}, fmt.Errorf("%w: profile id is required", ErrInvalidSnapshot)
	}
	if fn == nil {
		return ReflectionResult{}, fmt.Errorf("%w: nil reflection callback", ErrInvalidSnapshot)
	}
	release, err := c.acquire(ctx, profileID, true)
	if err != nil {
		return ReflectionResult{}, err
	}
	defer release()
	return fn(ctx)
}

// TryRun is the non-blocking naming alias for TryDo.
func (c *Coordinator) TryRun(ctx context.Context, profileID domain.ID, fn func(context.Context) (ReflectionResult, error)) (ReflectionResult, error) {
	return c.TryDo(ctx, profileID, fn)
}

// Active reports whether a profile currently owns the reflection slot.
func (c *Coordinator) Active(profileID domain.ID) bool {
	if c == nil || profileID.Empty() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	gate := c.profiles[profileID]
	return gate != nil && gate.running
}

func (c *Coordinator) acquire(ctx context.Context, profileID domain.ID, try bool) (func(), error) {
	for {
		if err := ContextError(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		gate := c.profiles[profileID]
		if gate == nil {
			gate = &profileGate{done: make(chan struct{})}
			c.profiles[profileID] = gate
		}
		if !gate.running {
			// A previous owner closes done to wake waiters. Recreate it before
			// handing the same gate to the next owner; a gate may be reused while
			// queued callers keep its profile entry alive.
			gate.done = make(chan struct{})
			gate.running = true
			c.mu.Unlock()
			return c.releaseFunc(profileID, gate), nil
		}
		if try {
			c.mu.Unlock()
			return nil, ErrProfileBusy
		}
		gate.waiters++
		done := gate.done
		c.mu.Unlock()
		select {
		case <-done:
			c.mu.Lock()
			if current := c.profiles[profileID]; current == gate && gate.waiters > 0 {
				gate.waiters--
			}
			c.mu.Unlock()
			continue
		case <-ctx.Done():
			c.mu.Lock()
			if current := c.profiles[profileID]; current == gate && gate.waiters > 0 {
				gate.waiters--
			}
			c.mu.Unlock()
			return nil, ctx.Err()
		}
	}
}

func (c *Coordinator) releaseFunc(profileID domain.ID, gate *profileGate) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			current := c.profiles[profileID]
			if current != gate {
				return
			}
			gate.running = false
			close(gate.done)
			if gate.waiters == 0 {
				delete(c.profiles, profileID)
			}
		})
	}
}
