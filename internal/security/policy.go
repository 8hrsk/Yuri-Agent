package security

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// PolicyEvaluator implements the minimum deny-by-default decision boundary.
// Grants are the only source of capability authorization. Risk determines
// whether an authorized operation can proceed immediately or needs a durable
// user approval; it never broadens a grant's scope.
type PolicyEvaluator struct {
	mu     sync.RWMutex
	grants map[domain.ID][]domain.PermissionGrant
	clock  domain.Clock
}

type PolicyOption func(*PolicyEvaluator)

func WithPolicyClock(clock domain.Clock) PolicyOption {
	return func(evaluator *PolicyEvaluator) {
		if clock != nil {
			evaluator.clock = clock
		}
	}
}

func WithPolicyGrants(grants []domain.PermissionGrant) PolicyOption {
	return func(evaluator *PolicyEvaluator) {
		for _, grant := range grants {
			if grant.Valid() {
				evaluator.grants[grant.SubjectID] = append(evaluator.grants[grant.SubjectID], grant)
			}
		}
	}
}

func WithPolicyGrant(grant domain.PermissionGrant) PolicyOption {
	return WithPolicyGrants([]domain.PermissionGrant{grant})
}

func NewPolicyEvaluator(options ...PolicyOption) *PolicyEvaluator {
	evaluator := &PolicyEvaluator{
		grants: make(map[domain.ID][]domain.PermissionGrant),
		clock:  domain.SystemClock{},
	}
	for _, option := range options {
		if option != nil {
			option(evaluator)
		}
	}
	return evaluator
}

// NewPolicyEngine is an expressive alias for callers using the domain port's
// older name.
func NewPolicyEngine(options ...PolicyOption) *PolicyEvaluator {
	return NewPolicyEvaluator(options...)
}

var _ domain.PolicyEngine = (*PolicyEvaluator)(nil)
var _ domain.CapabilityAuthorizer = (*PolicyEvaluator)(nil)

func (evaluator *PolicyEvaluator) AddGrant(grant domain.PermissionGrant) error {
	if evaluator == nil {
		return fmt.Errorf("%w: nil policy evaluator", domain.ErrInvalidArgument)
	}
	if !grant.Valid() {
		return fmt.Errorf("%w: invalid permission grant", domain.ErrInvalidArgument)
	}
	evaluator.mu.Lock()
	defer evaluator.mu.Unlock()
	for _, existing := range evaluator.grants[grant.SubjectID] {
		if existing.ID == grant.ID {
			return domain.ErrConflict
		}
	}
	evaluator.grants[grant.SubjectID] = append(evaluator.grants[grant.SubjectID], grant)
	return nil
}

