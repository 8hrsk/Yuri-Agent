package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func validManifest(executable string) Manifest {
	return Manifest{
		ID:              "example.reference",
		SchemaVersion:   ManifestSchemaVersion,
		Name:            "Reference plugin",
		Version:         "1.0.0",
		Publisher:       "Yuri",
		Executable:      executable,
		SupportedOS:     []string{runtime.GOOS},
		SupportedArch:   []string{runtime.GOARCH},
		ProtocolVersion: ProtocolVersion,
		Tools: []ToolDeclaration{{
			ID:           "echo",
			Name:         "Echo",
			Risk:         domain.RiskLow,
			InputSchema:  []byte(`{"type":"object"}`),
			OutputSchema: []byte(`{"type":"object"}`),
		}},
	}
}

func TestManifestValidateRejectsTraversalAndInvalidDeclarations(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Manifest)
	}{
		{name: "parent traversal", edit: func(m *Manifest) { m.Executable = "../plugin" }},
		{name: "absolute path", edit: func(m *Manifest) { m.Executable = "/tmp/plugin" }},
		{name: "windows absolute path", edit: func(m *Manifest) { m.Executable = `C:\\plugin.exe` }},
		{name: "unknown capability", edit: func(m *Manifest) { m.Tools[0].Permissions = []string{"not-a-capability"} }},
		{name: "missing schema", edit: func(m *Manifest) { m.Tools[0].InputSchema = nil }},
		{name: "wrong protocol", edit: func(m *Manifest) { m.ProtocolVersion = "99" }},
		{name: "critical risk unavailable", edit: func(m *Manifest) { m.Tools[0].Risk = domain.RiskCritical }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest("plugin")
			test.edit(&manifest)
			if err := manifest.Validate(); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Validate() error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestManifestResolveExecutableRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	executable := filepath.Join(root, "plugin")
	outsideExecutable := filepath.Join(outside, "plugin")
	if err := os.WriteFile(outsideExecutable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideExecutable, executable); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	manifest := validManifest("plugin")
	_, err := manifest.ResolveExecutable(root)
	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("ResolveExecutable() error = %v, want ErrPathEscape", err)
	}
}

func TestManifestResolveExecutableAndChecksum(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "plugin")
	contents := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(path, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := validManifest("plugin")
	hash := sha256.Sum256(contents)
	manifest.Checksum = &ChecksumMetadata{Algorithm: "sha256", Value: hex.EncodeToString(hash[:])}
	resolved, err := manifest.ResolveExecutable(root)
	if err != nil {
		t.Fatalf("ResolveExecutable() error = %v", err)
	}
	expected, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != expected {
		t.Fatalf("resolved path = %q, want %q", resolved, expected)
	}
	if err := manifest.VerifyChecksum(root); err != nil {
		t.Fatalf("VerifyChecksum() error = %v", err)
	}
	manifest.Checksum = &ChecksumMetadata{Algorithm: "sha256", Value: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := manifest.VerifyChecksum(root); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("VerifyChecksum() error = %v, want ErrInvalidManifest", err)
	}
}

func TestLoadManifestUsesSchemaCompatibleDocument(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "plugin")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := validManifest("plugin")
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if loaded.ID != manifest.ID || loaded.SchemaVersion != ManifestSchemaVersion {
		t.Fatalf("loaded manifest = %#v", loaded)
	}
}

func TestManifestCoreCompatibility(t *testing.T) {
	manifest := validManifest("plugin")
	manifest.MinCoreVersion = "1.2.0"
	manifest.MaxCoreVersion = "2.0.0"
	if manifest.CompatibleWithCore("1.1.9") || !manifest.CompatibleWithCore("1.5.0") || manifest.CompatibleWithCore("2.1.0") {
		t.Fatal("unexpected core compatibility result")
	}
}
