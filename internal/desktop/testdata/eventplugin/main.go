// Command eventplugin is a test fixture: a plugin that publishes an event on
// demand. It exists so the host's plugin event watcher can be driven with a
// real protocol frame produced by a real child process, which is the only
// faithful way to exercise the one bridge goroutine that consumes untrusted
// third-party input.
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
	ID:              "test.yuri.eventful",
	Name:            "Yuri Event Watcher Fixture",
	Version:         "0.1.0",
	Publisher:       "OrdoAI",
	Executable:      "event-plugin",
	SupportedOS:     []string{"darwin", "linux", "windows"},
	SupportedArch:   []string{"amd64", "arm64"},
	ProtocolVersion: plugin.ProtocolVersion,
	MinCoreVersion:  "0.1.0",
	MaxCoreVersion:  "0.4.0",
	Tools: []plugin.ToolDefinition{{
		ID: "emit", Name: "Publish an event", Risk: plugin.RiskLow,
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"event_type":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["emitted"],"properties":{"emitted":{"type":"boolean"}}}`),
	}},
	Events: []plugin.EventDefinition{{
		ID: "heartbeat", Name: "Heartbeat",
		Schema: json.RawMessage(`{"type":"object"}`),
	}},
}

func main() {
	// The handler needs the server to publish from, and the server needs the
	// handler to be constructed, so the reference is closed over rather than
	// passed in.
	var server *plugin.Server
	handler := plugin.ToolHandlerFunc(func(ctx context.Context, request plugin.ToolInvokeRequest, _ []plugin.PermissionGrant) (plugin.ToolResult, error) {
		return emit(ctx, server, request)
	})
	server, err := plugin.NewServer(manifest, handler, plugin.ServerOptions{})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func emit(ctx context.Context, server *plugin.Server, request plugin.ToolInvokeRequest) (plugin.ToolResult, error) {
	var input struct {
		EventType string `json:"event_type"`
	}
	if len(request.Arguments) > 0 {
		if err := json.Unmarshal(request.Arguments, &input); err != nil {
			return plugin.ToolResult{}, err
		}
	}
	if input.EventType == "" {
		input.EventType = "tick"
	}
	if err := server.EmitEvent(ctx, plugin.Event{
		Source: "heartbeat", EventType: input.EventType,
		Payload: json.RawMessage(`{"note":"published by the event fixture"}`),
	}); err != nil {
		return plugin.ToolResult{}, err
	}
	output, err := json.Marshal(map[string]bool{"emitted": true})
	if err != nil {
		return plugin.ToolResult{}, err
	}
	return plugin.ToolResult{OK: true, Output: output}, nil
}
