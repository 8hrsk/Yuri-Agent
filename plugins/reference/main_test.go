package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	plugin "github.com/OrdoAI/yuri-agent/sdk/plugin-go"
)

func TestReferenceManifestMatchesSDKManifest(t *testing.T) {
	if err := manifest.Validate(); err != nil {
		t.Fatalf("embedded manifest is invalid: %v", err)
	}
	data, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := plugin.DecodeManifest(data)
	if err != nil {
		t.Fatalf("plugin.json is invalid: %v", err)
	}
	if decoded.ID != manifest.ID || decoded.Version != manifest.Version || decoded.Executable != manifest.Executable {
		t.Fatalf("plugin.json %#v does not match embedded manifest %#v", decoded, manifest)
	}
}

func TestEchoReturnsTheMessage(t *testing.T) {
	result, err := echo(context.Background(), plugin.ToolInvokeRequest{
		ToolID: "echo", Arguments: json.RawMessage(`{"message":"hello"}`),
	}, nil)
	if err != nil {
		t.Fatalf("echo() error = %v", err)
	}
	if !result.OK || string(result.Output) != `{"message":"hello"}` {
		t.Fatalf("echo() result = %#v", result)
	}
}

func TestEchoRejectsInvalidInput(t *testing.T) {
	cases := map[string]json.RawMessage{
		"malformed": json.RawMessage(`{"message":`),
		"empty":     json.RawMessage(`{"message":""}`),
		"missing":   json.RawMessage(`{}`),
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := echo(context.Background(), plugin.ToolInvokeRequest{ToolID: "echo", Arguments: arguments}, nil); err == nil {
				t.Fatal("echo() unexpectedly accepted the input")
			}
		})
	}
}

// A cancelled turn reaches the handler as a cancelled context; the reference
// implementation is the example plugin authors copy, so it must observe it.
func TestEchoHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := echo(ctx, plugin.ToolInvokeRequest{ToolID: "echo", Arguments: json.RawMessage(`{"message":"hi"}`)}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("echo() error = %v, want context.Canceled", err)
	}
}
