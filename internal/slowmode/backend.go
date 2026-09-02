package slowmode

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// MetadataPriorityKey is the optional ModelRequest metadata key understood by
// DefaultPriority. Missing and unknown values deliberately map to foreground.
const MetadataPriorityKey = "slow_mode_priority"

const (
	MetadataPriorityForeground  = "foreground"
	MetadataPriorityOwner       = "owner"
	MetadataPriorityBackground  = "background"
	MetadataPriorityMaintenance = "maintenance"
)

// DefaultPriority maps locally-authored request metadata to queue priority.
// The safe default is foreground, so an unannotated interactive request is
// never denied capacity reserved for owner interaction.
func DefaultPriority(request agent.ModelRequest) PriorityClass {
	switch strings.ToLower(strings.TrimSpace(request.Metadata[MetadataPriorityKey])) {
	case MetadataPriorityOwner:
		return PriorityOwnerInitiated
	case MetadataPriorityBackground:
		return PriorityBackground
	case MetadataPriorityMaintenance:
		return PriorityMaintenance
	default:
		return PriorityForeground
	}
}

// EstimateInputTokens counts the final provider-neutral request before it is
// passed to the wrapped backend. Integrations can use a local estimate or a
// cached exact provider count.
type EstimateInputTokens func(context.Context, agent.ModelRequest) (int64, error)

// ClassifyPriority can override DefaultPriority for integration-specific
// workload context.
type ClassifyPriority func(agent.ModelRequest) PriorityClass

// ErrorFeedback classifies a sanitized backend error as quota feedback.
type ErrorFeedback func(error) (Feedback, bool)

// Backend wraps an agent.ModelBackend with quota admission. It preserves the
// optional InteractiveToolStream capability and always releases its active
// concurrency slot when the returned stream is closed.
type Backend struct {
	Backend     agent.ModelBackend
	Coordinator *Coordinator
	Estimate    EstimateInputTokens
	Classify    ClassifyPriority
	Feedback    ErrorFeedback
}

func (backend Backend) Start(ctx context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	if backend.Backend == nil || backend.Coordinator == nil || backend.Estimate == nil {
		return nil, fmt.Errorf("%w: slow-mode backend is incomplete", agent.ErrInvalidRequest)
	}
	tokens, err := backend.Estimate(ctx, request)
	if err != nil {
		if backend.Feedback != nil {
			if feedback, ok := backend.Feedback(err); ok {
				if _, feedbackErr := backend.Coordinator.ApplyFeedback(ctx, feedback); feedbackErr != nil {
					return nil, fmt.Errorf("estimate input tokens: %v; apply slow-mode feedback: %w", err, feedbackErr)
				}
			}
		}
		return nil, err
	}
	priority := DefaultPriority(request)
	if backend.Classify != nil {
		priority = backend.Classify(request)
	}
	lease, err := backend.Coordinator.Admit(ctx, Request{InputTokens: tokens, Priority: priority})
	if err != nil {
		return nil, err
	}
	stream, err := backend.Backend.Start(ctx, request)
	if err != nil {
		finishErr := lease.Finish(ctx, Outcome{Accounting: AccountingUnknown})
		var feedbackErr error
		if backend.Feedback != nil {
			if feedback, ok := backend.Feedback(err); ok {
				_, feedbackErr = backend.Coordinator.ApplyFeedback(ctx, feedback)
			}
		}
		if finishErr != nil {
			return nil, fmt.Errorf("start backend: %v; release slow-mode lease: %w", err, finishErr)
		}
		if feedbackErr != nil {
			return nil, fmt.Errorf("start backend: %v; apply slow-mode feedback: %w", err, feedbackErr)
		}
		return nil, err
	}
	if stream == nil {
		_ = lease.Finish(ctx, Outcome{Accounting: AccountingUnknown})
		return nil, fmt.Errorf("%w: backend returned nil stream", agent.ErrBackend)
	}
	return newQuotaStream(stream, lease, backend.Coordinator, backend.Feedback), nil
}

func newQuotaStream(stream agent.ModelStream, lease *Lease, coordinator *Coordinator, feedback ErrorFeedback) agent.ModelStream {
	wrapped := &quotaStream{ModelStream: stream, lease: lease, coordinator: coordinator, feedback: feedback}
	if interactive, ok := stream.(agent.InteractiveToolStream); ok {
		return &quotaInteractiveStream{quotaStream: wrapped, interactive: interactive}
	}
	return wrapped
}

type quotaStream struct {
	agent.ModelStream
	lease       *Lease
	coordinator *Coordinator
	feedback    ErrorFeedback

	mu           sync.Mutex
	actualInput  int64
	hasActual    bool
	completed    bool
	closeOnce    sync.Once
	closeErr     error
	feedbackOnce sync.Once
}

func (stream *quotaStream) Recv(ctx context.Context) (agent.ModelEvent, error) {
	event, err := stream.ModelStream.Recv(ctx)
	if err != nil && stream.feedback != nil {
		stream.feedbackOnce.Do(func() {
			if feedback, ok := stream.feedback(err); ok {
				_, _ = stream.coordinator.ApplyFeedback(ctx, feedback)
			}
		})
	}
	if err == nil {
		stream.mu.Lock()
		if event.Usage.InputTokens > 0 {
			stream.actualInput = event.Usage.InputTokens
			stream.hasActual = true
		}
		if event.Type == agent.ModelEventCompleted {
			stream.completed = true
		}
		stream.mu.Unlock()
	}
	return event, err
}

func (stream *quotaStream) Close() error {
	stream.closeOnce.Do(func() {
		transportErr := stream.ModelStream.Close()
		stream.mu.Lock()
		outcome := Outcome{Accounting: AccountingUnknown, ActualInputTokens: stream.actualInput, HasActualInputTokens: stream.hasActual}
		completed := stream.completed
		if completed {
			outcome.Accounting = AccountingCounted
		}
		stream.mu.Unlock()
		leaseErr := stream.lease.Finish(context.Background(), outcome)
		if completed {
			stream.coordinator.RecordSuccess()
		}
		if transportErr != nil {
			stream.closeErr = transportErr
		} else {
			stream.closeErr = leaseErr
		}
	})
	return stream.closeErr
}

type quotaInteractiveStream struct {
	*quotaStream
	interactive agent.InteractiveToolStream
}

func (stream *quotaInteractiveStream) RespondToolResult(ctx context.Context, callID string, result agent.ToolResult) error {
	return stream.interactive.RespondToolResult(ctx, callID, result)
}

var _ agent.ModelBackend = Backend{}
var _ agent.ModelStream = (*quotaStream)(nil)
var _ agent.InteractiveToolStream = (*quotaInteractiveStream)(nil)