func (evaluator *PolicyEvaluator) RevokeGrant(id domain.ID) error {
	if evaluator == nil {
		return fmt.Errorf("%w: nil policy evaluator", domain.ErrInvalidArgument)
	}
	if id.Empty() {
		return fmt.Errorf("%w: grant id is required", domain.ErrInvalidArgument)
	}
	evaluator.mu.Lock()
	defer evaluator.mu.Unlock()
	for subject, grants := range evaluator.grants {
		for index, grant := range grants {
			if grant.ID != id {
				continue
			}
			evaluator.grants[subject] = append(grants[:index], grants[index+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

// Grants returns a defensive snapshot, useful for diagnostics and tests.
func (evaluator *PolicyEvaluator) Grants() []domain.PermissionGrant {
	if evaluator == nil {
		return nil
	}
	evaluator.mu.RLock()
	defer evaluator.mu.RUnlock()
	result := make([]domain.PermissionGrant, 0)
	for _, grants := range evaluator.grants {
		for _, grant := range grants {
			grant.Scope.Values = append([]string(nil), grant.Scope.Values...)
			result = append(result, grant)
		}
	}
	return result
}

func (evaluator *PolicyEvaluator) Evaluate(request domain.PermissionRequest) (domain.PolicyResult, error) {
	if evaluator == nil {
		return domain.PolicyResult{}, fmt.Errorf("%w: nil policy evaluator", domain.ErrInvalidArgument)
	}
	if !request.Valid() {
		return domain.PolicyResult{}, fmt.Errorf("%w: invalid permission request", domain.ErrInvalidArgument)
	}
	if request.Risk == domain.RiskCritical {
		return domain.PolicyResult{Decision: domain.PermissionDeny, Reason: "critical operations are unavailable"}, nil
	}
	clock := evaluator.clock
	if clock == nil {
		clock = domain.SystemClock{}
	}
	now := clock.Now()
	if now.IsZero() {
		return domain.PolicyResult{}, fmt.Errorf("%w: policy clock returned zero time", domain.ErrInvalidArgument)
	}
	evaluator.mu.RLock()
	grants := append([]domain.PermissionGrant(nil), evaluator.grants[request.SubjectID]...)
	evaluator.mu.RUnlock()
	for _, grant := range grants {
		if grant.Capability != request.Capability || grant.GrantedAt.After(now) {
			continue
		}
		if !grant.ExpiresAt.IsZero() && !now.Before(grant.ExpiresAt) {
			continue
		}
		if !policyScopeCovers(grant.Scope, request.Scope) {
			continue
		}
		if request.Risk == domain.RiskLow {
			return domain.PolicyResult{Decision: domain.PermissionAllow, Reason: "active capability grant covers request"}, nil
		}
		return domain.PolicyResult{Decision: domain.PermissionNeedsApproval, Reason: "active grant covers request; user approval is required for this risk level"}, nil
	}
	return domain.PolicyResult{Decision: domain.PermissionDeny, Reason: "no active grant covers request"}, nil
}

func (evaluator *PolicyEvaluator) Authorize(ctx context.Context, request domain.PermissionRequest) (domain.PolicyResult, error) {
	if ctx == nil {
		return domain.PolicyResult{}, fmt.Errorf("%w: nil context", domain.ErrInvalidArgument)
	}
	select {
	case <-ctx.Done():
		return domain.PolicyResult{}, ctx.Err()
	default:
	}
	return evaluator.Evaluate(request)
}

// policyScopeCovers applies the shared coverage rule from scope.go. The only
// thing this evaluator adds is symlink-aware canonicalization of filesystem
// values, which it must do because it authorizes real paths on this machine:
// the shared rule is a lexical containment check and would otherwise accept a
// symlink that points outside the granted root.
//
// Canonicalization mirrors the previous per-pair behaviour exactly. A granted
// value that cannot be resolved is dropped (it could never have covered
// anything), and a requested value that cannot be resolved fails the whole
// check (no granted value could have covered it).
func policyScopeCovers(granted, requested domain.CapabilityScope) bool {
	if granted.Kind == domain.ScopeFilesystem && requested.Kind == domain.ScopeFilesystem {
		canonicalGranted, grantedOK := canonicalScope(granted, true)
		canonicalRequested, requestedOK := canonicalScope(requested, false)
		if !grantedOK || !requestedOK {
			return false
		}
		return ScopeCovers(canonicalGranted, canonicalRequested)
	}
	return ScopeCovers(granted, requested)
}

// canonicalScope resolves every filesystem value in a scope. When dropUnresolvable
// is true an unresolvable value is skipped; otherwise it fails the whole scope.
func canonicalScope(scope domain.CapabilityScope, dropUnresolvable bool) (domain.CapabilityScope, bool) {
	values := make([]string, 0, len(scope.Values))
	for _, value := range scope.Values {
		canonical, err := canonicalForScope(strings.TrimSpace(value))
		if err != nil {
			if dropUnresolvable {
				continue
			}
			return domain.CapabilityScope{}, false
		}
		values = append(values, canonical)
	}
	return domain.CapabilityScope{Kind: scope.Kind, Values: values}, true
}

func canonicalForScope(value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("relative filesystem scope")
	}
	cleaned := filepath.Clean(value)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// A create request legitimately names a missing leaf. Canonicalize its
	// existing parent so scope checks still reject symlink escapes without
	// requiring the target file to exist before authorization.
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(cleaned))
	if parentErr != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(cleaned)), nil
}
