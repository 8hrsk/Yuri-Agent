package security

import (
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func networkScope(values ...string) domain.CapabilityScope {
	return domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: values}
}

// N-9. Before unification this repository carried two functions named
// scopeCovers with the same job and different answers. The case below is the
// exact input on which they disagreed:
//
//	internal/security/policy.go             said "example.com" covered "api.example.com"
//	internal/desktop/plugins.go (authz)     said it did not
//
// The unified rule is the fail-closed one. A bare host is a host, not a tree.
func TestNetworkScopeBareHostDoesNotCoverSubdomains(t *testing.T) {
	if NetworkScopeCovers("example.com", "api.example.com") {
		t.Fatal("a bare host grant must not cover a subdomain")
	}
	if !NetworkScopeCovers("example.com", "example.com") {
		t.Fatal("a bare host grant must cover itself")
	}
	if !NetworkScopeCovers("*.example.com", "api.example.com") {
		t.Fatal("an explicit wildcard grant must cover a subdomain")
	}
	if NetworkScopeCovers("*.example.com", "example.com") {
		t.Fatal("a wildcard grant must not cover the bare parent host")
	}
	if NetworkScopeCovers("*.example.com", "example.com.evil.test") {
		t.Fatal("a wildcard grant must not cover a suffix impostor")
	}
	if NetworkScopeCovers("example.com", "notexample.com") {
		t.Fatal("a bare host grant must not cover a host that merely ends with it")
	}
}

// The same disagreement, expressed at the level callers actually use.
func TestScopeCoversBareHostIsExact(t *testing.T) {
	if ScopeCovers(networkScope("example.com"), networkScope("api.example.com")) {
		t.Fatal("a bare host grant must not cover a subdomain request")
	}
	if !ScopeCovers(networkScope("*.example.com"), networkScope("api.example.com", "cdn.example.com")) {
		t.Fatal("a wildcard grant must cover every requested subdomain")
	}
	if ScopeCovers(networkScope("*.example.com"), networkScope("api.example.com", "other.test")) {
		t.Fatal("one uncovered value must fail the whole request")
	}
}

// Normalization must not undo the rule. The old policy evaluator stripped a
// leading "*." from both sides before comparing, which is precisely how a
// wildcard grant and a bare host grant became the same thing there.
func TestNetworkScopeNormalizationKeepsTheWildcardMeaningful(t *testing.T) {
	if NetworkScopeCovers("EXAMPLE.com.", "example.com") != true {
		t.Fatal("case and a trailing root dot must not change a host")
	}
	if !NetworkScopeCovers("example.com", "https://example.com/path") {
		t.Fatal("a requested URL must be reduced to its host")
	}
	if NetworkScopeCovers("example.com", "https://api.example.com/path") {
		t.Fatal("reducing a URL to its host must not smuggle in subdomain coverage")
	}
}

// N-8 negative control. A scope *value* of "*" is a real any-host grant, so it
// must keep matching; the property N-8 fixed is that such a grant cannot be
// created without the owner's explicit unrestricted confirmation, which is
// what ScopeHasWildcardValue reports and the consent layer enforces. If this
// test ever passes while the consent gate is gone, the exploit is back.
func TestWildcardValueIsRecognizedAsUnrestricted(t *testing.T) {
	if !ScopeHasWildcardValue(networkScope("*")) {
		t.Fatal(`a bare "*" network value must be reported as unrestricted`)
	}
	if !ScopeHasWildcardValue(domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"*"}}) {
		t.Fatal(`a bare "*" resource value must be reported as unrestricted`)
	}
	if ScopeHasWildcardValue(networkScope("*.example.com")) {
		t.Fatal("a subdomain wildcard is a bounded scope, not an unrestricted one")
	}
	if !NetworkScopeCovers("*", "anything.test") {
		t.Fatal(`"*" is an explicit any-host grant and must cover any host`)
	}
}

// N-8, the other half: "*" is not a universal escape hatch for every kind. A
// resource named "*" is a resource, and an unrecognized kind matches nothing.
func TestScopeValueCoversFailsClosed(t *testing.T) {
	if ScopeValueCovers(domain.ScopeResource, "*", "calendar.write") {
		t.Fatal(`a literal "*" resource grant must not cover an unrelated resource`)
	}
	if ScopeValueCovers(domain.ScopeKind("device"), "camera", "camera") {
		t.Fatal("an unrecognized scope kind must fail closed")
	}
	if ScopeValueCovers(domain.ScopeNetwork, "", "example.com") ||
		ScopeValueCovers(domain.ScopeNetwork, "example.com", "  ") {
		t.Fatal("an empty scope value must never match")
	}
}

func TestScopeCoversStructuralRules(t *testing.T) {
	if !ScopeCovers(domain.UnrestrictedScope(), networkScope("example.com")) {
		t.Fatal("an unrestricted grant covers any request")
	}
	if ScopeCovers(networkScope("example.com"), domain.UnrestrictedScope()) {
		t.Fatal("a scoped grant must not satisfy an unrestricted request")
	}
	if ScopeCovers(networkScope("example.com"), domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"example.com"}}) {
		t.Fatal("scope kinds must match")
	}
	// A scope of kind "unrestricted" that also carries values is malformed.
	// Reading it as the broadest possible grant would let a malformed scope
	// win; it must fail closed instead.
	malformed := domain.CapabilityScope{Kind: domain.ScopeUnrestricted, Values: []string{"example.com"}}
	if ScopeCovers(malformed, networkScope("evil.test")) {
		t.Fatal("a malformed unrestricted scope must not cover anything")
	}
}

func TestFilesystemScopeCoversIsAPrefixBoundary(t *testing.T) {
	if !FilesystemScopeCovers("/tmp/project", "/tmp/project/notes.md") {
		t.Fatal("a path inside the granted directory must be covered")
	}
	if !FilesystemScopeCovers("/tmp/project", "/tmp/project") {
		t.Fatal("the granted directory itself must be covered")
	}
	if FilesystemScopeCovers("/tmp/project", "/tmp") {
		t.Fatal("a narrower grant must not satisfy a broader request")
	}
	if FilesystemScopeCovers("/tmp/project", "/tmp/project-other/x") {
		t.Fatal("a sibling whose name merely starts with the grant must not be covered")
	}
}
