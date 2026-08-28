package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	plugin "github.com/OrdoAI/yuri-agent/sdk/plugin-go"
)

const requiredCapability = "notifications.send"

var manifest = plugin.Manifest{
	SchemaVersion:   plugin.ManifestSchemaVersion,
	ID:              "test.yuri.crashable",
	Name:            "Yuri Crash Recovery Fixture",
	Version:         "0.1.0",
	Publisher:       "OrdoAI",
	Executable:      "crash-plugin",
	SupportedOS:     []string{"darwin", "linux", "windows"},
	SupportedArch:   []string{"amd64", "arm64"},
	ProtocolVersion: plugin.ProtocolVersion,
	MinCoreVersion:  "0.1.0",
	MaxCoreVersion:  "0.4.0",
	Tools: []plugin.ToolDefinition{{
		ID: "echo", Name: "Echo with grant", Risk: plugin.RiskMedium,
		InputSchema:  json.RawMessage(`{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}`),
		Permissions:  []string{requiredCapability},
	}},
	Permissions: []plugin.Permission{{
		Capability: requiredCapability, Scope: json.RawMessage(`{"kind":"unrestricted"}`),
		Reason: "exercise persisted host grants",
	}},
}

func main() {
	server, err := plugin.NewServer(manifest, plugin.ToolHandlerFunc(invoke), plugin.ServerOptions{})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func invoke(_ context.Context, request plugin.ToolInvokeRequest, grants []plugin.PermissionGrant) (plugin.ToolResult, error) {
	if !plugin.PermissionGranted(grants, requiredCapability) {
		return plugin.ToolResult{}, errors.New("required permission grant was not delivered")
	}
	var input struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(request.Arguments, &input); err != nil {
		return plugin.ToolResult{}, err
	}
	if input.Message == "crash" {
		os.Exit(23)
	}
	output, err := json.Marshal(input)
	if err != nil {
		return plugin.ToolResult{}, err
	}
	return plugin.ToolResult{OK: true, Output: output}, nil
}
