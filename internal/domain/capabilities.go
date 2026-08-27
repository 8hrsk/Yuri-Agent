package domain

import (
	"fmt"
	"strings"
	"time"
)

// Capability is a deliberately small, stable vocabulary shared by policy,
// tools, plugins, and the UI. Concrete adapters may add metadata, but should
// not silently reinterpret an existing capability.
type Capability string

const (
	CapabilityFilesystemRead    Capability = "filesystem.read"
	CapabilityFilesystemWrite   Capability = "filesystem.write"
	CapabilityFilesystemDelete  Capability = "filesystem.delete"
	CapabilityNetworkHTTP       Capability = "network.http"
	CapabilitySecretsUse        Capability = "secrets.use"
	CapabilityNotificationsSend Capability = "notifications.send"
	CapabilitySchedulerManage   Capability = "scheduler.manage"
	CapabilityMemoryRead        Capability = "memory.read"
	CapabilityMemoryWrite       Capability = "memory.write"
	CapabilityMemoryDelete      Capability = "memory.delete"
	CapabilityExternalSend      Capability = "external.send"
)

func (c Capability) Valid() bool {
	switch c {
	case CapabilityFilesystemRead, CapabilityFilesystemWrite,
		CapabilityFilesystemDelete, CapabilityNetworkHTTP, CapabilitySecretsUse,
		CapabilityNotificationsSend, CapabilitySchedulerManage,
		CapabilityMemoryRead, CapabilityMemoryWrite, CapabilityMemoryDelete,
		CapabilityExternalSend:
		return true
	default:
		return false
	}
}

// ScopeKind describes how a grant is narrowed. Enforcement belongs to a
// policy adapter; this value only gives all layers a common representation.
type ScopeKind string

const (
	ScopeUnrestricted ScopeKind = "unrestricted"
	ScopeFilesystem   ScopeKind = "filesystem"
	ScopeNetwork      ScopeKind = "network"
	ScopeResource     ScopeKind = "resource"
)

func (k ScopeKind) Valid() bool {
	switch k {
	case ScopeUnrestricted, ScopeFilesystem, ScopeNetwork, ScopeResource:
		return true
	default:
		return false
	}
}

// CapabilityScope is intentionally declarative. Paths and domains are not
// normalized here because the security adapter must perform platform-aware
// canonicalization immediately before granting access.
type CapabilityScope struct {
	Kind   ScopeKind `json:"kind"`
	Values []string  `json:"values,omitempty"`
}

func UnrestrictedScope() CapabilityScope { return CapabilityScope{Kind: ScopeUnrestricted} }

func (s CapabilityScope) Valid() bool {
	if !s.Kind.Valid() {
		return false
	}
	if s.Kind == ScopeUnrestricted {
		return len(s.Values) == 0
	}
	return len(s.Values) > 0
}

// CapabilitySet is an immutable-by-convention set. Has and Contains are
// read-only; Add returns a copy so callers cannot mutate a permission set
// held by another layer accidentally.
type CapabilitySet []Capability

func (set CapabilitySet) Has(capability Capability) bool {
	for _, existing := range set {
		if existing == capability {
			return true
		}
	}
	return false
}

func (set CapabilitySet) Add(capability Capability) (CapabilitySet, error) {
	if !capability.Valid() {
		return nil, fmt.Errorf("%w: unknown capability %q", ErrInvalidArgument, capability)
	}
	if set.Has(capability) {
		return append(CapabilitySet(nil), set...), nil
	}
	result := append(CapabilitySet(nil), set...)
	return append(result, capability), nil
}

func (set CapabilitySet) Validate() error {
	seen := make(map[Capability]struct{}, len(set))
	for _, capability := range set {
		if !capability.Valid() {
			return fmt.Errorf("%w: unknown capability %q", ErrInvalidArgument, capability)
		}
		if _, ok := seen[capability]; ok {
			return fmt.Errorf("%w: duplicate capability %q", ErrInvalidArgument, capability)
		}
		seen[capability] = struct{}{}
	}
	return nil
}

// PermissionGrant records a capability granted to a subject. It does not
// itself grant access; PolicyEngine must evaluate expiry and scope.
type PermissionGrant struct {
	ID         ID              `json:"id"`
	SubjectID  ID              `json:"subject_id"`
	Capability Capability      `json:"capability"`
	Scope      CapabilityScope `json:"scope"`
	GrantedAt  time.Time       `json:"granted_at"`
	ExpiresAt  time.Time       `json:"expires_at,omitempty"`
}

func (g PermissionGrant) Valid() bool {
	return !g.ID.Empty() && !g.SubjectID.Empty() && g.Capability.Valid() && g.Scope.Valid() && !g.GrantedAt.IsZero()
}

// PermissionRequest is passed to policy before any side effect.
type PermissionRequest struct {
	SubjectID  ID
	Capability Capability
	Scope      CapabilityScope
	Action     string
	Risk       RiskLevel
}

func (r PermissionRequest) Valid() bool {
	return !r.SubjectID.Empty() && r.Capability.Valid() && r.Scope.Valid() && r.Action != "" && r.Risk.Valid()
}

type PermissionDecision string

const (
	PermissionAllow         PermissionDecision = "allow"
	PermissionDeny          PermissionDecision = "deny"
	PermissionNeedsApproval PermissionDecision = "needs_approval"
)

type PolicyResult struct {
	Decision PermissionDecision
	Reason   string
}

type PolicyEngine interface {
	Evaluate(request PermissionRequest) (PolicyResult, error)
}

func (r PolicyResult) Valid() bool {
	return r.Decision == PermissionAllow || r.Decision == PermissionDeny || r.Decision == PermissionNeedsApproval
}

func NormalizeCapabilityName(value string) Capability {
	return Capability(strings.ToLower(strings.TrimSpace(value)))
}
