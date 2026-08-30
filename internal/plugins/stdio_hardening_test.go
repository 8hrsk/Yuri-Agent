package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func helperClient(t *testing.T, config ClientConfig, env ...string) *Client {
	t.Helper()
	config.Executable = os.Args[0]
	config.Args = []string{"-test.run=TestPluginHelperProcess"}
	config.Env = append([]string{"YURI_PLUGIN_HELPER=1"}, env...)
	client, err := NewClient(context.Background(), config)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// oversizedArguments exceeds any platform pipe buffer, so the frame cannot be
// absorbed by the kernel and the write really does have to block.
func oversizedArguments() json.RawMessage {
	return json.RawMessage(`{"message":"` + strings.Repeat("x", 1<<20) + `"}`)
}

// H-4. A plugin that never reads its stdin must not wedge the caller: the
// write has to observe the caller's context.
func TestClientWriteRespectsContextWhenPluginIgnoresStdin(t *testing.T) {
	client := helperClient(t, ClientConfig{
		MaxMessageBytes: 4 << 20,
		WriteTimeout:    30 * time.Second,
		CloseTimeout:    200 * time.Millisecond,
	}, "YURI_PLUGIN_IGNORE_STDIN=1")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: oversizedArguments()})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("InvokeTool() error = %v, want context deadline", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("InvokeTool() blocked on a plugin that never reads stdin")
	}
}

// H-4. The write is also bounded on its own, so a caller with a generous
// context still fails instead of hanging until the process is killed.
func TestClientWriteDeadlineFailsFast(t *testing.T) {
	client := helperClient(t, ClientConfig{
		MaxMessageBytes: 4 << 20,
		WriteTimeout:    150 * time.Millisecond,
		CloseTimeout:    200 * time.Millisecond,
	}, "YURI_PLUGIN_IGNORE_STDIN=1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := client.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: oversizedArguments()})
	if !errors.Is(err, ErrPluginExited) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("InvokeTool() error = %v, want a bounded stdin write failure", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("InvokeTool() took %s, want the write deadline to apply", elapsed)
	}
	if !isClosed(client.Done()) {
		t.Fatal("a desynchronized stdin stream must terminate the session")
	}
}

// M-30. cmd.Wait must not be able to reap the process before the last protocol
// frame has been read out of the pipe.
func TestClientDrainsStdoutBeforeReapingProcess(t *testing.T) {
	for attempt := 0; attempt < 15; attempt++ {
		client := helperClient(t, ClientConfig{CloseTimeout: time.Second}, "YURI_PLUGIN_EXIT_AFTER_TOOL=1")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := client.Handshake(ctx, HandshakeParams{CoreVersion: "0.3.0", PluginID: "example.reference"}); err != nil {
			cancel()
			t.Fatalf("attempt %d: Handshake() error = %v", attempt, err)
		}
		result, err := client.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: json.RawMessage(`{"message":"bye"}`)})
		if err != nil {
			cancel()
			t.Fatalf("attempt %d: the final tool_result was lost to the process reap: %v", attempt, err)
		}
		if !result.OK || string(result.Output) != `{"message":"bye"}` {
			cancel()
			t.Fatalf("attempt %d: result = %#v", attempt, result)
		}
		cancel()
		_ = client.Close()
	}
}

// L-23. A burst of events beyond the buffer must degrade, not terminate the
// session and trigger a restart loop.
func TestClientDropsEventsInsteadOfKillingSession(t *testing.T) {
	client := helperClient(t, ClientConfig{CloseTimeout: time.Second}, "YURI_PLUGIN_EVENT_FLOOD=400")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Handshake(ctx, HandshakeParams{CoreVersion: "0.3.0", PluginID: "example.reference"}); err != nil {
		t.Fatalf("Handshake() error = %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for client.DroppedEvents() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if client.DroppedEvents() == 0 {
		t.Fatal("expected the event burst to overflow the buffer")
	}
	if err := client.Err(); err != nil {
		t.Fatalf("event overflow terminated the session: %v", err)
	}
	if _, err := client.Health(ctx, HealthParams{}); err != nil {
		t.Fatalf("Health() after event overflow error = %v", err)
	}
}

// L-24. Crash diagnostics are captured, bounded, and never carry a secret into
// the host's error path.
func TestClientCapturesBoundedRedactedStderr(t *testing.T) {
	const secret = "sk-live-000111222333444555"
	client := helperClient(t, ClientConfig{CloseTimeout: time.Second, MaxStderrBytes: 4096},
		"YURI_PLUGIN_CRASH_ON_TOOL=1",
		"YURI_PLUGIN_STDERR=panic: config load failed: api_key="+secret+" authorization: Bearer "+secret,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Handshake(ctx, HandshakeParams{CoreVersion: "0.3.0", PluginID: "example.reference"}); err != nil {
		t.Fatalf("Handshake() error = %v", err)
	}
	if _, err := client.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: json.RawMessage(`{}`)}); !errors.Is(err, ErrPluginExited) {
		t.Fatalf("InvokeTool() error = %v, want ErrPluginExited", err)
	}
	select {
	case <-client.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("client did not observe the plugin exit")
	}
	tail := client.StderrTail()
	if !strings.Contains(tail, "panic: config load failed") {
		t.Fatalf("stderr tail lost the diagnostic: %q", tail)
	}
	if strings.Contains(tail, secret) {
		t.Fatalf("stderr tail leaked a secret: %q", tail)
	}
	if len(tail) > 4096+8 {
		t.Fatalf("stderr tail is unbounded: %d bytes", len(tail))
	}
}

