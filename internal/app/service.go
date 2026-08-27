// Package app contains application-service boundaries for the desktop core.
// Feature implementations are added behind these boundaries in later
// milestones; this package only coordinates durable run state, approvals, and
// typed events.
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// Dependencies are the ports needed by the foundation application service.
// Production wiring will supply SQLite-backed repositories and the durable
// event bus in later milestones. Tests can use the in-memory adapters in this
// package.
type Dependencies struct {
	Runs      domain.RunRepository
	Approvals domain.ApprovalRepository
	Events    domain.EventPublisher
	Clock     domain.Clock
	IDs       domain.IDGenerator
}

// Service is intentionally small. It does not run models, invoke tools, or
// access the filesystem; those concerns belong to later feature services.
type Service struct {
	runs      domain.RunRepository
	approvals domain.ApprovalRepository
	events    domain.EventPublisher
	clock     domain.Clock
	ids       domain.IDGenerator
}

func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Runs == nil {
		return nil, fmt.Errorf("%w: run repository is required", domain.ErrInvalidArgument)
	}
	if dependencies.Clock == nil {
		dependencies.Clock = domain.SystemClock{}
	}
	if dependencies.IDs == nil {
		dependencies.IDs = domain.RandomIDGenerator{}
	}
	if dependencies.Events == nil {
		dependencies.Events = NopEventPublisher{}
	}
	return &Service{
		runs: dependencies.Runs, approvals: dependencies.Approvals,
		events: dependencies.Events, clock: dependencies.Clock, ids: dependencies.IDs,
	}, nil
}

type CreateRunInput struct {
	ID             domain.ID
	Kind           domain.RunKind
	ConversationID domain.ID
	ParentRunID    domain.ID
	Budget         domain.RunBudget
}

func (s *Service) CreateRun(ctx context.Context, input CreateRunInput) (domain.AgentRun, error) {
	if err := contextError(ctx); err != nil {
		return domain.AgentRun{}, err
	}
	runID := input.ID
	if runID.Empty() {
		var err error
		runID, err = s.ids.NewID("run")
		if err != nil {
			return domain.AgentRun{}, fmt.Errorf("create run id: %w", err)
		}
	}
	run, err := domain.NewRun(runID, input.Kind, input.ConversationID, s.clock.Now())
	if err != nil {
		return domain.AgentRun{}, err
	}
	if !input.Budget.Valid() {
		return domain.AgentRun{}, fmt.Errorf("%w: invalid run budget", domain.ErrInvalidArgument)
	}
	run.ParentRunID = input.ParentRunID
	run.Budget = input.Budget
	if err := s.runs.Create(ctx, run); err != nil {
		return domain.AgentRun{}, err
	}
	if err := s.publish(ctx, domain.EventRunCreated, run.ID, run.ID, domain.ActorSystem, RunCreatedPayload{Run: run}); err != nil {
		return domain.AgentRun{}, err
	}
	return run, nil
}

func (s *Service) GetRun(ctx context.Context, id domain.ID) (domain.AgentRun, error) {
	if err := contextError(ctx); err != nil {
		return domain.AgentRun{}, err
	}
	return s.runs.Get(ctx, id)
}

type TransitionRunInput struct {
	RunID  domain.ID
	State  domain.RunState
	Reason string
}

// TransitionRun changes only durable lifecycle state. A worker or future
// agent runtime remains responsible for the actual work represented by it.
func (s *Service) TransitionRun(ctx context.Context, input TransitionRunInput) (domain.AgentRun, error) {
	if err := contextError(ctx); err != nil {
		return domain.AgentRun{}, err
	}
	run, err := s.runs.Get(ctx, input.RunID)
	if err != nil {
		return domain.AgentRun{}, err
	}
	previous := run
	if input.State == domain.RunStateFailed {
		if err := run.Fail(input.Reason, s.clock.Now()); err != nil {
			return domain.AgentRun{}, err
		}
	} else if err := run.Transition(input.State, s.clock.Now()); err != nil {
		return domain.AgentRun{}, err
	}
	if err := s.runs.Save(ctx, run); err != nil {
		return domain.AgentRun{}, err
	}
	if err := s.publish(ctx, domain.EventRunStateChanged, run.ID, run.ID, domain.ActorSystem, RunStateChangedPayload{Before: previous, After: run}); err != nil {
		return domain.AgentRun{}, err
	}
	return run, nil
}

func (s *Service) RequestCancellation(ctx context.Context, id domain.ID) (domain.AgentRun, error) {
	run, err := s.TransitionRun(ctx, TransitionRunInput{RunID: id, State: domain.RunStateCancelling})
	if err != nil {
		return domain.AgentRun{}, err
	}
	if err := s.publish(ctx, domain.EventRunCancellationRequested, run.ID, run.ID, domain.ActorUser, RunCancellationRequestedPayload{RunID: run.ID}); err != nil {
		return domain.AgentRun{}, err
	}
	return run, nil
}

