package domain

import (
	"fmt"
	"time"
)

type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

func (r RiskLevel) Valid() bool {
	switch r {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	default:
		return false
	}
}

type ApprovalDecision string

const (
	ApprovalPending   ApprovalDecision = "pending"
	ApprovalApproved  ApprovalDecision = "approved"
	ApprovalDenied    ApprovalDecision = "denied"
	ApprovalExpired   ApprovalDecision = "expired"
	ApprovalCancelled ApprovalDecision = "cancelled"
)

func (d ApprovalDecision) Valid() bool {
	switch d {
	case ApprovalPending, ApprovalApproved, ApprovalDenied, ApprovalExpired, ApprovalCancelled:
		return true
	default:
		return false
	}
}

func (d ApprovalDecision) Final() bool {
	switch d {
	case ApprovalApproved, ApprovalDenied, ApprovalExpired, ApprovalCancelled:
		return true
	default:
		return false
	}
}

// Approval is a durable request to allow one side effect. action_hash binds a
// decision to the exact proposed action, rather than to a mutable tool name.
type Approval struct {
	ID          ID               `json:"id"`
	RunID       ID               `json:"run_id"`
	ActionHash  string           `json:"action_hash"`
	Action      string           `json:"action"`
	ToolID      string           `json:"tool_id,omitempty"`
	Risk        RiskLevel        `json:"risk"`
	Scope       CapabilityScope  `json:"scope"`
	Decision    ApprovalDecision `json:"decision"`
	RequestedAt time.Time        `json:"requested_at"`
	ExpiresAt   time.Time        `json:"expires_at,omitempty"`
	DecidedAt   time.Time        `json:"decided_at,omitempty"`
	DecidedBy   Actor            `json:"decided_by,omitempty"`
	Reason      string           `json:"reason,omitempty"`
	Version     uint64           `json:"version"`
}

func NewApproval(id, runID ID, actionHash, action string, risk RiskLevel, scope CapabilityScope, now time.Time) (Approval, error) {
	if id.Empty() || runID.Empty() || actionHash == "" || action == "" || !risk.Valid() || !scope.Valid() || now.IsZero() {
		return Approval{}, fmt.Errorf("%w: incomplete approval request", ErrInvalidArgument)
	}
	now = now.UTC()
	return Approval{
		ID: id, RunID: runID, ActionHash: actionHash, Action: action,
		Risk: risk, Scope: scope, Decision: ApprovalPending,
		RequestedAt: now, Version: 1,
	}, nil
}

func (a Approval) Pending() bool { return a.Decision == ApprovalPending }

func (a *Approval) decide(decision ApprovalDecision, by Actor, reason string, now time.Time) error {
	if a == nil {
		return fmt.Errorf("%w: nil approval", ErrInvalidArgument)
	}
	if !a.Pending() {
		return fmt.Errorf("%w: %s", ErrAlreadyDecided, a.Decision)
	}
	if !by.Valid() {
		return fmt.Errorf("%w: invalid decision actor", ErrInvalidArgument)
	}
	if decision == ApprovalPending || !decision.Final() {
		return fmt.Errorf("%w: invalid final decision", ErrInvalidArgument)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: decision timestamp is required", ErrInvalidArgument)
	}
	a.Decision = decision
	a.DecidedAt = now.UTC()
	a.DecidedBy = by
	a.Reason = reason
	a.Version++
	return nil
}

func (a *Approval) Approve(by Actor, reason string, now time.Time) error {
	return a.decide(ApprovalApproved, by, reason, now)
}

func (a *Approval) Deny(by Actor, reason string, now time.Time) error {
	return a.decide(ApprovalDenied, by, reason, now)
}

func (a *Approval) Expire(now time.Time) error {
	return a.decide(ApprovalExpired, ActorSystem, "approval expired", now)
}

func (a *Approval) Cancel(now time.Time) error {
	return a.decide(ApprovalCancelled, ActorSystem, "approval cancelled", now)
}
