package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	"github.com/OrdoAI/yuri-agent/internal/security"
	builtintools "github.com/OrdoAI/yuri-agent/internal/tools"
)

func (b *Bridge) chatTools(now time.Time) (*agent.ToolRegistry, error) {
	registry := agent.NewToolRegistry()
	b.mu.RLock()
	roots := append([]string(nil), b.config.AllowedDirectories...)
	supervisors := make(map[string]*plugins.Supervisor, len(b.pluginSupervisors))
	for id, supervisor := range b.pluginSupervisors {
		supervisors[id] = supervisor
	}
	b.mu.RUnlock()
	if len(roots) > 0 {
		subjectID := domain.ID("yuri-core-agent")
		readGrantID, err := domain.NewID("grant")
		if err != nil {
			return nil, err
		}
		writeGrantID, err := domain.NewID("grant")
		if err != nil {
			return nil, err
		}
		policy := security.NewPolicyEvaluator(security.WithPolicyGrants([]domain.PermissionGrant{
			{
				ID: readGrantID, SubjectID: subjectID, Capability: domain.CapabilityFilesystemRead,
				Scope: domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: roots}, GrantedAt: now,
			},
			{
				ID: writeGrantID, SubjectID: subjectID, Capability: domain.CapabilityFilesystemWrite,
				Scope: domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: roots}, GrantedAt: now,
			},
		}))
		filesystem, err := builtintools.NewReadOnlyFilesystem(builtintools.ReadOnlyFilesystemConfig{
			Roots: roots, Policy: policy, SubjectID: subjectID,
		})
		if err != nil {
			return nil, err
		}
		if err := registry.Register(filesystemAgentTool{tool: filesystem}); err != nil {
			return nil, err
		}
		writer, err := builtintools.NewWriteFilesystem(builtintools.WriteFilesystemConfig{
			Roots: roots, Policy: policy, SubjectID: subjectID,
		})
		if err != nil {
			return nil, err
		}
		if err := registry.Register(filesystemWriteAgentTool{tool: writer}); err != nil {
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

type filesystemAgentTool struct {
	tool *builtintools.ReadOnlyFilesystemTool
}

type filesystemWriteAgentTool struct {
	tool *builtintools.WriteFilesystemTool
}

func (adapter filesystemWriteAgentTool) Descriptor() agent.ToolDescriptor {
	definition := adapter.tool.Definition()
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
	var request builtintools.WriteRequest
	if err := json.Unmarshal(call.Arguments, &request); err != nil {
		return agent.ToolResult{}, fmt.Errorf("decode filesystem write request: %w", err)
	}
	var result builtintools.WriteResult
	var err error
	if approved {
		result, err = adapter.tool.ExecuteApproved(ctx, request)
	} else {
		result, err = adapter.tool.Execute(ctx, request)
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

func (adapter filesystemAgentTool) Descriptor() agent.ToolDescriptor {
	definition := adapter.tool.Definition()
	schema, _ := json.Marshal(definition.InputSchema)
	return agent.ToolDescriptor{
		Name: definition.ID, Description: definition.Description, InputSchema: schema,
		Risk: definition.Risk, Capabilities: domain.CapabilitySet(definition.Capabilities),
	}
}

func (adapter filesystemAgentTool) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	var request builtintools.ReadRequest
	if err := json.Unmarshal(call.Arguments, &request); err != nil {
		return agent.ToolResult{}, fmt.Errorf("decode filesystem request: %w", err)
	}
	result, err := adapter.tool.Execute(ctx, request)
	if err != nil {
		return agent.ToolResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("encode filesystem result: %w", err)
	}
	return agent.ToolResult{Content: string(encoded)}, nil
}
