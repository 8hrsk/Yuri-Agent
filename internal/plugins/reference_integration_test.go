package plugins

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This is the canonical compatibility test between the public SDK/reference
// implementation and the private host runtime. A protocol change must update
// both sides in one commit or this test fails during handshake.
func TestReferencePluginRunsThroughCoreSupervisor(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	manifestContent, err := os.ReadFile(filepath.Join(repositoryRoot, "plugins", "reference", ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	packageDirectory := t.TempDir()
	binDirectory := filepath.Join(packageDirectory, "bin")
	if err := os.MkdirAll(binDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(binDirectory, "yuri-reference")
	command := exec.Command("go", "build", "-o", executable, "./plugins/reference")
	command.Dir = repositoryRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build reference plugin: %v\n%s", err, output)
	}
	if err := os.Chmod(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(SupervisorConfig{
		Manifest: manifest, PackageDir: packageDirectory, CoreVersion: "0.4.0", DevMode: true,
		Client: ClientConfig{CloseTimeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := supervisor.InvokeTool(ctx, ToolInvokeParams{
		ToolID: "echo", Arguments: json.RawMessage(`{"message":"hello from host"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || string(result.Output) != `{"message":"hello from host"}` {
		t.Fatalf("unexpected reference result: %#v", result)
	}
	if err := supervisor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
