package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFrameRoundTripAndLimits(t *testing.T) {
	request, err := NewRequest(MessageHealth, HealthRequest{Probe: "startup"})
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := Encode(&wire, request); err != nil {
		t.Fatal(err)
	}
	decoded, err := NewFrameReader(&wire, 1024).Read()
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Type != MessageHealth || decoded.ID != request.ID {
		t.Fatalf("decoded = %#v", decoded)
	}
	if _, err := NewFrameReader(strings.NewReader(strings.Repeat("x", 2048)), 1024).Read(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame error = %v, want ErrFrameTooLarge", err)
	}
	if _, err := NewFrameReader(strings.NewReader("\n"), 1024).Read(); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("blank frame error = %v, want ErrInvalidFrame", err)
	}
	if _, err := NewFrameReader(strings.NewReader("{}\n"), 1024).Read(); !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("invalid envelope error = %v, want ErrProtocolMismatch", err)
	}
	if _, err := NewFrameReader(strings.NewReader("{}"), 1024).Read(); !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("invalid EOF envelope error = %v, want ErrProtocolMismatch", err)
	}
	if _, err := NewFrameReader(strings.NewReader("{\"protocol\":\"yuri.plugin.v1\",\"type\":\"health\",\"id\":\"x\",\"payload\":{\"bad\":}\n"), 1024).Read(); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("malformed JSON error = %v, want ErrInvalidFrame", err)
	}
}

func TestServerLifecycleAndToolInvocation(t *testing.T) {
	manifest := validTestManifest()
	var called bool
	server, err := NewServer(manifest, ToolHandlerFunc(func(ctx context.Context, request ToolInvokeRequest, grants []PermissionGrant) (ToolResult, error) {
		called = true
		if !PermissionGranted(grants, CapabilityNetworkHTTP) {
			return ToolResult{}, errors.New("expected network grant")
		}
		var input map[string]any
		if err := json.Unmarshal(request.Arguments, &input); err != nil {
			return ToolResult{}, err
		}
		output, _ := json.Marshal(map[string]any{"echo": input["message"]})
		return ToolResult{OK: true, Output: output}, nil
	}), ServerOptions{ToolTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]Envelope, 0, 5)
	handshake, _ := NewRequest(MessageHandshake, HandshakeRequest{
		CoreVersion: "0.3.0", ProtocolVersion: ProtocolVersion, PluginID: manifest.ID,
		Grants: []PermissionGrant{{Capability: CapabilityNetworkHTTP, Scope: json.RawMessage(`{"hosts":["example.com"]}`)}},
	})
	health, _ := NewRequest(MessageHealth, HealthRequest{})
	invoke, _ := NewRequest(MessageToolInvoke, ToolInvokeRequest{ToolID: "echo", Arguments: json.RawMessage(`{"message":"hello"}`)})
	shutdown, _ := NewRequest(MessageShutdown, ShutdownRequest{Reason: "test"})
	requests = append(requests, handshake, health, invoke, shutdown)
	var input bytes.Buffer
	for _, request := range requests {
		if err := Encode(&input, request); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	reader := NewFrameReader(&output, MaxFrameBytes)
	responses := make([]Envelope, 0, 4)
	for {
		response, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	if len(responses) != 4 {
		t.Fatalf("response count = %d, want 4", len(responses))
	}
	if responses[0].Type != MessageHandshakeResult || responses[1].Type != MessageHealthResult || responses[2].Type != MessageToolResult || responses[3].Type != MessageShutdownResult {
		t.Fatalf("response types = %v, %v, %v, %v", responses[0].Type, responses[1].Type, responses[2].Type, responses[3].Type)
	}
	var result ToolResult
	if err := DecodePayload(responses[2], &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || !called || string(result.Output) != `{"echo":"hello"}` {
		t.Fatalf("tool result = %#v, called=%v", result, called)
	}
	if err := server.EmitEvent(context.Background(), Event{Source: "updates", EventType: "changed", Payload: json.RawMessage(`{"id":2}`)}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("EmitEvent() after shutdown = %v, want ErrNotReady", err)
	}
}

func TestServerRejectsToolBeforeHandshake(t *testing.T) {
	server, err := NewServer(validTestManifest(), ToolHandlerFunc(func(context.Context, ToolInvokeRequest, []PermissionGrant) (ToolResult, error) {
		return ToolResult{OK: true, Output: json.RawMessage(`{}`)}, nil
	}), ServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := NewRequest(MessageToolInvoke, ToolInvokeRequest{ToolID: "echo", Arguments: json.RawMessage(`{}`)})
	var input bytes.Buffer
	_ = Encode(&input, request)
	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	response, err := NewFrameReader(&output, MaxFrameBytes).Read()
	if err != nil {
		t.Fatal(err)
	}
	if response.Type != MessageError || response.Error == nil || response.Error.Code != "not_ready" {
		t.Fatalf("response = %#v", response)
	}
}

func TestServerEventRequiresDeclaration(t *testing.T) {
	manifest := validTestManifest()
	manifest.Events = []EventDefinition{{ID: "updates", Name: "Updates", Schema: json.RawMessage(`{"type":"object"}`)}}
	server, err := NewServer(manifest, ToolHandlerFunc(func(context.Context, ToolInvokeRequest, []PermissionGrant) (ToolResult, error) {
		return ToolResult{OK: true, Output: json.RawMessage(`{}`)}, nil
	}), ServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	handshake, _ := NewRequest(MessageHandshake, HandshakeRequest{ProtocolVersion: ProtocolVersion, PluginID: manifest.ID})
	_ = Encode(&input, handshake)
	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	if err := server.EmitEvent(context.Background(), Event{Source: "undeclared", EventType: "test", Payload: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("EmitEvent() unexpectedly succeeded")
	}
	if err := server.EmitEvent(context.Background(), Event{Source: "updates", EventType: "changed", Payload: json.RawMessage(`{"id":1}`)}); err != nil {
		t.Fatalf("EmitEvent() declared source error = %v", err)
	}
	frames := NewFrameReader(bytes.NewReader(output.Bytes()), MaxFrameBytes)
	var message Envelope
	for i := 0; i < 2; i++ {
		message, err = frames.Read()
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && message.Type != MessageEvent {
			t.Fatalf("event message type = %q", message.Type)
		}
	}
}
