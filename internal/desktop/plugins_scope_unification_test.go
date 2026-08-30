package desktop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	"github.com/OrdoAI/yuri-agent/internal/security"
)

// N-9. Two functions named scopeCovers used to live in this tree — one here
// and one in internal/security/policy.go — and they disagreed about a bare
// hostname. The policy evaluator read a grant of "example.com" as also
// covering "api.example.com"; this authorizer read it as that host alone. The
// same owner-approved string therefore meant two different things depending on
// which evaluator happened to read it.
//
// There is now one rule, the fail-closed one, and both layers call it. This
// test asserts the unified answer on the exact input the two disagreed on, and
// asserts it through the desktop entry point rather than the shared function,
// so that re-introducing a local copy here would fail it.
func TestPluginAuthorizerUsesTheUnifiedNetworkScopeRule(t *testing.T) {
	bareHost := domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"example.com"}}
	subdomain := domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"api.example.com"}}

	if scopeCovers(bareHost, subdomain) {
		t.Fatal("a bare host grant must not cover a subdomain")
	}
	if !scopeCovers(bareHost, bareHost) {
		t.Fatal("a bare host grant must cover itself")
	}
	wildcard := domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"*.example.com"}}
	if !scopeCovers(wildcard, subdomain) {
		t.Fatal("an explicit wildcard grant must cover a subdomain")
	}
	if scopeCovers(wildcard, bareHost) {
		t.Fatal("a wildcard grant must not cover the bare parent host")
	}

	// The desktop and security layers must not merely agree by coincidence:
	// they must be the same code. If this ever diverges again, one of the two
	// answers below changes and the assertion fires.
	for _, testCase := range []struct {
		granted, requested string
	}{
		{"example.com", "api.example.com"},
		{"example.com", "example.com"},
		{"*.example.com", "api.example.com"},
		{"*.example.com", "example.com"},
		{"*.example.com", "example.com.evil.test"},
		{"*", "anything.test"},
	} {
		grantedScope := domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{testCase.granted}}
		requestedScope := domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{testCase.requested}}
		if scopeCovers(grantedScope, requestedScope) != security.ScopeCovers(grantedScope, requestedScope) {
			t.Fatalf("desktop and security disagree on %q covering %q", testCase.granted, testCase.requested)
		}
	}
}

// N-9, end to end through the owner's consent. A manifest that declares
// "example.com" is asking for that host; an owner reading the consent dialog
// reads it as that host; and the runtime authorizer must enforce that host.
// Under the permissive reading the same grant would have licensed every
// subdomain the plugin cared to name.
func TestPluginNetworkGrantDoesNotReachSubdomains(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	bridge.config.PluginDevMode = true
	packageDirectory := signedPluginPackage(t, root, "", nil, []plugins.PermissionDeclaration{
		{Capability: string(domain.CapabilityNetworkHTTP), Scope: json.RawMessage(`{"kind":"network","values":["example.com"]}`), Reason: "fetch"},
	})
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityNetworkHTTP),
	}}}); err != nil {
		t.Fatal(err)
	}
	authorizer := pluginGrantAuthorizer{repository: bridge.repositories.Plugins}

	allowed, err := authorizer.Authorize(context.Background(), plugins.AuthorizationRequest{
		PluginID: installed.ID, ToolID: "fetch", Capability: string(domain.CapabilityNetworkHTTP),
		Scope: json.RawMessage(`{"kind":"network","values":["example.com"]}`),
	})
	if err != nil || !allowed.Allowed {
		t.Fatalf("the granted host itself = %#v, %v", allowed, err)
	}
	denied, err := authorizer.Authorize(context.Background(), plugins.AuthorizationRequest{
		PluginID: installed.ID, ToolID: "fetch", Capability: string(domain.CapabilityNetworkHTTP),
		Scope: json.RawMessage(`{"kind":"network","values":["api.example.com"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Allowed {
		t.Fatal("a grant of example.com authorized api.example.com")
	}
}

// N-8 exploit, re-run against the unified rule. The original defect: a
// manifest declared {"kind":"network","values":["*"]}, the owner enabled the
// plugin with an ordinary consent, and because the grant's *kind* was not
// "unrestricted" the AllowUnrestricted gate never fired — yet "*" matched
// every host at authorization time. The gate must still fire, and the grant
// that follows an explicit confirmation must still be the unbounded one the
// owner was warned about.
func TestWildcardNetworkValueStillRequiresUnrestrictedConfirmation(t *testing.T) {
	root := t.TempDir()
	bridge := newPluginTestBridge(t, root)
	bridge.config.PluginDevMode = true
	packageDirectory := signedPluginPackage(t, root, "", nil, []plugins.PermissionDeclaration{
		{Capability: string(domain.CapabilityNetworkHTTP), Scope: json.RawMessage(`{"kind":"network","values":["*"]}`), Reason: "fetch"},
	})
	installed, err := bridge.InstallPlugin(PluginPathRequest{Path: packageDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityNetworkHTTP),
	}}}); err == nil {
		t.Fatal(`N-8 exploit succeeded: a "*" network scope was granted without an explicit unrestricted confirmation`)
	}
	if _, err := bridge.EnablePlugin(PluginEnableRequest{ID: installed.ID, Capabilities: []PluginCapabilityConsent{{
		Capability: string(domain.CapabilityNetworkHTTP), AllowUnrestricted: true,
	}}}); err != nil {
		t.Fatalf("confirmed wildcard consent was rejected: %v", err)
	}
	authorizer := pluginGrantAuthorizer{repository: bridge.repositories.Plugins}
	allowed, err := authorizer.Authorize(context.Background(), plugins.AuthorizationRequest{
		PluginID: installed.ID, ToolID: "fetch", Capability: string(domain.CapabilityNetworkHTTP),
		Scope: json.RawMessage(`{"kind":"network","values":["anything.test"]}`),
	})
	if err != nil || !allowed.Allowed {
		t.Fatalf(`a confirmed "*" grant must cover any host: %#v, %v`, allowed, err)
	}
}

// N-8's other half: "*" is not a wildcard for kinds that have no wildcard. An
// owner who confirms an unbounded resource grant gets a resource literally
// named "*", and an unrecognized kind matches nothing at all.
func TestUnifiedRuleStillFailsClosedForResourceAndUnknownKinds(t *testing.T) {
	wildcard := domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"*"}}
	if scopeCovers(wildcard, domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"calendar.write"}}) {
		t.Fatal(`a literal "*" resource grant must not cover an unrelated resource`)
	}
	unknown := domain.CapabilityScope{Kind: domain.ScopeKind("device"), Values: []string{"camera"}}
	if scopeCovers(unknown, unknown) {
		t.Fatal("an unrecognized scope kind must fail closed")
	}
}