type RequestApprovalInput struct {
	ID         domain.ID
	RunID      domain.ID
	ActionHash string
	Action     string
	ToolID     string
	Risk       domain.RiskLevel
	Scope      domain.CapabilityScope
	ExpiresAt  time.Time
}

// RequestApproval records a request and moves a running run to
// waiting_approval. No external action is performed here.
func (s *Service) RequestApproval(ctx context.Context, input RequestApprovalInput) (domain.Approval, error) {
	if s.approvals == nil {
		return domain.Approval{}, fmt.Errorf("%w: approval repository is not configured", domain.ErrInvalidArgument)
	}
	if err := contextError(ctx); err != nil {
		return domain.Approval{}, err
	}
	run, err := s.runs.Get(ctx, input.RunID)
	if err != nil {
		return domain.Approval{}, err
	}
	if run.State != domain.RunStateRunning {
		return domain.Approval{}, fmt.Errorf("%w: approval requires running run", domain.ErrInvalidTransition)
	}
	approvalID := input.ID
	if approvalID.Empty() {
		approvalID, err = s.ids.NewID("approval")
		if err != nil {
			return domain.Approval{}, fmt.Errorf("create approval id: %w", err)
		}
	}
	approval, err := domain.NewApproval(approvalID, input.RunID, input.ActionHash, input.Action, input.Risk, input.Scope, s.clock.Now())
	if err != nil {
		return domain.Approval{}, err
	}
	approval.ToolID = input.ToolID
	approval.ExpiresAt = input.ExpiresAt.UTC()
	if err := s.approvals.Create(ctx, approval); err != nil {
		return domain.Approval{}, err
	}
	previous := run
	if err := run.Transition(domain.RunStateWaitingApproval, s.clock.Now()); err != nil {
		return domain.Approval{}, err
	}
	if err := s.runs.Save(ctx, run); err != nil {
		return domain.Approval{}, err
	}
	if err := s.publish(ctx, domain.EventApprovalRequested, approval.ID, approval.RunID, domain.ActorAgent, ApprovalRequestedPayload{Approval: approval}); err != nil {
		return domain.Approval{}, err
	}
	if err := s.publish(ctx, domain.EventRunStateChanged, run.ID, run.ID, domain.ActorSystem, RunStateChangedPayload{Before: previous, After: run}); err != nil {
		return domain.Approval{}, err
	}
	return approval, nil
}

func (s *Service) ResolveApproval(ctx context.Context, id domain.ID, decision domain.ApprovalDecision, actor domain.Actor, reason string) (domain.Approval, error) {
	if s.approvals == nil {
		return domain.Approval{}, fmt.Errorf("%w: approval repository is not configured", domain.ErrInvalidArgument)
	}
	if err := contextError(ctx); err != nil {
		return domain.Approval{}, err
	}
	approval, err := s.approvals.Get(ctx, id)
	if err != nil {
		return domain.Approval{}, err
	}
	now := s.clock.Now()
	switch decision {
	case domain.ApprovalApproved:
		err = approval.Approve(actor, reason, now)
	case domain.ApprovalDenied:
		err = approval.Deny(actor, reason, now)
	case domain.ApprovalExpired:
		err = approval.Expire(now)
	case domain.ApprovalCancelled:
		err = approval.Cancel(now)
	default:
		err = fmt.Errorf("%w: invalid approval decision %q", domain.ErrInvalidArgument, decision)
	}
	if err != nil {
		return domain.Approval{}, err
	}
	if err := s.approvals.Save(ctx, approval); err != nil {
		return domain.Approval{}, err
	}
	if err := s.publish(ctx, domain.EventApprovalResolved, approval.ID, approval.RunID, actor, ApprovalResolvedPayload{Approval: approval}); err != nil {
		return domain.Approval{}, err
	}
	return approval, nil
}

func (s *Service) publish(ctx context.Context, eventType domain.EventType, aggregateID, runID domain.ID, actor domain.Actor, payload any) error {
	eventID, err := s.ids.NewID("event")
	if err != nil {
		return fmt.Errorf("create event id: %w", err)
	}
	event, err := domain.NewEvent(eventID, eventType, aggregateID, runID, actor, s.clock.Now(), payload)
	if err != nil {
		return err
	}
	return s.events.Publish(ctx, event)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", domain.ErrInvalidArgument)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

type NopEventPublisher struct{}

func (NopEventPublisher) Publish(context.Context, domain.Event) error { return nil }

type RunCreatedPayload struct{ Run domain.AgentRun }

type RunStateChangedPayload struct {
	Before domain.AgentRun
	After  domain.AgentRun
}

type RunCancellationRequestedPayload struct{ RunID domain.ID }

type ApprovalRequestedPayload struct{ Approval domain.Approval }

type ApprovalResolvedPayload struct{ Approval domain.Approval }
