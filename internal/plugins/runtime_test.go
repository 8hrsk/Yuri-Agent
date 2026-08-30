package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// TestPluginHelperProcess is a small protocol peer used by integration tests.
// It runs in a copied test binary so the production runtime is tested against
// a real child process, not an in-memory fake.
func TestPluginHelperProcess(t *testing.T) {
	if os.Getenv("YURI_PLUGIN_HELPER") != "1" {
		return
	}
	if text := os.Getenv("YURI_PLUGIN_STDERR"); text != "" {
		_, _ = fmt.Fprintln(os.Stderr, text)
	}
	if os.Getenv("YURI_PLUGIN_IGNORE_STDIN") == "1" {
		// Deliberately never drains stdin. A host that writes more than one
		// pipe buffer must not be wedged forever by this.
		time.Sleep(10 * time.Second)
		return
	}

	var writeMu sync.Mutex
	encoder := json.NewEncoder(os.Stdout)
	write := func(envelope Envelope) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = encoder.Encode(envelope)
	}

	var cancelMu sync.Mutex
	cancels := make(map[string]chan struct{})

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	for scanner.Scan() {
		var request Envelope
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return
		}
		switch request.Type {
		case MessageEvent:
			continue
		case MessageType("request.cancel"):
			// Written as a literal so this helper also compiles against the
			// pre-fix protocol, where the type constant does not exist.
			var params CancelParams
			_ = json.Unmarshal(request.Payload, &params)
			if marker := os.Getenv("YURI_PLUGIN_CANCEL_MARKER"); marker != "" {
				_ = os.WriteFile(marker, []byte(params.RequestID), 0o600)
			}
			cancelMu.Lock()
			stop := cancels[params.RequestID]
			delete(cancels, params.RequestID)
			cancelMu.Unlock()
			if stop != nil {
				close(stop)
			}
			continue
		case MessageHandshake, MessageHealth, MessageToolInvoke, MessageShutdown:
		default:
			return
		}
		var response Envelope
		switch request.Type {
		case MessageHandshake:
			var params HandshakeParams
			if err := json.Unmarshal(request.Payload, &params); err != nil {
				return
			}
			response, _ = NewTypedResponse(MessageHandshakeResult, request.ID, HandshakeResult{
				ProtocolVersion: params.ProtocolVersion,
				PluginID:        params.PluginID,
				PluginVersion:   "1.0.0",
				Accepted:        os.Getenv("YURI_PLUGIN_NOT_READY") != "1",
			}, nil)
			if count := os.Getenv("YURI_PLUGIN_EVENT_FLOOD"); count != "" {
				write(response)
				total, _ := strconv.Atoi(count)
				for i := 0; i < total; i++ {
					event, err := NewEvent("noise", map[string]any{"index": i})
					if err != nil {
						return
					}
					write(event)
				}
				continue
			}
		case MessageHealth:
			if os.Getenv("YURI_PLUGIN_DELAY_HEALTH") == "1" {
				time.Sleep(250 * time.Millisecond)
			}
			response, _ = NewTypedResponse(MessageHealthResult, request.ID, HealthResult{Status: "ok", PluginID: "example.reference", PluginVersion: "1.0.0", ProtocolVersion: ProtocolVersion, CheckedAt: time.Now().UTC()}, nil)
			if delay := os.Getenv("YURI_PLUGIN_EXIT_AFTER_HEALTH_MS"); delay != "" {
				write(response)
				milliseconds, _ := strconv.Atoi(delay)
				time.Sleep(time.Duration(milliseconds) * time.Millisecond)
				os.Exit(3)
			}
		case MessageToolInvoke:
			if os.Getenv("YURI_PLUGIN_CRASH_ON_TOOL") == "1" {
				os.Exit(17)
			}
			var params ToolInvokeParams
			if err := json.Unmarshal(request.Payload, &params); err != nil {
				return
			}
			if os.Getenv("YURI_PLUGIN_SLOW_TOOL") == "1" {
				stop := make(chan struct{})
				cancelMu.Lock()
				cancels[request.ID] = stop
				cancelMu.Unlock()
				go func(id string, arguments json.RawMessage) {
					select {
					case <-stop:
						cancelled, _ := NewTypedResponse(MessageError, id, nil, &RPCError{Code: "cancelled", Message: "request cancelled by host"})
						write(cancelled)
					case <-time.After(5 * time.Second):
						completed, _ := NewTypedResponse(MessageToolResult, id, ToolInvokeResult{OK: true, Output: arguments}, nil)
						write(completed)
					}
				}(request.ID, params.Arguments)
				continue
			}
			response, _ = NewTypedResponse(MessageToolResult, request.ID, ToolInvokeResult{OK: true, Output: params.Arguments}, nil)
			if os.Getenv("YURI_PLUGIN_EXIT_AFTER_TOOL") == "1" {
				// Exits the instant the frame is handed to the pipe: the host
				// must still deliver the result rather than ErrPluginExited.
				write(response)
				os.Exit(0)
			}
		case MessageShutdown:
			response, _ = NewTypedResponse(MessageShutdownResult, request.ID, ShutdownResult{Accepted: true}, nil)
			write(response)
			return
		default:
			response, _ = NewTypedResponse(MessageError, request.ID, nil, &RPCError{Code: "method_not_found", Message: "unknown method"})
		}
		if os.Getenv("YURI_PLUGIN_HUGE_RESPONSE") == "1" && request.Type == MessageHealth {
			response.Payload = json.RawMessage(`{"status":"` + strings.Repeat("x", 4096) + `"}`)
		}
		write(response)
	}
}

