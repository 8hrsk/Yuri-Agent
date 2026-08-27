// Command yuri-reference is the small reference plugin used by Stage 3
// integration tests and by plugin authors as a starting point.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	plugin "github.com/OrdoAI/yuri-agent/sdk/plugin-go"
)

var manifest = plugin.Manifest{
	SchemaVersion:   plugin.ManifestSchemaVersion,
	ID:              "yuri.reference-echo",
	Name:            "Yuri Reference Echo",
	Version:         "0.1.0",
	Publisher:       "OrdoAI",
	Description:     "A minimal out-of-process plugin that echoes a message.",
	Executable:      "bin/yuri-reference",
	SupportedOS:     []string{"darwin", "windows", "linux"},
	SupportedArch:   []string{"amd64", "arm64"},
	ProtocolVersion: plugin.ProtocolVersion,
	MinCoreVersion:  "0.1.0",
	MaxCoreVersion:  "0.4.0",
	Tools: []plugin.ToolDefinition{
		{
			ID:          "echo",
			Name:        "Echo",
			Description: "Returns the supplied message without performing a side effect.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["message"],
  "properties": {"message": {"type": "string", "maxLength": 4096}}
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["message"],
  "properties": {"message": {"type": "string"}}
}`),
			Risk: plugin.RiskLow,
		},
	},
	Events:      []plugin.EventDefinition{},
	Permissions: []plugin.Permission{},
}

func main() {
	server, err := plugin.NewServer(manifest, plugin.ToolHandlerFunc(echo), plugin.ServerOptions{
		Logger: func(format string, args ...any) {
			_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
		},
	})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func echo(ctx context.Context, request plugin.ToolInvokeRequest, _ []plugin.PermissionGrant) (plugin.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return plugin.ToolResult{}, err
	}
	var input struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(request.Arguments, &input); err != nil {
		return plugin.ToolResult{}, fmt.Errorf("decode echo arguments: %w", err)
	}
	if input.Message == "" {
		return plugin.ToolResult{}, errors.New("message is required")
	}
	output, err := json.Marshal(struct {
		Message string `json:"message"`
	}{Message: input.Message})
	if err != nil {
		return plugin.ToolResult{}, fmt.Errorf("encode echo result: %w", err)
	}
	return plugin.ToolResult{OK: true, Output: output}, nil
}
