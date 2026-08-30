package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	"github.com/OrdoAI/yuri-agent/internal/security"
	builtintools "github.com/OrdoAI/yuri-agent/internal/tools"
)

const coreAgentSubjectID = domain.ID("yuri-core-agent")

func (b *Bridge) chatTools(_ time.Time) (*agent.ToolRegistry, error) {
	registry := agent.NewToolRegistry()
	b.mu.RLock()
	supervisors := make(map[string]*plugins.Supervisor, len(b.pluginSupervisors))
	for id, supervisor := range b.pluginSupervisors {
		supervisors[id] = supervisor
	}
	webSearchConfig := b.config.WebSearch
	b.mu.RUnlock()

	// Filesystem tools are always discoverable. Their adapters read the current
	// allowlist immediately before execution, so an agent can request a narrowly
	// scoped permission even when onboarding did not pre-authorize any folder.
	if err := registry.Register(filesystemAgentTool{bridge: b}); err != nil {
		return nil, err
	}
	if err := registry.Register(filesystemWriteAgentTool{bridge: b}); err != nil {
		return nil, err
	}
	webFetch, err := newWebFetch()
	if err != nil {
		return nil, err
	}
	if err := registry.Register(webFetchAgentTool{tool: webFetch}); err != nil {
		return nil, err
	}
	if webSearchConfig.Enabled {
		webSearch, err := newWebSearch(webSearchConfig)
		if err != nil {
			return nil, err
		}
		if err := registry.Register(webSearchAgentTool{tool: webSearch, defaultLimit: webSearchConfig.DefaultResultLimit}); err != nil {
			return nil, err
		}
	}
	if b.scheduler != nil {
		if err := registry.Register(scheduleAgentTool{bridge: b}); err != nil {
			return nil, err
		}
	}
	for pluginID, supervisor := range supervisors {
		state, _ := supervisor.State()
		if state != plugins.StateRunning {
			continue
		}
		manifest := supervisor.Manifest()
		for _, declaration := range manifest.Tools {
			if err := registry.Register(pluginAgentTool{pluginID: pluginID, declaration: declaration, supervisor: supervisor}); err != nil {
				return nil, err
			}
		}
	}
	return registry, nil
}

func filesystemPolicy(roots []string, capability domain.Capability) (domain.PolicyEngine, error) {
	grantID, err := domain.NewID("grant")
	if err != nil {
		return nil, err
	}
	return security.NewPolicyEvaluator(security.WithPolicyGrants([]domain.PermissionGrant{{
		ID: grantID, SubjectID: coreAgentSubjectID, Capability: capability,
		Scope: domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: roots}, GrantedAt: time.Now().UTC(),
	}})), nil
}

func newReadFilesystem(roots []string) (*builtintools.ReadOnlyFilesystemTool, error) {
	policy, err := filesystemPolicy(roots, domain.CapabilityFilesystemRead)
	if err != nil {
		return nil, err
	}
	return builtintools.NewReadOnlyFilesystem(builtintools.ReadOnlyFilesystemConfig{
		Roots: roots, Policy: policy, SubjectID: coreAgentSubjectID,
	})
}

func newWriteFilesystem(roots []string) (*builtintools.WriteFilesystemTool, error) {
	policy, err := filesystemPolicy(roots, domain.CapabilityFilesystemWrite)
	if err != nil {
		return nil, err
	}
	return builtintools.NewWriteFilesystem(builtintools.WriteFilesystemConfig{
		Roots: roots, Policy: policy, SubjectID: coreAgentSubjectID,
	})
}

func newWebFetch() (*builtintools.WebFetchTool, error) {
	grantID, err := domain.NewID("grant")
	if err != nil {
		return nil, err
	}
	policy := security.NewPolicyEvaluator(security.WithPolicyGrants([]domain.PermissionGrant{{
		ID: grantID, SubjectID: coreAgentSubjectID, Capability: domain.CapabilityNetworkHTTP,
		Scope: domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{"*"}}, GrantedAt: time.Now().UTC(),
	}}))
	return builtintools.NewWebFetch(builtintools.WebFetchConfig{Policy: policy, SubjectID: coreAgentSubjectID})
}