func copyTestBinary(t *testing.T, dir string) string {
	t.Helper()
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target := filepath.Join(dir, "reference-plugin")
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	return target
}

func testSupervisor(t *testing.T, extraEnv ...string) (*Supervisor, func()) {
	t.Helper()
	return testSupervisorWith(t, nil, extraEnv...)
}

func testSupervisorWith(t *testing.T, customize func(*SupervisorConfig), extraEnv ...string) (*Supervisor, func()) {
	t.Helper()
	packageDir := t.TempDir()
	_ = copyTestBinary(t, packageDir)
	manifest := validManifest("reference-plugin")
	config := SupervisorConfig{
		Manifest:    manifest,
		PackageDir:  packageDir,
		CoreVersion: "0.3.0",
		Authorizer:  AllowAllAuthorizer{},
		DevMode:     true,
		Client: ClientConfig{
			Args: []string{"-test.run=TestPluginHelperProcess"},
			Env:  append([]string{"YURI_PLUGIN_HELPER=1"}, extraEnv...),
		},
	}
	if customize != nil {
		customize(&config)
	}
	supervisor, err := NewSupervisor(config)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = supervisor.Stop(ctx)
	}
	return supervisor, cleanup
}

func TestSupervisorStartInvokeHealthAndStop(t *testing.T) {
	supervisor, cleanup := testSupervisor(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	state, err := supervisor.State()
	if err != nil || state != StateRunning {
		t.Fatalf("State() = %s, %v; want running", state, err)
	}
	if _, err := supervisor.Health(ctx); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	result, err := supervisor.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: json.RawMessage(`{"message":"hello"}`)})
	if err != nil {
		t.Fatalf("InvokeTool() error = %v", err)
	}
	if string(result.Output) != `{"message":"hello"}` {
		t.Fatalf("tool output = %s", result.Output)
	}
	if err := supervisor.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	state, err = supervisor.State()
	if err != nil || state != StateStopped {
		t.Fatalf("State() after stop = %s, %v; want stopped", state, err)
	}
}

func TestSupervisorDoesNotTrustSelfAttestedSignature(t *testing.T) {
	packageDir := t.TempDir()
	_ = copyTestBinary(t, packageDir)
	manifest := validManifest("reference-plugin")
	manifest.Signature = &SignatureMetadata{Algorithm: "ed25519", KeyID: "publisher-key", Value: "self-attested"}
	_, err := NewSupervisor(SupervisorConfig{Manifest: manifest, PackageDir: packageDir, CoreVersion: "0.4.0"})
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("NewSupervisor() error = %v, want unverified signature rejection", err)
	}
	if _, err := NewSupervisor(SupervisorConfig{
		Manifest: manifest, PackageDir: packageDir, CoreVersion: "0.4.0", SignatureVerified: true,
	}); err != nil {
		t.Fatalf("verified signature marker was rejected: %v", err)
	}
}

type recordingAuthorizer struct {
	mu       sync.Mutex
	requests []AuthorizationRequest
	allowed  bool
}

func (a *recordingAuthorizer) Authorize(_ context.Context, request AuthorizationRequest) (AuthorizationResult, error) {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	a.mu.Unlock()
	return AuthorizationResult{Allowed: a.allowed, Reason: "test decision"}, nil
}

