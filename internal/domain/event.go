package domain

import (
	"context"
	"fmt"
	"time"
)

// EventType is a stable identifier for events crossing application, worker,
// and UI boundaries. Feature packages may add namespaced values later.
type EventType string

const (
	EventRunCreated               EventType = "run.created"
	EventRunStateChanged          EventType = "run.state_changed"
	EventRunCancellationRequested EventType = "run.cancellation_requested"
	EventApprovalRequested        EventType = "approval.requested"
	EventApprovalResolved         EventType = "approval.resolved"
)

// Event is an envelope; Payload is owned by the event producer and should be a
// small immutable value. Adapters may serialize it for transport.
type Event struct {
	ID          ID        `json:"id"`
	Type        EventType `json:"type"`
	AggregateID ID        `json:"aggregate_id"`
	RunID       ID        `json:"run_id,omitempty"`
	Actor       Actor     `json:"actor"`
	OccurredAt  time.Time `json:"occurred_at"`
	Sequence    uint64    `json:"sequence"`
	Payload     any       `json:"payload,omitempty"`
}

func (e Event) Valid() bool {
	return !e.ID.Empty() && e.Type != "" && !e.AggregateID.Empty() && e.Actor.Valid() && !e.OccurredAt.IsZero()
}

func NewEvent(id ID, eventType EventType, aggregateID, runID ID, actor Actor, at time.Time, payload any) (Event, error) {
	if id.Empty() || eventType == "" || aggregateID.Empty() || !actor.Valid() || at.IsZero() {
		return Event{}, fmt.Errorf("%w: incomplete event", ErrInvalidArgument)
	}
	return Event{
		ID: id, Type: eventType, AggregateID: aggregateID, RunID: runID,
		Actor: actor, OccurredAt: at.UTC(), Payload: payload,
	}, nil
}

type EventHandler func(context.Context, Event) error

// Subscription is cancelable by the subscriber and safe to call more than
// once. Implementations should make cancellation idempotent.
type Subscription interface {
	Close() error
}

// EventBus is the only event dependency application services need. Durable
// delivery and UI transport are adapter concerns for later milestones.
type EventBus interface {
	Publish(context.Context, Event) error
	Subscribe(context.Context, EventType, EventHandler) (Subscription, error)
}

// EventPublisher is useful for services that only publish and should not
// accidentally depend on subscription internals.
type EventPublisher interface {
	Publish(context.Context, Event) error
}