func newWebSearch(value config.WebSearchConfig) (*builtintools.WebSearchTool, error) {
	provider, err := builtintools.NewSearXNGProvider(builtintools.SearXNGConfig{Endpoint: value.Endpoint})
	if err != nil {
		return nil, err
	}
	return builtintools.NewWebSearch(provider)
}

type filesystemAgentTool struct{ bridge *Bridge }
type filesystemWriteAgentTool struct{ bridge *Bridge }
type webFetchAgentTool struct{ tool *builtintools.WebFetchTool }
type webSearchAgentTool struct {
	tool         *builtintools.WebSearchTool
	defaultLimit int
}

func (webSearchAgentTool) Descriptor() agent.ToolDescriptor {
	definition := (&builtintools.WebSearchTool{}).Definition()
	schema, _ := json.Marshal(definition.InputSchema)
	return agent.ToolDescriptor{
		Name: definition.ID, Description: definition.Description, InputSchema: schema,
		Risk: definition.Risk, Capabilities: domain.CapabilitySet(definition.Capabilities),
	}
}

func (adapter webSearchAgentTool) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	if adapter.tool == nil {
		return agent.ToolResult{}, fmt.Errorf("web search tool is unavailable")
	}
	var request builtintools.WebSearchRequest
	if err := json.Unmarshal(call.Arguments, &request); err != nil {
		return agent.ToolResult{}, fmt.Errorf("decode web search request: %w", err)
	}
	if request.Limit == 0 {
		request.Limit = adapter.defaultLimit
	}
	result, err := adapter.tool.Execute(ctx, request)
	if err != nil {
		return agent.ToolResult{}, err
	}
	content, err := json.Marshal(result)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("encode web search result: %w", err)
	}
	return agent.ToolResult{Content: string(content)}, nil
}

func (webFetchAgentTool) Descriptor() agent.ToolDescriptor {
	definition := (&builtintools.WebFetchTool{}).Definition()
	schema, _ := json.Marshal(definition.InputSchema)
	return agent.ToolDescriptor{
		Name: definition.ID, Description: definition.Description, InputSchema: schema,
		Risk: definition.Risk, Capabilities: domain.CapabilitySet(definition.Capabilities),
	}
}

func (adapter webFetchAgentTool) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	if adapter.tool == nil {
		return agent.ToolResult{}, fmt.Errorf("web fetch tool is unavailable")
	}
	var request builtintools.WebFetchRequest
	if err := json.Unmarshal(call.Arguments, &request); err != nil {
		return agent.ToolResult{}, fmt.Errorf("decode web fetch request: %w", err)
	}
	result, err := adapter.tool.Execute(ctx, request)
	if err != nil {
		return agent.ToolResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("encode web fetch result: %w", err)
	}
	return agent.ToolResult{Content: string(encoded)}, nil
}

func (filesystemWriteAgentTool) Descriptor() agent.ToolDescriptor {
	definition := (&builtintools.WriteFilesystemTool{}).Definition()
	schema, _ := json.Marshal(definition.InputSchema)
	return agent.ToolDescriptor{
		Name: definition.ID, Description: definition.Description, InputSchema: schema,
		Risk: definition.Risk, Capabilities: domain.CapabilitySet(definition.Capabilities),
	}
}

func (adapter filesystemWriteAgentTool) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	return adapter.execute(ctx, call, false)
}

func (adapter filesystemWriteAgentTool) ExecuteApproved(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	return adapter.execute(ctx, call, true)
}

