package domain

import "context"

// RunRepository is the persistence boundary for durable run state. Save is
// expected to perform optimistic concurrency using AgentRun.Version.
type RunRepository interface {
	Create(context.Context, AgentRun) error
	Get(context.Context, ID) (AgentRun, error)
	Save(context.Context, AgentRun) error
}

// RunStore is the name used by the architecture document. Keep Repository as
// the more explicit persistence term while exposing the architectural alias.
type RunStore = RunRepository

// SchedulerStore is the architectural alias for the durable schedule/job
// repository. The concrete contract is declared with scheduler entities in
// scheduler.go to keep worker and storage adapters independent of UI code.
type SchedulerStore = SchedulerRepository

type ApprovalRepository interface {
	Create(context.Context, Approval) error
	Get(context.Context, ID) (Approval, error)
	Save(context.Context, Approval) error
	ListByRun(context.Context, ID) ([]Approval, error)
}

type ApprovalStore = ApprovalRepository

// CapabilityAuthorizer is deliberately separate from PolicyEngine: an
// authorizer answers whether a previously granted capability covers a request;
// PolicyEngine also decides whether a confirmation is required.
type CapabilityAuthorizer interface {
	Authorize(context.Context, PermissionRequest) (PolicyResult, error)
}

// PolicyEvaluator is the architectural name for the policy boundary.
type PolicyEvaluator = PolicyEngine

// Context represents the minimum context handoff contract between a UI run,
// background worker, and future inference backends. It is metadata only in
// the foundation milestone; message and memory models arrive later.
type Context struct {
	ConversationID ID
	RunID          ID
	ParentRunID    ID
	Kind           RunKind
}
