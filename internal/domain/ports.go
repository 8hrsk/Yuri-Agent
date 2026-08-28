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

// PersonaRepository is the durable boundary for the mutable personality
// layer. Implementations must append revisions rather than overwrite history;
// immutable policy and identity seed are intentionally outside this port.
type PersonaRepository interface {
	Create(context.Context, MutablePersona) error
	Get(context.Context, ID) (MutablePersona, error)
	GetVersion(context.Context, ID, uint64) (MutablePersona, error)
	ListVersions(context.Context, ID, ...int) ([]PersonaVersionRecord, error)
	AppendVersion(context.Context, MutablePersona, uint64, ...any) (MutablePersona, error)
	Rollback(context.Context, ID, ...any) (MutablePersona, error)
	Reset(context.Context, ID, ...any) (MutablePersona, error)
}

// PersonaStore is the architectural alias used by application services.
type PersonaStore = PersonaRepository

type RelationshipRepository interface {
	Create(context.Context, RelationshipState) error
	Get(context.Context, ID) (RelationshipState, error)
	GetVersion(context.Context, ID, uint64) (RelationshipState, error)
	ListVersions(context.Context, ID, ...int) ([]RelationshipVersionRecord, error)
	AppendVersion(context.Context, RelationshipState, uint64, ...any) (RelationshipState, error)
	Rollback(context.Context, ID, ...any) (RelationshipState, error)
	Reset(context.Context, ID, ...any) (RelationshipState, error)
}

type RelationshipStore = RelationshipRepository

type AffectiveRepository interface {
	CreateState(context.Context, AffectiveState) error
	GetState(context.Context, ID) (AffectiveState, error)
	GetVersion(context.Context, ID, uint64) (AffectiveState, error)
	ListVersions(context.Context, ID, ...int) ([]AffectiveVersionRecord, error)
	AppendVersion(context.Context, AffectiveState, uint64, ...any) (AffectiveState, error)
	AppendEvent(context.Context, ...any) (AffectiveState, error)
	ListEvents(context.Context, ID, ...any) ([]AffectiveEvent, error)
	Rollback(context.Context, ID, ...any) (AffectiveState, error)
	Reset(context.Context, ID, ...any) (AffectiveState, error)
}

type AffectStore = AffectiveRepository

// PersonaVersionRecord, RelationshipVersionRecord and AffectiveVersionRecord
// are intentionally defined in domain as small immutable history envelopes.
// The concrete SQLite adapter may expose richer adapter metadata without
// forcing callers to depend on SQL details.

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