func (adapter filesystemWriteAgentTool) execute(ctx context.Context, call agent.ToolCall, approved bool) (agent.ToolResult, error) {
	if adapter.bridge == nil {
		return agent.ToolResult{}, fmt.Errorf("filesystem bridge is unavailable")
	}
	var request builtintools.WriteRequest
	if err := json.Unmarshal(call.Arguments, &request); err != nil {
		return agent.ToolResult{}, fmt.Errorf("decode filesystem write request: %w", err)
	}
	roots := adapter.bridge.AllowedDirectories()
	if approved {
		var err error
		roots, err = adapter.bridge.approvedFilesystemRoots(ctx, call, roots)
		if err != nil {
			return agent.ToolResult{}, err
		}
	}
	if len(roots) == 0 {
		return agent.ToolResult{}, fmt.Errorf("%w: filesystem access has not been granted", domain.ErrNotPermitted)
	}
	tool, err := newWriteFilesystem(roots)
	if err != nil {
		return agent.ToolResult{}, err
	}
	var result builtintools.WriteResult
	if approved {
		result, err = tool.ExecuteApproved(ctx, request)
	} else {
		result, err = tool.Execute(ctx, request)
	}
	if err != nil {
		return agent.ToolResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("encode filesystem write result: %w", err)
	}
	return agent.ToolResult{Content: string(encoded)}, nil
}

func (filesystemAgentTool) Descriptor() agent.ToolDescriptor {
	definition := (&builtintools.ReadOnlyFilesystemTool{}).Definition()
	schema, _ := json.Marshal(definition.InputSchema)
	return agent.ToolDescriptor{
		Name: definition.ID, Description: definition.Description, InputSchema: schema,
		Risk: definition.Risk, Capabilities: domain.CapabilitySet(definition.Capabilities),
	}
}

func (adapter filesystemAgentTool) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	return adapter.execute(ctx, call, false)
}

func (adapter filesystemAgentTool) ExecuteApproved(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	return adapter.execute(ctx, call, true)
}

func (adapter filesystemAgentTool) execute(ctx context.Context, call agent.ToolCall, approved bool) (agent.ToolResult, error) {
	if adapter.bridge == nil {
		return agent.ToolResult{}, fmt.Errorf("filesystem bridge is unavailable")
	}
	var request builtintools.ReadRequest
	if err := json.Unmarshal(call.Arguments, &request); err != nil {
		return agent.ToolResult{}, fmt.Errorf("decode filesystem request: %w", err)
	}
	roots := adapter.bridge.AllowedDirectories()
	if approved {
		var err error
		roots, err = adapter.bridge.approvedFilesystemRoots(ctx, call, roots)
		if err != nil {
			return agent.ToolResult{}, err
		}
	}
	if len(roots) == 0 {
		return agent.ToolResult{}, fmt.Errorf("%w: filesystem access has not been granted", domain.ErrNotPermitted)
	}
	tool, err := newReadFilesystem(roots)
	if err != nil {
		return agent.ToolResult{}, err
	}
	result, err := tool.Execute(ctx, request)
	if err != nil {
		return agent.ToolResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("encode filesystem result: %w", err)
	}
	return agent.ToolResult{Content: string(encoded)}, nil
}

func appendUniqueRoot(roots []string, root string) []string {
	for _, existing := range roots {
		if existing == root {
			return roots
		}
	}
	return append(append([]string(nil), roots...), root)
}

func (b *Bridge) approvedFilesystemRoots(ctx context.Context, call agent.ToolCall, roots []string) ([]string, error) {
	access, err := filesystemAccessForRoots(call, roots)
	if err != nil {
		return nil, err
	}
	if access.Allowed {
		return roots, nil
	}
	runID, ok := agent.ApprovedRunID(ctx)
	if !ok || b == nil || b.repositories == nil {
		return nil, fmt.Errorf("%w: approved filesystem scope is unavailable", domain.ErrNotPermitted)
	}
	record, err := b.repositories.Approvals.Get(ctx, approvalIDFor(runID, call.ID))
	if err != nil {
		return nil, fmt.Errorf("load filesystem approval: %w", err)
	}
	if record.Decision != domain.ApprovalApproved || record.Scope.Kind != domain.ScopeFilesystem || len(record.Scope.Values) != 1 {
		return nil, fmt.Errorf("%w: filesystem approval scope is invalid", domain.ErrNotPermitted)
	}
	permissionRoot := record.Scope.Values[0]
	info, err := os.Stat(permissionRoot)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: approved filesystem directory is unavailable", domain.ErrNotPermitted)
	}
	return appendUniqueRoot(roots, permissionRoot), nil
}