func TestSupervisorEnforcesManifestAndAuthorizer(t *testing.T) {
	packageDir := t.TempDir()
	_ = copyTestBinary(t, packageDir)
	manifest := validManifest("reference-plugin")
	manifest.Permissions = []PermissionDeclaration{{Capability: string(domain.CapabilityFilesystemRead), Reason: "read test data", Scope: json.RawMessage(`{"kind":"filesystem","values":["/tmp"]}`)}}
	manifest.Tools[0].Permissions = []string{string(domain.CapabilityFilesystemRead)}
	authorizer := &recordingAuthorizer{allowed: false}
	supervisor, err := NewSupervisor(SupervisorConfig{
		Manifest: manifest, PackageDir: packageDir, CoreVersion: "0.3.0", DevMode: true,
		Authorizer: authorizer,
		Client:     ClientConfig{Args: []string{"-test.run=TestPluginHelperProcess"}, Env: []string{"YURI_PLUGIN_HELPER=1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer supervisor.Stop(context.Background())
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = supervisor.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("InvokeTool() error = %v, want ErrPermissionDenied", err)
	}
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	if len(authorizer.requests) != 1 || authorizer.requests[0].Capability != string(domain.CapabilityFilesystemRead) {
		t.Fatalf("authorization requests = %+v", authorizer.requests)
	}
}

func TestSupervisorReportsCrashWithoutTakingDownHost(t *testing.T) {
	supervisor, cleanup := testSupervisor(t, "YURI_PLUGIN_CRASH_ON_TOOL=1")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := supervisor.InvokeTool(ctx, ToolInvokeParams{ToolID: "echo", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrPluginExited) {
		t.Fatalf("InvokeTool() error = %v, want ErrPluginExited", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, _ := supervisor.State()
		if state == StateCrashed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	state, stateErr := supervisor.State()
	t.Fatalf("state = %s (%v), want crashed", state, stateErr)
}

func TestClientCorrelationAndCancellation(t *testing.T) {
	client, err := NewClient(context.Background(), ClientConfig{
		Executable: os.Args[0],
		Args:       []string{"-test.run=TestPluginHelperProcess"},
		Env:        []string{"YURI_PLUGIN_HELPER=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.Handshake(ctx, HandshakeParams{CoreVersion: "0.3.0", PluginID: "example.reference"}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := client.Health(ctx, HealthParams{})
			errCh <- err
		}()
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent Health() error = %v", err)
		}
	}

	delayed, err := NewClient(context.Background(), ClientConfig{
		Executable: os.Args[0], Args: []string{"-test.run=TestPluginHelperProcess"},
		Env: []string{"YURI_PLUGIN_HELPER=1", "YURI_PLUGIN_DELAY_HEALTH=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer delayed.Close()
	if _, err := delayed.Handshake(ctx, HandshakeParams{CoreVersion: "0.3.0", PluginID: "example.reference"}); err != nil {
		t.Fatal(err)
	}
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer timeoutCancel()
	_, err = delayed.Health(timeoutCtx, HealthParams{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Health() cancellation error = %v, want deadline", err)
	}
}

func TestClientRejectsOversizedMessage(t *testing.T) {
	client, err := NewClient(context.Background(), ClientConfig{
		Executable: os.Args[0], Args: []string{"-test.run=TestPluginHelperProcess"},
		Env: []string{"YURI_PLUGIN_HELPER=1", "YURI_PLUGIN_HUGE_RESPONSE=1"}, MaxMessageBytes: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Health(ctx, HealthParams{}); !errors.Is(err, ErrHealthFailed) || !strings.Contains(err.Error(), ErrMessageTooLarge.Error()) {
		t.Fatalf("Health() error = %v, want oversized health failure", err)
	}
}

func TestProtocolEnvelopeRejectsInvalidFields(t *testing.T) {
	bad := []Envelope{
		{Protocol: ProtocolName, Type: MessageHealth, ID: ""},
		{Protocol: ProtocolName, Type: MessageHealthResult, ID: "1", Payload: nil},
		{Protocol: ProtocolName, Type: MessageEvent, ID: "unexpected", ReplyTo: "reply"},
	}
	for _, envelope := range bad {
		if err := envelope.Validate(); err == nil {
			t.Errorf("Validate(%+v) unexpectedly succeeded", envelope)
		}
	}
	if _, err := NewRequest("1", MethodHealth, func() {}); err == nil || !strings.Contains(err.Error(), ErrInvalidProtocol.Error()) {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if _, err := NewResponse("1", nil, &RPCError{}); err == nil || !strings.Contains(err.Error(), ErrInvalidProtocol.Error()) {
		t.Fatalf("NewResponse() error = %v", err)
	}
}
