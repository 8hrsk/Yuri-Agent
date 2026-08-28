package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestPolicyEvaluatorIsDenyByDefaultAndRiskAware(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	subject := domain.ID("agent")
	request := domain.PermissionRequest{
		SubjectID: subject, Capability: domain.CapabilityFilesystemRead,
		Scope:  domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{root}},
		Action: "filesystem.read " + root, Risk: domain.RiskLow,
	}
	evaluator := NewPolicyEvaluator(WithPolicyClock(domain.FixedClock{At: now}))
	decision, err := evaluator.Evaluate(request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != domain.PermissionDeny {
		t.Fatalf("no grant decision = %#v", decision)
	}
	grant := domain.PermissionGrant{
		ID: "grant-1", SubjectID: subject, Capability: domain.CapabilityFilesystemRead,
		Scope: request.Scope, GrantedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	if err := evaluator.AddGrant(grant); err != nil {
		t.Fatal(err)
	}
	decision, err = evaluator.Authorize(context.Background(), request)
	if err != nil || decision.Decision != domain.PermissionAllow {
		t.Fatalf("low decision = %#v, %v", decision, err)
	}
	request.Risk = domain.RiskMedium
	decision, err = evaluator.Evaluate(request)
	if err != nil || decision.Decision != domain.PermissionNeedsApproval {
		t.Fatalf("medium decision = %#v, %v", decision, err)
	}
	request.Risk = domain.RiskCritical
	decision, err = evaluator.Evaluate(request)
	if err != nil || decision.Decision != domain.PermissionDeny {
		t.Fatalf("critical decision = %#v, %v", decision, err)
	}
	if err := evaluator.RevokeGrant(grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.Evaluate(request); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyEvaluatorChecksScopeAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	evaluator := NewPolicyEvaluator(
		WithPolicyClock(domain.FixedClock{At: now}),
		WithPolicyGrant(domain.PermissionGrant{
			ID: "grant-expired", SubjectID: "agent", Capability: domain.CapabilityFilesystemRead,
			Scope:     domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{root}},
			GrantedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Second),
		}),
	)
	request := domain.PermissionRequest{
		SubjectID: "agent", Capability: domain.CapabilityFilesystemRead,
		Scope:  domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{inside}},
		Action: "read", Risk: domain.RiskLow,
	}
	decision, err := evaluator.Evaluate(request)
	if err != nil || decision.Decision != domain.PermissionDeny {
		t.Fatalf("expired decision = %#v, %v", decision, err)
	}
	if err := evaluator.AddGrant(domain.PermissionGrant{
		ID: "grant-active", SubjectID: "agent", Capability: domain.CapabilityFilesystemRead,
		Scope:     domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{root}},
		GrantedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = evaluator.Evaluate(request)
	if err != nil || decision.Decision != domain.PermissionAllow {
		t.Fatalf("nested decision = %#v, %v", decision, err)
	}
	sibling := filepath.Join(t.TempDir(), "sibling")
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	request.Scope.Values = []string{sibling}
	decision, err = evaluator.Evaluate(request)
	if err != nil || decision.Decision != domain.PermissionDeny {
		t.Fatalf("sibling decision = %#v, %v", decision, err)
	}
}

func TestPolicyEvaluatorAuthorizesMissingWriteLeafOnlyWithWriteGrant(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	target := filepath.Join(root, "new.txt")
	evaluator := NewPolicyEvaluator(
		WithPolicyClock(domain.FixedClock{At: now}),
		WithPolicyGrant(domain.PermissionGrant{
			ID: "read-only", SubjectID: "agent", Capability: domain.CapabilityFilesystemRead,
			Scope: domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{root}}, GrantedAt: now,
		}),
	)
	request := domain.PermissionRequest{
		SubjectID: "agent", Capability: domain.CapabilityFilesystemWrite,
		Scope:  domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{target}},
		Action: "filesystem.create " + target, Risk: domain.RiskMedium,
	}
	decision, err := evaluator.Evaluate(request)
	if err != nil || decision.Decision != domain.PermissionDeny {
		t.Fatalf("read grant covered write = %#v, %v", decision, err)
	}
	if err := evaluator.AddGrant(domain.PermissionGrant{
		ID: "write", SubjectID: "agent", Capability: domain.CapabilityFilesystemWrite,
		Scope: domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{root}}, GrantedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = evaluator.Evaluate(request)
	if err != nil || decision.Decision != domain.PermissionNeedsApproval {
		t.Fatalf("missing-leaf write decision = %#v, %v", decision, err)
	}
}

func TestPathAllowlistRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	insideFile := filepath.Join(root, "inside.txt")
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(insideFile, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	allowlist, err := NewPathAllowlist([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := allowlist.Resolve(insideFile)
	if err != nil {
		t.Fatalf("inside resolve = %q, %v", resolved, err)
	}
	if !filepath.IsAbs(resolved) || filepath.Base(resolved) != "inside.txt" {
		t.Fatalf("inside resolve = %q", resolved)
	}
	if _, err := allowlist.Resolve(link); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("symlink resolve error = %v, want path not allowed", err)
	}
	if _, err := allowlist.Resolve("relative.txt"); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("relative resolve error = %v, want path not allowed", err)
	}
}