func TestStderrCaptureIsBoundedAndRedacted(t *testing.T) {
	capture := newStderrCapture(1024, nil)
	for i := 0; i < 200; i++ {
		if _, err := capture.Write([]byte(strings.Repeat("a", 64) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot := capture.Snapshot(); len(snapshot) > 1024+8 {
		t.Fatalf("snapshot = %d bytes, want it bounded to 1024", len(snapshot))
	}

	cases := []struct {
		name  string
		input string
		leak  string
	}{
		{"openai key", "auth failed for sk-live-abcdef0123456789", "sk-live-abcdef0123456789"},
		{"assignment", "OPENAI_API_KEY=hunter2hunter2hunter2 not accepted", "hunter2hunter2hunter2"},
		{"json", `{"token":"eyJhbGciOiJIUzI1NiJ9payload"}`, "eyJhbGciOiJIUzI1NiJ9payload"},
		{"bearer", "Authorization: Bearer abc.def.ghi-jkl", "abc.def.ghi-jkl"},
		{"jwt", "rejected eyJhbGciOi.eyJzdWIiOjEyMzQ1.SflKxwRJSM", "eyJhbGciOi.eyJzdWIiOjEyMzQ1.SflKxwRJSM"},
		{"password", "password: correct-horse-battery", "correct-horse-battery"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			redacted := redactDiagnostics(testCase.input)
			if strings.Contains(redacted, testCase.leak) {
				t.Fatalf("redactDiagnostics(%q) = %q, still contains the secret", testCase.input, redacted)
			}
			if !strings.Contains(redacted, "[REDACTED]") {
				t.Fatalf("redactDiagnostics(%q) = %q, want a redaction marker", testCase.input, redacted)
			}
		})
	}
	if got := redactDiagnostics("connection refused on 127.0.0.1:9000"); got != "connection refused on 127.0.0.1:9000" {
		t.Fatalf("redactDiagnostics() mangled a harmless diagnostic: %q", got)
	}
}

// M-28. A plugin that starts cleanly and then dies resets the consecutive
// attempt counter on every cycle. Only a sliding window stops the loop.
func TestSupervisorCrashLoopTripsTheBreaker(t *testing.T) {
	supervisor, cleanup := testSupervisorWith(t, func(config *SupervisorConfig) {
		config.Restart = RestartPolicy{
			MaxAttempts:          50,
			InitialBackoff:       time.Millisecond,
			MaxBackoff:           5 * time.Millisecond,
			RestartWindow:        10 * time.Second,
			MaxRestartsPerWindow: 2,
		}
	}, "YURI_PLUGIN_EXIT_AFTER_HEALTH_MS=30")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, stateErr := supervisor.State()
		if state == StateFailed {
			if stateErr == nil || !strings.Contains(stateErr.Error(), "automatic restart disabled") {
				t.Fatalf("failure reason = %v, want the restart breaker", stateErr)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s (%v), want the crash loop to trip the breaker", state, stateErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A plugin that ignores stdin entirely cannot be started, and that failure has
// to be reported instead of blocking the caller forever.
func TestSupervisorStartFailsFastOnPluginThatIgnoresStdin(t *testing.T) {
	supervisor, cleanup := testSupervisorWith(t, func(config *SupervisorConfig) {
		config.CancelGrace = 150 * time.Millisecond
		config.Client.CloseTimeout = 200 * time.Millisecond
	}, "YURI_PLUGIN_IGNORE_STDIN=1")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := supervisor.Start(ctx); err == nil {
		t.Fatal("Start() unexpectedly succeeded for a plugin that ignores stdin")
	}
	state, _ := supervisor.State()
	if state != StateFailed {
		t.Fatalf("state = %s, want failed", state)
	}
}

// L-23. The drop counter has to be readable at the layer that owns the plugin,
// otherwise a burst is discarded with nothing left to show for it.
func TestSupervisorSurfacesDroppedEvents(t *testing.T) {
	supervisor, cleanup := testSupervisor(t, "YURI_PLUGIN_EVENT_FLOOD=400")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for supervisor.DroppedEvents() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if supervisor.DroppedEvents() == 0 {
		t.Fatal("the supervisor does not surface the client's dropped-event counter")
	}
	if state, err := supervisor.State(); err != nil || state != StateRunning {
		t.Fatalf("an event burst changed the lifecycle state: %s, %v", state, err)
	}
}
