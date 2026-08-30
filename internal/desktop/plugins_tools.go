package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
)

func (b *Bridge) pluginEffectiveGrants(ctx context.Context, id domain.ID) ([]plugins.PermissionGrant, error) {
	grants, err := b.repositories.Plugins.Grants(ctx, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result := make([]plugins.PermissionGrant, 0, len(grants))
	for _, grant := range grants {
		if !grant.ExpiresAt.IsZero() && !now.Before(grant.ExpiresAt) {
			continue
		}
		scope, err := json.Marshal(grant.Scope)
		if err != nil {
			return nil, err
		}
		result = append(result, plugins.PermissionGrant{
			Capability: string(grant.Capability), Scope: scope, ExpiresAt: grant.ExpiresAt,
		})
	}
	return result, nil
}

func pluginDomainScope(raw json.RawMessage) (domain.CapabilityScope, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return domain.UnrestrictedScope(), nil
	}
	var scope domain.CapabilityScope
	if err := json.Unmarshal(raw, &scope); err != nil {
		return domain.CapabilityScope{}, fmt.Errorf("decode capability scope: %w", err)
	}
	if !scope.Valid() {
		return domain.CapabilityScope{}, errors.New("capability scope is invalid")
	}
	return scope, nil
}

type pluginAgentTool struct {
	pluginID    string
	declaration plugins.ToolDeclaration
	supervisor  *plugins.Supervisor
}

func (tool pluginAgentTool) Descriptor() agent.ToolDescriptor {
	capabilities := make(domain.CapabilitySet, 0, len(tool.declaration.Permissions))
	for _, capability := range tool.declaration.Permissions {
		capabilities = append(capabilities, domain.NormalizeCapabilityName(capability))
	}
	return agent.ToolDescriptor{
		Name: pluginToolName(tool.pluginID, tool.declaration.ID), Description: tool.declaration.Description,
		InputSchema: append(json.RawMessage(nil), tool.declaration.InputSchema...),
		Risk:        tool.declaration.Risk, Capabilities: capabilities,
	}
}

func (tool pluginAgentTool) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	result, err := tool.supervisor.InvokeTool(ctx, plugins.ToolInvokeParams{
		ToolID: tool.declaration.ID, Arguments: append(json.RawMessage(nil), call.Arguments...),
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	content := string(result.Output)
	if content == "" {
		content = "{}"
	}
	return agent.ToolResult{Content: content, IsError: !result.OK || result.Error != nil, Metadata: map[string]any{"plugin_id": tool.pluginID}}, nil
}

func pluginToolName(pluginID, toolID string) string {
	sanitize := func(value string, limit int) string {
		var builder strings.Builder
		for _, char := range value {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
				builder.WriteRune(char)
			} else {
				builder.WriteByte('_')
			}
			if builder.Len() >= limit {
				break
			}
		}
		return strings.Trim(builder.String(), "_")
	}
	digest := sha256.Sum256([]byte(pluginID + "\x00" + toolID))
	return "plugin_" + sanitize(pluginID, 20) + "_" + sanitize(toolID, 20) + "_" + hex.EncodeToString(digest[:4])
}
