package app

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// InMemoryEventBus provides synchronous, process-local event delivery for
// foundation tests. It intentionally makes no durability or retry promise.
type InMemoryEventBus struct {
	mu          sync.RWMutex
	nextID      atomic.Uint64
	subscribers map[domain.EventType]map[uint64]domain.EventHandler
}

func NewInMemoryEventBus() *InMemoryEventBus {
	return &InMemoryEventBus{subscribers: make(map[domain.EventType]map[uint64]domain.EventHandler)}
}

func (b *InMemoryEventBus) Publish(ctx context.Context, event domain.Event) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if !event.Valid() {
		return fmt.Errorf("%w: invalid event", domain.ErrInvalidArgument)
	}
	b.mu.RLock()
	handlers := make([]domain.EventHandler, 0, len(b.subscribers[event.Type]))
	for _, handler := range b.subscribers[event.Type] {
		handlers = append(handlers, handler)
	}
	b.mu.RUnlock()
	for _, handler := range handlers {
		if err := contextError(ctx); err != nil {
			return err
		}
		if err := handler(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (b *InMemoryEventBus) Subscribe(ctx context.Context, eventType domain.EventType, handler domain.EventHandler) (domain.Subscription, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if eventType == "" || handler == nil {
		return nil, fmt.Errorf("%w: event type and handler are required", domain.ErrInvalidArgument)
	}
	id := b.nextID.Add(1)
	b.mu.Lock()
	if b.subscribers[eventType] == nil {
		b.subscribers[eventType] = make(map[uint64]domain.EventHandler)
	}
	b.subscribers[eventType][id] = handler
	b.mu.Unlock()
	subscription := &eventSubscription{bus: b, eventType: eventType, id: id}
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			_ = subscription.Close()
		}()
	}
	return subscription, nil
}

type eventSubscription struct {
	bus       *InMemoryEventBus
	eventType domain.EventType
	id        uint64
	once      sync.Once
}

func (s *eventSubscription) Close() error {
	s.once.Do(func() {
		s.bus.mu.Lock()
		defer s.bus.mu.Unlock()
		items := s.bus.subscribers[s.eventType]
		delete(items, s.id)
		if len(items) == 0 {
			delete(s.bus.subscribers, s.eventType)
		}
	})
	return nil
}
