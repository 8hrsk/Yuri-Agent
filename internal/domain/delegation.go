package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DelegationStatus is the durable lifecycle of an anonymous, ephemeral child
// execution. A delegation is metadata only: it never creates an AgentProfile,
// persona state, or memory namespace.
type DelegationStatus string

const (
	DelegationStatusCreated    DelegationStatus = "created"
	DelegationStatusQueued     DelegationStatus = "queued"
	DelegationStatusRunning    DelegationStatus = "running"
	DelegationStatusCancelling DelegationStatus = "cancelling"
	DelegationStatusCompleted  DelegationStatus = "completed"
	DelegationStatusFailed     DelegationStatus = "failed"
	DelegationStatusCancelled  DelegationStatus = "cancelled"
)

func (s DelegationStatus) Valid() bool {
	switch s {
	case DelegationStatusCreated, DelegationStatusQueued, DelegationStatusRunning, DelegationStatusCancelling,
		DelegationStatusCompleted, DelegationStatusFailed, DelegationStatusCancelled:
		return true
	default:
		return false
	}
}

func (s DelegationStatus) Terminal() bool {
	return s == DelegationStatusCompleted || s == DelegationStatusFailed || s == DelegationStatusCancelled
}

// Delegation is intentionally separate from AgentRun. It records the bounded
// anonymous child requested by a named principal, while the eventual runtime
// remains free to execute it without granting it identity or memory access.
type Delegation struct {
	ID               ID               `json:"id"`
	ChildRunID       ID               `json:"child_run_id"`
	PrincipalAgentID ID               `json:"principal_agent_id"`
	ParentRunID      ID               `json:"parent_run_id"`
	ScopeJSON        string           `json:"scope_json"`
	Depth            int              `json:"depth"`
	Status           DelegationStatus `json:"status"`
	Budget           RunBudget        `json:"budget"`
	IdempotencyKey   string           `json:"idempotency_key"`
	RequestHash      string           `json:"request_hash"`
	ResultText       string           `json:"result_text,omitempty"`
	Failure          string           `json:"failure,omitempty"`
	Version          uint64           `json:"version"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	StartedAt        time.Time        `json:"started_at,omitempty"`
	FinishedAt       time.Time        `json:"finished_at,omitempty"`
}

func NewDelegation(id, childRunID, principalAgentID, parentRunID ID, scopeJSON, idempotencyKey, requestHash string, now time.Time) (Delegation, error) {
	delegation := Delegation{
		ID: id, ChildRunID: childRunID, PrincipalAgentID: principalAgentID, ParentRunID: parentRunID,
		ScopeJSON: strings.TrimSpace(scopeJSON), Depth: 1,
		Status: DelegationStatusCreated, IdempotencyKey: strings.TrimSpace(idempotencyKey), RequestHash: strings.TrimSpace(requestHash),
		Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := delegation.Validate(); err != nil {
		return Delegation{}, err
	}
	return delegation, nil
}

func (d Delegation) Validate() error {
	if d.ID.Empty() || d.ChildRunID.Empty() || d.PrincipalAgentID.Empty() || d.ParentRunID.Empty() {
		return fmt.Errorf("%w: delegation and ownership ids are required", ErrInvalidArgument)
	}
	if d.Depth != 1 {
		return fmt.Errorf("%w: delegation depth must be exactly one", ErrInvalidArgument)
	}
	if !d.Status.Valid() || !d.Budget.Valid() {
		return fmt.Errorf("%w: invalid delegation status or budget", ErrInvalidArgument)
	}
	if d.Version == 0 {
		return fmt.Errorf("%w: delegation version must be positive", ErrInvalidArgument)
	}
	if strings.TrimSpace(d.IdempotencyKey) == "" || len(d.IdempotencyKey) > 256 {
		return fmt.Errorf("%w: delegation idempotency key is required and must be at most 256 bytes", ErrInvalidArgument)
	}
	if strings.TrimSpace(d.RequestHash) == "" || len(d.RequestHash) > 256 || strings.ContainsRune(d.RequestHash, '\x00') {
		return fmt.Errorf("%w: delegation request hash is required and must be at most 256 bytes", ErrInvalidArgument)
	}
	if len(d.ResultText) > 16*1024 || strings.ContainsRune(d.ResultText, '\x00') {
		return fmt.Errorf("%w: delegation result must be at most 16 KiB and contain no NUL", ErrInvalidArgument)
	}
	if len(d.Failure) > 1024 || strings.ContainsRune(d.Failure, '\x00') {
		return fmt.Errorf("%w: delegation failure must be at most 1 KiB and contain no NUL", ErrInvalidArgument)
	}
	if !d.Status.Terminal() && (strings.TrimSpace(d.ResultText) != "" || strings.TrimSpace(d.Failure) != "") {
		return fmt.Errorf("%w: non-terminal delegation cannot persist result or failure", ErrInvalidArgument)
	}
	switch d.Status {
	case DelegationStatusCompleted:
		if strings.TrimSpace(d.ResultText) == "" || strings.TrimSpace(d.Failure) != "" {
			return fmt.Errorf("%w: completed delegation requires result and no failure", ErrInvalidArgument)
		}
	case DelegationStatusFailed:
		if strings.TrimSpace(d.ResultText) != "" || strings.TrimSpace(d.Failure) == "" {
			return fmt.Errorf("%w: failed delegation requires failure and no result", ErrInvalidArgument)
		}
	case DelegationStatusCancelled:
		if strings.TrimSpace(d.ResultText) != "" || strings.TrimSpace(d.Failure) != "" {
			return fmt.Errorf("%w: cancelled delegation cannot persist result or failure", ErrInvalidArgument)
		}
	}
	scope := strings.TrimSpace(d.ScopeJSON)
	if scope == "" {
		scope = "{}"
	}
	var object map[string]json.RawMessage
	if !json.Valid([]byte(scope)) || json.Unmarshal([]byte(scope), &object) != nil {
		return fmt.Errorf("%w: delegation scope_json must be a JSON object", ErrInvalidArgument)
	}
	if len(scope) > 64*1024 {
		return fmt.Errorf("%w: delegation scope_json is too large", ErrInvalidArgument)
	}
	if d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() || d.UpdatedAt.Before(d.CreatedAt) {
		return fmt.Errorf("%w: delegation timestamps are required", ErrInvalidArgument)
	}
	if !d.StartedAt.IsZero() && d.StartedAt.Before(d.CreatedAt) {
		return fmt.Errorf("%w: delegation started_at precedes created_at", ErrInvalidArgument)
	}
	if !d.FinishedAt.IsZero() && d.FinishedAt.Before(d.CreatedAt) {
		return fmt.Errorf("%w: delegation finished_at precedes created_at", ErrInvalidArgument)
	}
	return nil
}

func (d Delegation) CanTransition(next DelegationStatus) bool {
	if !next.Valid() || d.Status.Terminal() {
		return false
	}
	switch d.Status {
	case DelegationStatusCreated:
		return next == DelegationStatusQueued || next == DelegationStatusCancelled
	case DelegationStatusQueued:
		return next == DelegationStatusRunning || next == DelegationStatusCancelled
	case DelegationStatusRunning:
		return next == DelegationStatusCompleted || next == DelegationStatusFailed || next == DelegationStatusCancelling
	case DelegationStatusCancelling:
		return next == DelegationStatusCancelled || next == DelegationStatusFailed
	default:
		return false
	}
}

func (d *Delegation) Transition(next DelegationStatus, now time.Time) error {
	if d == nil {
		return fmt.Errorf("%w: nil delegation", ErrInvalidArgument)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: delegation transition timestamp is required", ErrInvalidArgument)
	}
	if !d.CanTransition(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, d.Status, next)
	}
	d.Status = next
	d.UpdatedAt = now.UTC()
	d.Version++
	if next == DelegationStatusRunning && d.StartedAt.IsZero() {
		d.StartedAt = d.UpdatedAt
	}
	if next.Terminal() {
		d.FinishedAt = d.UpdatedAt
	}
	return nil
}
