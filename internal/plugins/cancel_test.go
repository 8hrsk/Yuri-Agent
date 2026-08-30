package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// M-29. Cancelling a turn must reach the plugin as request.cancel and leave a
// responsive plugin running. SIGKILL of the whole process group is the
// escalation for a wedged handler, not the first response to a user pressing
// stop.
//
// This test is written only against API that predates the fix so it can be run
// against the previous revision, where it fails twice over: no cancel frame is
// ever written, and Supervisor.InvokeTool stops the plugin outright.
func TestSupervisorCancelledToolSendsCancelAndKeepsPluginAlive(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "cancel.marker")
	supervisor, cleanup := testSupervisor(t,
		"YURI_PLUGIN_SLOW_TOOL=1",
		"YURI_PLUGIN_CANCEL_MARKER="+marker,
	)
	defer cleanup()

	startCtx, cancelStart := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStart()
	if err := supervisor.Start(startCtx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	invokeCtx, cancelInvoke := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelInvoke()
	done := make(chan error, 1)
	go func() {
		_, err := supervisor.InvokeTool(invokeCtx, ToolInvokeParams{ToolID: "echo", Arguments: json.RawMessage(`{"message":"slow"}`)})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("InvokeTool() error = %v, want context deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("InvokeTool() did not return after its context expired")
	}

	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin never received request.cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}

	state, stateErr := supervisor.State()
	if state != StateRunning {
		t.Fatalf("state after cancellation = %s (%v), want running", state, stateErr)
	}
	healthCtx, cancelHealth := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelHealth()
	if _, err := supervisor.Health(healthCtx); err != nil {
		t.Fatalf("Health() after cancellation error = %v", err)
	}
}
