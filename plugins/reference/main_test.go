package main

import (
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
