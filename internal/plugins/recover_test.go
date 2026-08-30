package plugins

import (
	"context"
	"errors"
	"os"
	"strings"

	"testing"
	"time"
)

// Every test in this file drives a real child process speaking the real stdio
// protocol (the TestPluginHelperProcess peer re-executed out of this test
// binary). Only the panic itself is injected, through the unexported
// goroutineFaultHook seam, because the package has no reachable panic today.
//
// Each test asserts an *observable outcome* — the error a blocked caller
// receives, the supervisor's lifecycle state, the child being reaped — never
// mere survival. A test that only checked "the process did not die" would pass
// against a bare recover() with no reporting, which leaves the object claiming
// health forever; that is the failure mode these assertions exist to catch.

// injectPanic arms one fault site for the duration of one test. It deliberately
// fires every time that site is reached rather than only once: goroutines
// belonging to an earlier test's client can still be shutting down while this
// test runs, and a one-shot hook would be spent on one of those instead of on
// the goroutine under test. Every extra firing lands in a guarded goroutine and
// is recovered, which is the point.
func injectPanic(t *testing.T, site string, value string) {
	t.Helper()
	hook := func(fired string) {
		if fired == site {
			panic(value)
		}
	}
	goroutineFaultHook.Store(&hook)
	t.Cleanup(func() { goroutineFaultHook.Store(nil) })
}

