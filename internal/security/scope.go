package security

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// This file is the single definition of what a capability grant covers.
//
// N-9: the rule used to exist twice — once in this package's policy evaluator
// and once in the desktop plugin authorizer — and the two disagreed about the
// most consequential case, a bare hostname. The policy evaluator treated a
// grant of "example.com" as also covering "api.example.com"; the plugin
// authorizer treated it as that host and nothing else. The same grant string
// therefore meant two different things depending on which evaluator read it,
// so an owner could approve a scope under one mental model and have it
// enforced under the other.
//
// The unified rule is the fail-closed one: a bare host covers exactly itself,
// and subdomains require an explicit "*." prefix. Every layer now calls these
// functions; nothing re-implements them.

// ScopeCovers reports whether the granted scope permits everything the
// requested scope asks for.
//
// Both scopes must be well formed. An unrestricted grant covers anything; an
// unrestricted *request* is satisfied only by an unrestricted grant. Otherwise
// the kinds must match and every requested value must be covered by at least
// one granted value.
//
// Validity is checked before the unrestricted shortcut on purpose: a scope of
// kind "unrestricted" that also carries values is malformed, and a malformed
// scope must not be read as the broadest possible grant.
func ScopeCovers(granted, requested domain.CapabilityScope) bool {
	if !granted.Valid() || !requested.Valid() {
		return false
	}
	if granted.Kind == domain.ScopeUnrestricted {
		return true
	}
	if requested.Kind == domain.ScopeUnrestricted || granted.Kind != requested.Kind {
		return false
	}
	if len(granted.Values) == 0 || len(requested.Values) == 0 {
		return false
	}
	for _, requestedValue := range requested.Values {
		covered := false
		for _, grantedValue := range granted.Values {
			if ScopeValueCovers(granted.Kind, grantedValue, requestedValue) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// ScopeValueCovers compares a single granted value against a single requested
// value for a given scope kind.
//
// N-8: an unrecognized kind returns false rather than falling through to any
// permissive branch. A scope kind nobody has taught this function about is not
// a licence to match anything.
func ScopeValueCovers(kind domain.ScopeKind, granted, requested string) bool {
	granted = strings.TrimSpace(granted)
	requested = strings.TrimSpace(requested)
	if granted == "" || requested == "" {
		return false
	}
	switch kind {
	case domain.ScopeFilesystem:
		return FilesystemScopeCovers(granted, requested)
	case domain.ScopeNetwork:
		return NetworkScopeCovers(granted, requested)
	case domain.ScopeResource:
		// A resource is an opaque identifier and is compared exactly. There is
		// deliberately no wildcard: "*" is a resource named "*", not a licence
		// covering every resource.
		return granted == requested
	default:
		return false
	}
}

// FilesystemScopeCovers treats a granted path as a prefix boundary. It is a
// lexical check: callers that need symlink-aware containment must canonicalize
// both sides first (see canonicalFilesystemScope), and the filesystem tools
// perform their own resolution again immediately before touching anything.
func FilesystemScopeCovers(granted, requested string) bool {
	grantedPath := filepath.Clean(strings.TrimSpace(granted))
	requestedPath := filepath.Clean(strings.TrimSpace(requested))
	if grantedPath == "" || requestedPath == "" || grantedPath == "." || requestedPath == "." {
		return false
	}
	if grantedPath == requestedPath {
		return true
	}
	separator := string(filepath.Separator)
	if !strings.HasSuffix(grantedPath, separator) {
		grantedPath += separator
	}
	return strings.HasPrefix(requestedPath, grantedPath)
}

// NetworkScopeCovers is the unified host rule.
//
//	"example.com"    covers example.com and nothing else
//	"*.example.com"  covers api.example.com but neither example.com itself nor
//	                 an impostor such as example.com.evil.test
//	"*"              covers every host — an explicit any-host grant, which the
//	                 consent layer gates behind the same confirmation as scope
//	                 kind "unrestricted" (see ScopeHasWildcardValue)
//
// A bare host deliberately does not cover its subdomains. Convenience argues
// the other way, but least privilege wins: an owner who wants api.example.com
// can name it, or name "*.example.com" and mean it.
func NetworkScopeCovers(granted, requested string) bool {
	grantedHost := normalizeNetworkScopeValue(granted)
	requestedHost := normalizeNetworkScopeValue(requested)
	if grantedHost == "" || requestedHost == "" {
		return false
	}
	if grantedHost == "*" {
		return true
	}
	if strings.HasPrefix(grantedHost, "*.") {
		suffix := grantedHost[1:]
		return strings.HasSuffix(requestedHost, suffix) && len(requestedHost) > len(suffix)
	}
	return grantedHost == requestedHost
}

// normalizeNetworkScopeValue lowercases a scope value, drops a trailing root
// dot, and reduces a full URL to its host. It deliberately does NOT strip a
// leading "*.": that prefix is the whole difference between a host grant and a
// subdomain grant, and stripping it is exactly the bug N-9 records.
func normalizeNetworkScopeValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
		value = parsed.Hostname()
	}
	return strings.TrimSuffix(value, ".")
}

// ScopeHasWildcardValue reports whether a scope carries a bare "*" value. A
// declaration that does is asking for an unbounded grant while looking like a
// narrow one: for a network scope "*" literally is every host, and for every
// other kind both the plugin author writing it and the owner reading it mean
// "everything". Such a consent therefore passes through the same explicit
// confirmation as scope kind "unrestricted" instead of slipping past it.
//
// This is the N-8 gate. Removing it would let a grant whose *kind* is not
// "unrestricted" obtain unrestricted reach without the owner confirming it.
func ScopeHasWildcardValue(scope domain.CapabilityScope) bool {
	for _, value := range scope.Values {
		if strings.TrimSpace(value) == "*" {
			return true
		}
	}
	return false
}
