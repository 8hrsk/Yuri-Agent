package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func validTestManifest() Manifest {
	return Manifest{
		SchemaVersion:   ManifestSchemaVersion,
		ID:              "example/echo",
		Name:            "Example Echo",
		Version:         "1.2.3",
		Publisher:       "Example",
		Executable:      "bin/echo",
		SupportedOS:     []string{"darwin"},
		SupportedArch:   []string{"arm64"},
		ProtocolVersion: ProtocolVersion,
		MinCoreVersion:  "0.1.0",
		MaxCoreVersion:  "0.9.0",
		Tools: []ToolDefinition{{
			ID:           "echo",
			Name:         "Echo",
			InputSchema:  json.RawMessage(`{"type":"object"}`),
			OutputSchema: json.RawMessage(`{"type":"object"}`),
			Risk:         RiskLow,
		}},
		Events:      []EventDefinition{},
		Permissions: []Permission{},
	}
}

func TestManifestValidateAcceptsReferenceShape(t *testing.T) {
	manifest := validTestManifest()
	manifest.Repository = &Repository{URL: "https://github.com/example/echo"}
	manifest.ReleaseAssets = []ReleaseAsset{{
		OS: "darwin", Arch: "arm64", URL: "https://github.com/example/echo/releases/download/v1.2.3/echo.tgz", Filename: "echo.tgz", Checksum: strings.Repeat("a", 64),
	}}
	manifest.Checksum = &Checksum{Algorithm: "sha256", Value: strings.Repeat("b", 64)}
	manifest.Signature = &Signature{Algorithm: "ed25519", KeyID: "example-key", Value: "base64-signature"}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	decoded, err := DecodeManifest(encoded)
	if err != nil {
		t.Fatalf("DecodeManifest() error = %v", err)
	}
	if decoded.ID != manifest.ID || decoded.ProtocolVersion != ProtocolVersion {
		t.Fatalf("decoded manifest = %#v", decoded)
	}
}

func TestManifestRejectsUnsafeOrAmbiguousDeclarations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "path traversal", mutate: func(m *Manifest) { m.Executable = "../escape" }},
		{name: "absolute windows path", mutate: func(m *Manifest) { m.Executable = `C:\\escape.exe` }},
		{name: "unknown capability", mutate: func(m *Manifest) { m.Permissions = []Permission{{Capability: "process.exec", Reason: "no"}} }},
		{name: "duplicate tool", mutate: func(m *Manifest) { m.Tools = append(m.Tools, m.Tools[0]) }},
		{name: "unsupported protocol", mutate: func(m *Manifest) { m.ProtocolVersion = "2.0" }},
		{name: "insecure repository", mutate: func(m *Manifest) { m.Repository = &Repository{URL: "http://github.com/example/echo"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validTestManifest()
			test.mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

func TestDecodeManifestRejectsUnknownFields(t *testing.T) {
	data, err := json.Marshal(validTestManifest())
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"unknown":true}`)...)
	if _, err := DecodeManifest(data); err == nil {
		t.Fatal("DecodeManifest() unexpectedly accepted unknown field")
	}
}

func TestDecodeManifestRejectsTrailingValues(t *testing.T) {
	data, err := json.Marshal(validTestManifest())
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte(` {}`)...)
	if _, err := DecodeManifest(data); err == nil {
		t.Fatal("DecodeManifest() unexpectedly accepted trailing JSON value")
	}
}

func TestSHA256IsDeterministic(t *testing.T) {
	if got, want := SHA256([]byte("yuri")), "c309191c26ca712c9eff38a140ba5326750581ae6cb4d6dd873fa8433d5cff7a"; got != want {
		t.Fatalf("SHA256() = %q, want %q", got, want)
	}
	if SHA256([]byte("yuri")) == SHA256([]byte("other")) {
		t.Fatal("SHA256() returned the same value for different inputs")
	}
}