func faultClient(t *testing.T, ctx context.Context, env ...string) *Client {
	t.Helper()
	client, err := NewClient(ctx, ClientConfig{
		Executable: os.Args[0],
		Args:       []string{"-test.run=TestPluginHelperProcess"},
		Env:        append([]string{"YURI_PLUGIN_HELPER=1"}, env...),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func waitForDone(t *testing.T, client *Client, within time.Duration) {
	t.Helper()
	select {
	case <-client.Done():
	case <-time.After(within):
		t.Fatalf("client session never terminated: Done() is still open after %s, Err() = %v", within, client.Err())
	}
}

func waitForState(t *testing.T, supervisor *Supervisor, want LifecycleState, within time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(within)
	var state LifecycleState
	var stateErr error
	for time.Now().Before(deadline) {
		state, stateErr = supervisor.State()
		if state == want {
			return stateErr
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("State() = %q (%v), want %q within %s", state, stateErr, want, within)
	return nil
}

// TestReadStdoutPanicFailsTheSessionInsteadOfTheProcess covers the decoder that
// reads bytes a third-party plugin fully controls. The caller that was waiting
// on the frame being decoded must receive a real protocol error, not block.
func TestReadStdoutPanicFailsTheSessionInsteadOfTheProcess(t *testing.T) {
	injectPanic(t, faultReadStdout, "read_stdout fault")
	client := faultClient(t, context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Handshake(ctx, HandshakeParams{CoreVersion: "0.3.0", PluginID: "example.reference"})
	if err == nil {
		t.Fatal("Handshake() succeeded although the decoder panicked on its reply")
	}
	if !errors.Is(err, ErrInvalidProtocol) || !strings.Contains(err.Error(), "read_stdout fault") {
		t.Fatalf("Handshake() error = %v, want an ErrInvalidProtocol naming the decoder fault", err)
	}
	waitForDone(t, client, 5*time.Second)
	if sessionErr := client.Err(); !errors.Is(sessionErr, ErrInvalidProtocol) {
		t.Fatalf("Err() = %v, want ErrInvalidProtocol", sessionErr)
	}
	select {
	case _, open := <-client.Events():
		if open {
			t.Fatal("Events() delivered a frame after the decoder died")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Events() was never closed, so a consumer would range forever")
	}
}

// TestWaitProcessPanicStillReportsTheExit covers the only goroutine that reaps
// the child. The failure must name the fault rather than being papered over by
// the slower clean-EOF fallback, which would report a plain exit.
func TestWaitProcessPanicStillReportsTheExit(t *testing.T) {
	injectPanic(t, faultWaitProcess, "wait_process fault")
	client := faultClient(t, context.Background(), "YURI_PLUGIN_EXIT_AFTER_HEALTH_MS=50")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Handshake(ctx, HandshakeParams{CoreVersion: "0.3.0", PluginID: "example.reference"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Health(ctx, HealthParams{}); err != nil {
		t.Fatal(err)
	}
	waitForDone(t, client, 5*time.Second)
	sessionErr := client.Err()
	if !errors.Is(sessionErr, ErrPluginExited) {
		t.Fatalf("Err() = %v, want ErrPluginExited", sessionErr)
	}
	if !strings.Contains(sessionErr.Error(), "wait_process fault") {
		t.Fatalf("Err() = %v, want the reported failure to name the fault; a bare recover leaves the "+
			"clean-EOF fallback to report a plain exit instead", sessionErr)
	}
}

// TestWriteLoopPanicReleasesTheBlockedCaller covers the single owner of stdin.
// Its caller is parked on a per-request channel that only this goroutine ever
// signals, so a silent death would block that caller until its context expires.
func TestWriteLoopPanicReleasesTheBlockedCaller(t *testing.T) {
	injectPanic(t, faultWriteLoop, "write_loop fault")
	client := faultClient(t, context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	_, err := client.Handshake(ctx, HandshakeParams{CoreVersion: "0.3.0", PluginID: "example.reference"})
	if err == nil {
		t.Fatal("Handshake() succeeded although the writer panicked on its frame")
	}
	if !errors.Is(err, ErrPluginExited) || !strings.Contains(err.Error(), "write_loop fault") {
		t.Fatalf("Handshake() error = %v after %s, want an ErrPluginExited naming the writer fault",
			err, time.Since(start))
	}
	waitForDone(t, client, 5*time.Second)
	if sessionErr := client.Err(); !errors.Is(sessionErr, ErrPluginExited) {
		t.Fatalf("Err() = %v, want ErrPluginExited; a session whose writer is gone must not look healthy", sessionErr)
	}
	// A later caller must fail fast too rather than queue behind a writer that
	// no longer exists.
	if _, err := client.Health(ctx, HealthParams{}); !errors.Is(err, ErrPluginExited) {
		t.Fatalf("Health() after the writer died = %v, want ErrPluginExited", err)
	}
}

// TestCloseOnContextPanicStillKillsTheChild covers the goroutine that turns
// lifecycle-context cancellation into a dead process. The child here never
// reads EOF on stdin, so nothing but this path can stop it: if the recovery
// does not kill the process group, a third-party process keeps running with the
// owner's granted capabilities.
func TestCloseOnContextPanicStillKillsTheChild(t *testing.T) {
	injectPanic(t, faultCloseOnContext, "close_on_context fault")
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	client := faultClient(t, lifecycle)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Handshake(ctx, HandshakeParams{CoreVersion: "0.3.0", PluginID: "example.reference"}); err != nil {
		t.Fatal(err)
	}

	cancelLifecycle()
	// Done() closing is the observable for the child being gone. Nothing else
	// can close it here: with no reporting, Close never runs, the session never
	// terminates, the host end of stdin is never released, and the child sits
	// in its read loop forever holding the capabilities the owner granted it.
	waitForDone(t, client, 5*time.Second)
	if sessionErr := client.Err(); !errors.Is(sessionErr, ErrPluginExited) {
		t.Fatalf("Err() = %v, want ErrPluginExited after the cancelled lifecycle killed the child", sessionErr)
	}
}

// TestSupervisorWatchPanicFailsTheSupervisor covers the goroutine that owns the
// supervisor's whole crash story. It also proves the supervisor mutex is
// released: State and Health both take it, so a lock stranded by the panic
// would hang this test rather than fail it.
func TestSupervisorWatchPanicFailsTheSupervisor(t *testing.T) {
	injectPanic(t, faultSupervisorWatch, "supervisor_watch fault")
	supervisor, cleanup := testSupervisorWith(t, func(config *SupervisorConfig) {
		config.Restart = RestartPolicy{MaxAttempts: 3, InitialBackoff: 10 * time.Millisecond}
	}, "YURI_PLUGIN_CRASH_ON_TOOL=1")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: []byte(`{}`)}); !errors.Is(err, ErrPluginExited) {
		t.Fatalf("InvokeTool() error = %v, want ErrPluginExited", err)
	}

	stateErr := waitForState(t, supervisor, StateFailed, 10*time.Second)
	if stateErr == nil || !strings.Contains(stateErr.Error(), "supervisor_watch fault") {
		t.Fatalf("State() error = %v, want a failure naming the watcher fault", stateErr)
	}
	// A supervisor mutex left held by the panic would block here forever.
	if _, err := supervisor.Health(ctx); !errors.Is(err, ErrPluginNotReady) {
		t.Fatalf("Health() = %v, want ErrPluginNotReady from a failed supervisor", err)
	}
	if _, err := supervisor.Events(); !errors.Is(err, ErrPluginNotReady) {
		t.Fatalf("Events() = %v, want ErrPluginNotReady; a failed supervisor must not hand out a dead channel", err)
	}
	// StateFailed is terminal: the panic must not be able to drive the restart
	// path, which is what keeps it from resetting the 5/60s crash-loop window.
	time.Sleep(300 * time.Millisecond)
	if state, _ := supervisor.State(); state != StateFailed {
		t.Fatalf("State() = %q after the recovery settled, want it to stay failed with no restart", state)
	}
}

// TestSupervisorRestartPanicFailsTheSupervisor covers the automatic-restart
// goroutine. Its own cleanup takes the supervisor mutex, so a panic inside a
// critical section that did not unlock with defer would deadlock the goroutine
// before restartWG.Done ever ran.
func TestSupervisorRestartPanicFailsTheSupervisor(t *testing.T) {
	injectPanic(t, faultSupervisorRestart, "supervisor_restart fault")
	supervisor, cleanup := testSupervisorWith(t, func(config *SupervisorConfig) {
		config.Restart = RestartPolicy{MaxAttempts: 3, InitialBackoff: 10 * time.Millisecond}
	}, "YURI_PLUGIN_CRASH_ON_TOOL=1")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: []byte(`{}`)}); !errors.Is(err, ErrPluginExited) {
		t.Fatalf("InvokeTool() error = %v, want ErrPluginExited", err)
	}

	stateErr := waitForState(t, supervisor, StateFailed, 10*time.Second)
	if stateErr == nil || !strings.Contains(stateErr.Error(), "supervisor_restart fault") {
		t.Fatalf("State() error = %v, want a failure naming the restart fault", stateErr)
	}
	if _, err := supervisor.Health(ctx); !errors.Is(err, ErrPluginNotReady) {
		t.Fatalf("Health() = %v, want ErrPluginNotReady from a failed supervisor", err)
	}
	// clearRestarting must have run, otherwise the supervisor could never
	// schedule another restart for the rest of its life.
	supervisor.mu.Lock()
	restarting := supervisor.restarting
	supervisor.mu.Unlock()
	if restarting {
		t.Fatal("restarting flag is still set: a panicking restart goroutine wedged the restart path permanently")
	}
}

// TestSupervisorPanicCannotBypassTheCrashLoopWindow answers the M-28 question
// directly: a panic in the watcher must not become a way to keep restarting a
// plugin outside the 5/60s budget. The recovery deliberately declines to
// restart at all, so the crash record can only ever grow, never reset.
func TestSupervisorPanicCannotBypassTheCrashLoopWindow(t *testing.T) {
	injectPanic(t, faultSupervisorWatch, "supervisor_watch fault")
	supervisor, cleanup := testSupervisorWith(t, func(config *SupervisorConfig) {
		config.Restart = RestartPolicy{MaxAttempts: 10, InitialBackoff: time.Millisecond, MaxRestartsPerWindow: 2}
	}, "YURI_PLUGIN_CRASH_ON_TOOL=1")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: []byte(`{}`)}); !errors.Is(err, ErrPluginExited) {
		t.Fatalf("InvokeTool() error = %v, want ErrPluginExited", err)
	}
	waitForState(t, supervisor, StateFailed, 10*time.Second)

	supervisor.mu.Lock()
	crashes := len(supervisor.crashes)
	supervisor.mu.Unlock()
	// The panic fires before recordCrashLocked, so the window is simply not
	// advanced. What matters is that it is never rewound and that no restart
	// was scheduled behind its back.
	if crashes > 1 {
		t.Fatalf("crash window holds %d entries, want at most the single crash that happened", crashes)
	}
	for i := 0; i < 5; i++ {
		if state, _ := supervisor.State(); state != StateFailed {
			t.Fatalf("State() = %q, want failed; a panic must not reopen the restart path", state)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
