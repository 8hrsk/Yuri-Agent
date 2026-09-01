package desktop

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
)

// chatExecution owns every route-bound dependency used by one chat attempt.
// Rebuilding the complete bundle on fallback is important: delegation and
// memory extraction must use the same provider/model recorded on the run.
type chatExecution struct {
	backend agent.ModelBackend
	model   string
	tools   *agent.ToolRegistry
	runtime *agent.Runtime
	memory  *memory.Engine
}

// inferenceRouteSetupError marks only failures that happened while resolving
// a provider-bound backend (for example an unavailable credential or Codex
// process). Registry, policy, persistence, and memory setup errors must not be
// disguised as provider fallback opportunities.
type inferenceRouteSetupError struct{ cause error }

func (err inferenceRouteSetupError) Error() string { return err.cause.Error() }
func (err inferenceRouteSetupError) Unwrap() error { return err.cause }

func (b *Bridge) newChatExecution(
	ctx context.Context,
	route domain.RunInferenceRoute,
	runKind domain.RunKind,
	agentID, runID, conversationID domain.ID,
) (chatExecution, error) {
	backend, model, err := b.chatBackendForRoute(ctx, route.ProviderID, route.Model)
	if err != nil {
		return chatExecution{}, inferenceRouteSetupError{cause: err}
	}
	registry, err := b.chatTools(time.Now().UTC())
	if err != nil {
		return chatExecution{}, err
	}
	if runKind != domain.RunKindSubagent {
		if err := registry.Register(delegationAgentTool{
			bridge: b, backend: backend, model: model,
			principalAgentID: agentID, parentRunID: runID, conversationID: conversationID, parentTools: registry,
		}); err != nil {
			return chatExecution{}, err
		}
		if err := registry.Register(peerDialogueAgentTool{
			bridge: b, initiatorAgentID: agentID, triggerRunID: runID,
		}); err != nil {
			return chatExecution{}, err
		}
	}
	runtime, err := agent.NewRuntime(backend, registry)
	if err != nil {
		return chatExecution{}, err
	}
	if runKind == domain.RunKindBackground {
		runtime.Authorizer = backgroundToolAuthorizer{bridge: b}
	} else {
		runtime.Authorizer = desktopToolAuthorizer{bridge: b}
	}
	runtime.Approvals = desktopApprovalHandler{bridge: b}
	memoryEngine, err := b.newMemoryEngine(backend, model, agentID)
	if err != nil {
		return chatExecution{}, err
	}
	return chatExecution{backend: backend, model: model, tools: registry, runtime: runtime, memory: memoryEngine}, nil
}

// preOutputAttemptSink hides only the primary attempt's bookkeeping events.
// The first user-visible model/tool event commits the attempt and permanently
// disables fallback. A provider failure before that boundary is retained for
// classification but never shown as a terminal error if fallback succeeds.
type preOutputAttemptSink struct {
	mu        sync.Mutex
	target    agent.EventSink
	started   *agent.Event
	committed bool
}

func newPreOutputAttemptSink(target agent.EventSink) *preOutputAttemptSink {
	return &preOutputAttemptSink{target: target}
}

func (sink *preOutputAttemptSink) Sink(ctx context.Context, event agent.Event) error {
	if sink == nil {
		return fmt.Errorf("%w: fallback attempt sink is required", agent.ErrInvalidRequest)
	}
	sink.mu.Lock()
	switch event.Type {
	case agent.EventRunStarted:
		copyEvent := event
		sink.started = &copyEvent
		sink.mu.Unlock()
		return nil
	case agent.EventRunFailed:
		// failChatRun owns the durable and renderer terminal event if this
		// attempt cannot recover. Suppressing this provisional event is what
		// prevents a successful fallback from first flashing an error.
		sink.mu.Unlock()
		return nil
	case agent.EventRunCompleted:
		started := sink.takeStartedLocked()
		target := sink.target
		sink.mu.Unlock()
		if started != nil && target != nil {
			if err := target(ctx, *started); err != nil {
				return err
			}
		}
		if target != nil {
			return target(ctx, event)
		}
		return nil
	default:
		started := sink.takeStartedLocked()
		sink.committed = true
		target := sink.target
		sink.mu.Unlock()
		if started != nil && target != nil {
			if err := target(ctx, *started); err != nil {
				return err
			}
		}
		if target != nil {
			return target(ctx, event)
		}
		return nil
	}
}

func (sink *preOutputAttemptSink) takeStartedLocked() *agent.Event {
	started := sink.started
	sink.started = nil
	return started
}

func (sink *preOutputAttemptSink) FlushStarted(ctx context.Context) error {
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	started := sink.takeStartedLocked()
	target := sink.target
	sink.mu.Unlock()
	if started != nil && target != nil {
		return target(ctx, *started)
	}
	return nil
}

func (sink *preOutputAttemptSink) Committed() bool {
	if sink == nil {
		return false
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.committed
}

func skipRunStartedSink(target agent.EventSink) agent.EventSink {
	return func(ctx context.Context, event agent.Event) error {
		if event.Type == agent.EventRunStarted {
			return nil
		}
		if target == nil {
			return nil
		}
		return target(ctx, event)
	}
}

func inferenceFallbackEligible(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, agent.ErrBudgetExceeded) || errors.Is(err, agent.ErrModelCapabilityUnsupported) ||
		errors.Is(err, domain.ErrInvalidArgument) {
		return false
	}
	failure, classified := agent.InferenceFailureFromError(err)
	if !classified {
		var setupError inferenceRouteSetupError
		// Route-bound setup failures have not yet crossed the model boundary
		// and are safe to recover through an explicitly configured fallback.
		return errors.As(err, &setupError)
	}
	switch failure.Kind {
	case domain.RunFailureContextLimit, domain.RunFailureInvalidRequest, domain.RunFailureBudgetExceeded:
		return false
	case domain.RunFailureAuthentication, domain.RunFailureRateLimit, domain.RunFailureQuotaExhausted,
		domain.RunFailureModelUnavailable, domain.RunFailureTimeout, domain.RunFailureTransient, domain.RunFailureUnknown:
		return true
	default:
		return false
	}
}

func (b *Bridge) switchChatRunToFallback(
	ctx context.Context,
	run *domain.AgentRun,
	emitter *chatEmitter,
	agentID domain.ID,
	fallback domain.RunInferenceRoute,
	cause error,
) error {
	if run == nil || emitter == nil {
		return fmt.Errorf("%w: run and emitter are required for fallback", domain.ErrInvalidArgument)
	}
	from := run.Inference
	candidate := *run
	if err := candidate.SwitchInferenceRoute(fallback, time.Now().UTC()); err != nil {
		return err
	}
	if err := b.repositories.Runs.Save(ctx, candidate); err != nil {
		return err
	}
	reason, _ := inferenceFailure(cause)
	if err := b.appendInferenceFallbackAudit(ctx, candidate.ID, agentID, from, fallback, reason); err != nil {
		return err
	}
	*run = candidate
	emitter.setInference(fallback)
	emitter.emit(ChatEvent{
		Type: "run.fallback", ConversationID: emitter.conversationID, RunID: emitter.runID,
		ProviderID: fallback.ProviderID, Model: fallback.Model,
		FromProviderID: from.ProviderID, FromModel: from.Model,
		ToProviderID: fallback.ProviderID, ToModel: fallback.Model,
		Reason: reason,
	})
	return nil
}
